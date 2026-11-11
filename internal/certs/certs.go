// Package certs gets the household's wildcard certificate, one per domain, from an ACME certificate authority through
// a DNS challenge (spec: Names and certificates; ADR 0006). The household's app names never reach public Certificate
// Transparency logs: only *.<domain> does.
//
// The challenge's record goes through the household's DNS provider as one of HomersLab's own records (internal/dns),
// and its propagation is checked at the zone's authoritative servers, found through public resolvers, never the
// machine's own (#79).
package certs

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/libdns/libdns"
	"github.com/mholt/acmez/v3"
	"github.com/mholt/acmez/v3/acme"

	"github.com/ArjunRachithcf/homerslab/internal/dns"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// Authority is an ACME certificate authority.
type Authority struct {
	Name      string // as people know it
	Directory string // its ACME directory
	// IssuerDomains are the names CAA records give it (RFC 8659), such as letsencrypt.org.
	IssuerDomains []string
	// HTTP reaches the directory; the default client when nil (a test CA's roots go here).
	HTTP *http.Client
	// DNS finds the zone, its servers and its CAA records: public resolvers only (#79).
	DNS dns.Resolver
	// Propagation is how long the challenge's record may take to reach every authoritative server, and Poll how
	// often they are asked.
	Propagation, Poll time.Duration
	Log               *slog.Logger
}

// Let's Encrypt, and its staging endpoint for rehearsals and CI, which never use up production's limits (#80).
var (
	LetsEncrypt = Authority{Name: "Let's Encrypt", Directory: "https://acme-v02.api.letsencrypt.org/directory",
		IssuerDomains: []string{"letsencrypt.org"}}
	LetsEncryptStaging = Authority{Name: "Let's Encrypt (staging)", Directory: "https://acme-staging-v02.api.letsencrypt.org/directory",
		IssuerDomains: []string{"letsencrypt.org"}}
)

// Certificate is one domain's wildcard certificate. Only Key is secret.
type Certificate struct {
	Domain    string      `json:"domain"`
	Chain     []byte      `json:"chain"` // PEM, the leaf first
	Key       mesh.Secret `json:"-"`     // PEM
	NotBefore time.Time   `json:"notBefore"`
	NotAfter  time.Time   `json:"notAfter"`
	Serial    string      `json:"serial"`
}

// RenewAt is when a certificate is renewed: once a third of its lifetime is left, read from the certificate itself,
// so shorter lifetimes need no new day counts (#80).
func (c Certificate) RenewAt() time.Time {
	return c.NotAfter.Add(-c.NotAfter.Sub(c.NotBefore) / 3)
}

// Due says whether a certificate needs renewing at now.
func (c Certificate) Due(now time.Time) bool { return !now.Before(c.RenewAt()) }

// Names is what a domain's certificate covers: the wildcard only, so no app's name is ever in a CT log.
func Names(domain string) []string { return []string{"*." + domain} }

// Parse checks that a chain and a key make one domain's wildcard certificate, and reads its dates.
func Parse(domain string, chain []byte, key mesh.Secret) (Certificate, error) {
	pair, err := tls.X509KeyPair(chain, []byte(key.Reveal()))
	if err != nil {
		return Certificate{}, fmt.Errorf("the certificate and its key don't make a pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return Certificate{}, err
	}
	return leafCertificate(domain, chain, key, leaf)
}

func leafCertificate(domain string, chain []byte, key mesh.Secret, leaf *x509.Certificate) (Certificate, error) {
	if err := leaf.VerifyHostname("homerslab-check." + domain); err != nil || len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != "*."+domain {
		return Certificate{}, fmt.Errorf("the certificate is for %v, not *.%s alone", leaf.DNSNames, domain)
	}
	return Certificate{Domain: domain, Chain: chain, Key: key, NotBefore: leaf.NotBefore.UTC(), NotAfter: leaf.NotAfter.UTC(),
		Serial: leaf.SerialNumber.Text(16)}, nil
}

// NewKey is a new private key, PEM: for an ACME account, or a certificate.
func NewKey() (mesh.Secret, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return "", err
	}
	return mesh.Secret(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

func signer(key mesh.Secret) (crypto.Signer, error) {
	b, _ := pem.Decode([]byte(key.Reveal()))
	if b == nil {
		return nil, errors.New("not a PEM key")
	}
	k, err := x509.ParsePKCS8PrivateKey(b.Bytes)
	if err != nil {
		return nil, err
	}
	s, ok := k.(crypto.Signer)
	if !ok {
		return nil, errors.New("the key can't sign")
	}
	return s, nil
}

// CAAForbids is a zone whose CAA records don't let the chosen authority issue: the domain step says so in plain
// words (#80).
type CAAForbids struct {
	Domain    string
	Authority string
	Allowed   []string
}

func (e *CAAForbids) Error() string {
	return fmt.Sprintf("the CAA records of %s let only %s issue certificates, not %s", e.Domain, strings.Join(e.Allowed, ", "), e.Authority)
}

// CheckCAA says whether the domain's CAA records let this authority issue its wildcard certificate.
func (a Authority) CheckCAA(ctx context.Context, domain string) error {
	set, err := a.DNS.CAA(ctx, "*."+domain)
	if err != nil {
		return err
	}
	if dns.AllowsIssuer(set, a.IssuerDomains) {
		return nil
	}
	return &CAAForbids{Domain: domain, Authority: a.Name, Allowed: dns.Issuers(set)}
}

// Request is one issuance: the domain, its zone as HomersLab may change it, and the ACME account.
type Request struct {
	Domain     string
	Zone       *dns.Owned
	AccountKey mesh.Secret
	Contact    string // the admin's mail address
	// Fresh asks for a new certificate even while the one there is valid: the machine that held its key was lost or
	// stolen.
	Fresh bool
}

// Ensure keeps have while it is valid and not due at now, and only otherwise issues a new one: a restore, a rehearsal
// or a reinstall puts the old certificate back instead of using up the authority's limits (#80). It says whether it
// issued.
func (a Authority) Ensure(ctx context.Context, r Request, have *Certificate, now time.Time) (Certificate, bool, error) {
	if have != nil && !r.Fresh && have.Domain == r.Domain && !have.Due(now) && now.After(have.NotBefore) {
		return *have, false, nil
	}
	c, err := a.Issue(ctx, r)
	if err != nil {
		return Certificate{}, false, err
	}
	return c, true, nil
}

// Issue gets a new wildcard certificate for the domain.
func (a Authority) Issue(ctx context.Context, r Request) (Certificate, error) {
	if a.Directory == "" {
		return Certificate{}, errors.New("no certificate authority")
	}
	acctKey, err := signer(r.AccountKey)
	if err != nil {
		return Certificate{}, fmt.Errorf("the ACME account's key: %w", err)
	}
	log := a.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	client := &acmez.Client{
		Client: &acme.Client{Directory: a.Directory, HTTPClient: a.HTTP, UserAgent: "HomersLab", Logger: log},
		ChallengeSolvers: map[string]acmez.Solver{
			acme.ChallengeTypeDNS01: &solver{a: a, zone: r.Zone},
		},
	}
	account := acme.Account{TermsOfServiceAgreed: true, PrivateKey: acctKey}
	if r.Contact != "" {
		account.Contact = []string{"mailto:" + r.Contact}
	}
	// The same key always finds the same account (RFC 8555 §7.3.1): restores and reinstalls make no new one.
	account, err = client.NewAccount(ctx, account)
	if err != nil {
		return Certificate{}, fmt.Errorf("the ACME account at %s: %w", a.Name, err)
	}
	key, err := NewKey()
	if err != nil {
		return Certificate{}, err
	}
	certKey, _ := signer(key)
	chains, err := client.ObtainCertificateForSANs(ctx, account, certKey, Names(r.Domain))
	if err != nil {
		return Certificate{}, fmt.Errorf("the certificate for *.%s: %w", r.Domain, err)
	}
	if len(chains) == 0 {
		return Certificate{}, errors.New("the authority gave no certificate")
	}
	return Parse(r.Domain, chains[0].ChainPEM, key)
}

// solver answers the DNS challenge with one of HomersLab's own records, and waits until every authoritative server
// holds it.
type solver struct {
	a    Authority
	zone *dns.Owned
}

func (s *solver) record(ch acme.Challenge) dns.Record {
	return dns.Record{Name: libdns.RelativeName(ch.DNS01TXTRecordName()+".", s.zone.Zone), Type: "TXT",
		Value: ch.DNS01KeyAuthorization(), TTL: time.Minute}
}

func (s *solver) Present(ctx context.Context, ch acme.Challenge) error {
	return s.zone.Create(ctx, s.record(ch))
}

func (s *solver) Wait(ctx context.Context, ch acme.Challenge) error {
	timeout, poll := s.a.Propagation, s.a.Poll
	if timeout == 0 {
		timeout = 5 * time.Minute
	}
	if poll == 0 {
		poll = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	fqdn := ch.DNS01TXTRecordName() + "."
	for {
		ok, err := s.a.DNS.HasTXT(ctx, s.zone.Zone, fqdn, ch.DNS01KeyAuthorization())
		if ok {
			return nil
		}
		select {
		case <-ctx.Done():
			if err != nil {
				return fmt.Errorf("the challenge's record never reached every name server of %s: %w", s.zone.Zone, err)
			}
			return fmt.Errorf("the challenge's record never reached every name server of %s", s.zone.Zone)
		case <-time.After(poll):
		}
	}
}

func (s *solver) CleanUp(ctx context.Context, ch acme.Challenge) error {
	return s.zone.Delete(ctx, s.record(ch))
}
