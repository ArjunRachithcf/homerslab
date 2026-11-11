package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/certs"
	"github.com/ArjunRachithcf/homerslab/internal/dns"
	"github.com/ArjunRachithcf/homerslab/internal/manager/setup"
	"github.com/ArjunRachithcf/homerslab/internal/manager/store"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// Domains is how the manager reaches the household's DNS provider and its certificate authority: the dns and certs
// packages (which pass the DNS provider contract suite against a test CA and a test DNS server), or a test's stand-in.
type Domains interface {
	// Zone is the public zone that holds the domain; it checks the token by listing that zone with it.
	Zone(ctx context.Context, s dns.Settings, token mesh.Secret, domain string) (string, error)
	// CAA says whether the zone's CAA records let the authority issue (*certs.CAAForbids when they don't).
	CAA(ctx context.Context, domain string) error
	// Ensure keeps have while it is valid, and otherwise issues the domain's wildcard certificate.
	Ensure(ctx context.Context, i Issuance, have *certs.Certificate, now time.Time) (certs.Certificate, bool, error)
	// Authority names the certificate authority.
	Authority() string
}

// Issuance is one domain's certificate, to keep or to issue.
type Issuance struct {
	Settings     dns.Settings
	Token        mesh.Secret
	Zone, Domain string
	Account      mesh.Secret
	Contact      string
	Fresh        bool
}

type realDomains struct {
	a    certs.Authority
	book dns.Book
}

// ErrDNSRefused is a DNS token the provider didn't take.
var ErrDNSRefused = errors.New("the DNS provider refused the token")

func (d realDomains) Zone(ctx context.Context, s dns.Settings, token mesh.Secret, domain string) (string, error) {
	zone, err := d.a.DNS.Zone(ctx, domain)
	if err != nil {
		return "", err
	}
	p, err := dns.Open(s, token)
	if err != nil {
		return "", err
	}
	if _, err := p.GetRecords(ctx, zone); err != nil {
		return "", fmt.Errorf("%w: %v", ErrDNSRefused, err)
	}
	return zone, nil
}

func (d realDomains) CAA(ctx context.Context, domain string) error { return d.a.CheckCAA(ctx, domain) }

func (d realDomains) Ensure(ctx context.Context, i Issuance, have *certs.Certificate, now time.Time) (certs.Certificate, bool, error) {
	p, err := dns.Open(i.Settings, i.Token)
	if err != nil {
		return certs.Certificate{}, false, err
	}
	zone := &dns.Owned{Provider: p, Zone: i.Zone, Domain: i.Domain, Book: d.book}
	return d.a.Ensure(ctx, certs.Request{Domain: i.Domain, Zone: zone, AccountKey: i.Account, Contact: i.Contact, Fresh: i.Fresh}, have, now)
}

func (d realDomains) Authority() string { return d.a.Name }

// The manager's credentials for the domain, sealed by the agent: the DNS provider's token, the ACME account's key,
// and the main domain's certificate key (handed to the edges).
const (
	credDNSToken   = "manager-dns-token"
	credACME       = "manager-acme-account"
	credTLSKey     = "manager-tls-key"
	ansDomain      = "domain"
	ansDNSProvider = "dnsProvider"
	ansDNSServer   = "dnsServer"
	ansDNSKeyName  = "dnsKeyName"
	ansDNSKeyAlg   = "dnsKeyAlgorithm"
	ansZone        = "dnsZone"
	// ansPort is the household's port, chosen once at the network step (ADR 0021).
	ansPort = "householdPort"
)

var domainName = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]([a-z0-9-]{0,61}[a-z0-9])?$`)

// AdminName is Admin's name on a domain.
func AdminName(domain string) string { return "admin." + domain }

func settingsOf(ans map[string]string) dns.Settings {
	return dns.Settings{Provider: ans[ansDNSProvider], Server: ans[ansDNSServer], KeyName: ans[ansDNSKeyName], KeyAlgorithm: ans[ansDNSKeyAlg]}
}

// domain is the LAN page's domain step: the household's domain and its DNS provider. It carries no secret: the step
// waits for the DNS token, which the admin gives once in this machine's terminal (`sudo homerslab setup secret dns`,
// #64).
func (m *Manager) domain(ctx context.Context, raw json.RawMessage, earlier map[string]string) (map[string]string, error) {
	var a struct {
		Domain       string `json:"domain"`
		Provider     string `json:"provider"`
		Server       string `json:"server"`
		KeyName      string `json:"keyName"`
		KeyAlgorithm string `json:"keyAlgorithm"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, &setup.Invalid{Fields: []string{"answer"}}
	}
	var bad []string
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(a.Domain)), ".")
	if len(d) > 200 || !domainName.MatchString(d) {
		bad = append(bad, "domain")
	}
	s, err := dns.Settings{Provider: a.Provider, Server: a.Server, KeyName: a.KeyName, KeyAlgorithm: a.KeyAlgorithm}.Check()
	var inv *dns.Invalid
	if errors.As(err, &inv) {
		bad = append(bad, inv.Fields...)
	}
	if bad != nil {
		return nil, &setup.Invalid{Fields: bad}
	}
	if have := earlier[ansDomain]; have != "" && have != d {
		if _, err := m.db.Certificate(ctx, have); err == nil {
			// Renaming the main domain is a guided migration of its own.
			return nil, &setup.Invalid{Fields: []string{"domain"}}
		}
	}
	keep := map[string]string{ansDomain: d, ansDNSProvider: s.Provider, ansDNSServer: s.Server, ansDNSKeyName: s.KeyName, ansDNSKeyAlg: s.KeyAlgorithm}
	if earlier[ansDomain] == d && settingsOf(earlier) == s {
		if _, err := m.db.Certificate(ctx, d); err == nil {
			return keep, nil // issued already: answering again changes nothing
		}
	}
	return nil, &setup.NeedsSecret{Secret: "dns", Keep: keep}
}

// Errors of the domain's token and certificate, so the terminal can say what to fix.
var (
	errIssue = errors.New("the certificate wasn't issued")
	errEdge  = errors.New("the edge didn't take the certificate")
	errNames = errors.New("the mesh's names weren't made")
)

// dnsSecret takes the DNS token from the terminal, during first-time setup: it is checked, sealed, and used for the
// domain's certificate, which ends setup's first half: mesh connected, certificate issued.
func (m *Manager) dnsSecret(ctx context.Context, value string, earlier map[string]string) (map[string]string, error) {
	keep, _, err := m.takeDNSToken(ctx, mesh.Secret(strings.TrimSpace(value)), earlier)
	return keep, err
}

// takeDNSToken checks a DNS token against the zone, seals it, and makes sure the domain's certificate is issued and on
// the edge. It returns the answers to keep, and whether a certificate was issued.
func (m *Manager) takeDNSToken(ctx context.Context, token mesh.Secret, ans map[string]string) (map[string]string, bool, error) {
	domain, s := ans[ansDomain], settingsOf(ans)
	zone, err := m.domains.Zone(ctx, s, token, domain)
	if err != nil {
		return nil, false, err
	}
	if err := m.domains.CAA(ctx, domain); err != nil {
		return nil, false, err
	}
	if err := m.agent.seal(ctx, credDNSToken, token); err != nil {
		return nil, false, err
	}
	keep := map[string]string{ansZone: zone}
	if err := m.db.SaveAnswers(ctx, keep); err != nil {
		return nil, false, err
	}
	ans[ansZone] = zone
	issued, err := m.ensureCertificate(ctx, ans, token, false)
	return keep, issued, err
}

// ensureCertificate keeps the main domain's certificate while it is valid, or issues it, then puts it on the edge and
// the household's names on the mesh. It says whether it issued.
func (m *Manager) ensureCertificate(ctx context.Context, ans map[string]string, token mesh.Secret, fresh bool) (bool, error) {
	domain := ans[ansDomain]
	account, err := m.agent.unseal(ctx, credACME)
	if err != nil || account == "" {
		if account, err = certs.NewKey(); err != nil {
			return false, err
		}
		// Kept before its first use: the account a CA knows is never lost half-way.
		if err := m.agent.seal(ctx, credACME, account); err != nil {
			return false, err
		}
	}
	have := m.storedCertificate(ctx, domain)
	c, issued, err := m.domains.Ensure(ctx, Issuance{Settings: settingsOf(ans), Token: token, Zone: ans[ansZone], Domain: domain,
		Account: account, Contact: ans["mail"], Fresh: fresh}, have, m.cfg.Now())
	if err != nil {
		m.log.Warn("the domain's certificate", "domain", domain, "err", err)
		return false, fmt.Errorf("%w: %v", errIssue, err)
	}
	if issued {
		if err := m.agent.seal(ctx, credTLSKey, c.Key); err != nil {
			return false, err
		}
		if err := m.db.SaveCertificate(ctx, store.Certificate{Domain: domain, Chain: c.Chain, Serial: c.Serial, NotBefore: c.NotBefore,
			NotAfter: c.NotAfter, Authority: m.domains.Authority()}); err != nil {
			return false, err
		}
		m.log.Info("issued the domain's certificate", "domain", domain, "serial", c.Serial, "notAfter", c.NotAfter, "renewAt", c.RenewAt())
	} else {
		m.log.Info("kept the domain's certificate: still valid", "domain", domain, "serial", c.Serial, "renewAt", c.RenewAt())
	}
	if err := m.installCertificate(ctx, ans, c); err != nil {
		return issued, err
	}
	return issued, nil
}

// storedCertificate is the domain's certificate as the database and the agent keep it, or nil.
func (m *Manager) storedCertificate(ctx context.Context, domain string) *certs.Certificate {
	row, err := m.db.Certificate(ctx, domain)
	if err != nil {
		return nil
	}
	key, err := m.agent.unseal(ctx, credTLSKey)
	if err != nil {
		return nil
	}
	c, err := certs.Parse(domain, row.Chain, key)
	if err != nil {
		m.log.Warn("the stored certificate doesn't hold together; a new one is issued", "domain", domain, "err", err)
		return nil
	}
	return &c
}

// installCertificate puts the certificate on the primary's edge, with Admin's site at the household's port, and the
// household's names on the mesh, pointing at the primary.
func (m *Manager) installCertificate(ctx context.Context, ans map[string]string, c certs.Certificate) error {
	domain := ans[ansDomain]
	if err := m.agent.edgeSites(ctx, agent.EdgeSites{Port: householdPort(ans), Admin: AdminName(domain)}); err != nil {
		return fmt.Errorf("%w: %v", errEdge, err)
	}
	if err := m.agent.edgeCertificate(ctx, domain, c.Chain, c.Key); err != nil {
		return fmt.Errorf("%w: %v", errEdge, err)
	}
	if err := m.ensureNames(ctx, ans); err != nil {
		return fmt.Errorf("%w: %v", errNames, err)
	}
	return nil
}

// ensureNames makes the household's names resolve to the primary on the mesh.
func (m *Manager) ensureNames(ctx context.Context, ans map[string]string) error {
	ip, err := netip.ParseAddr(ans[ansPrimaryIP])
	if err != nil {
		return errors.New("the primary has no mesh address")
	}
	t, err := m.currentToken(ctx, ans)
	if err != nil {
		return err
	}
	nb, err := m.netbird.Open(ans[ansURL], t)
	if err != nil {
		return err
	}
	return nb.EnsureNames(ctx, ans[ansDomain], ip)
}

// householdPort is the household's port: chosen at the network step, 443 before.
func householdPort(ans map[string]string) int {
	if p, err := strconv.Atoi(ans[ansPort]); err == nil && p > 0 {
		return p
	}
	return 443
}

// renew renews the domain's certificate once a third of its lifetime is left. The first renewal that fails raises a
// warning for the admins, long before the certificate expires (#80). It runs from the scheduler.
func (m *Manager) renew(ctx context.Context) error {
	ans, err := m.db.Answers(ctx)
	if err != nil {
		return err
	}
	domain := ans[ansDomain]
	row, err := m.db.Certificate(ctx, domain)
	if errors.Is(err, store.ErrNotFound) || domain == "" {
		return nil
	}
	if err != nil {
		return err
	}
	c := certs.Certificate{Domain: domain, NotBefore: row.NotBefore, NotAfter: row.NotAfter, Serial: row.Serial}
	if !c.Due(m.cfg.Now()) {
		return nil
	}
	token, err := m.agent.unseal(ctx, credDNSToken)
	if err == nil {
		_, err = m.ensureCertificate(ctx, ans, token, false)
	}
	if err != nil {
		return m.renewalFailed(ctx, row, err)
	}
	return m.db.ResolveWarnings(ctx, warnCertificate+domain, m.cfg.Now())
}

const warnCertificate = "certificate:"

func (m *Manager) renewalFailed(ctx context.Context, row store.Certificate, why error) error {
	first, err := m.db.RenewalFailed(ctx, row.Domain, m.cfg.Now(), why.Error())
	if err != nil {
		return err
	}
	if first {
		what := fmt.Sprintf("The certificate for *.%s couldn't be renewed, and it expires on %s. If the DNS token no longer "+
			"works, give a new one on the primary: sudo homerslab dns token", row.Domain, row.NotAfter.UTC().Format("2 January 2006"))
		if err := m.db.AddWarning(ctx, store.Warning{At: m.cfg.Now(), Source: warnCertificate + row.Domain, What: what}); err != nil {
			return err
		}
	}
	return fmt.Errorf("renewing the certificate for *.%s: %w", row.Domain, why)
}

// DNSStatus is the main domain, its DNS provider and its certificate, for `homerslab dns token` to show before it reads
// a token. None of it is secret.
type DNSStatus struct {
	Domain      string             `json:"domain"`
	Provider    string             `json:"provider"`
	Where       string             `json:"where"`
	Certificate *store.Certificate `json:"certificate,omitempty"`
	RenewAt     time.Time          `json:"renewAt,omitzero"`
}

func (m *Manager) getDNS(w http.ResponseWriter, r *http.Request) {
	ans, err := m.db.Answers(r.Context())
	if err != nil {
		m.internal(w, "reading the answers", err)
		return
	}
	if ans[ansDomain] == "" {
		fail(w, http.StatusConflict, "no-domain")
		return
	}
	s := settingsOf(ans)
	st := DNSStatus{Domain: ans[ansDomain], Provider: s.Provider, Where: s.Where()}
	if row, err := m.db.Certificate(r.Context(), ans[ansDomain]); err == nil {
		st.Certificate = &row
		st.RenewAt = certs.Certificate{NotBefore: row.NotBefore, NotAfter: row.NotAfter}.RenewAt()
	}
	writeJSON(w, http.StatusOK, st)
}

// putDNSToken is `homerslab dns token`, on the control socket only: the console's way to replace the DNS token when an
// expired certificate keeps everyone out of Admin (#80). Root on the primary can already do anything, so it grants
// nothing new; from Admin, the same change is a risky change. The new token is checked against the zone, sealed, and a
// renewal that is due or failing runs right away.
func (m *Manager) putDNSToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value string `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil || strings.TrimSpace(body.Value) == "" {
		fail(w, http.StatusBadRequest, "no-value")
		return
	}
	ctx := r.Context()
	ans, err := m.db.Answers(ctx)
	if err != nil {
		m.internal(w, "reading the answers", err)
		return
	}
	row, err := m.db.Certificate(ctx, ans[ansDomain])
	if ans[ansDomain] == "" || errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusConflict, "no-domain") // first-time setup takes it: `homerslab setup secret dns`
		return
	}
	if err != nil {
		m.internal(w, "reading the certificate", err)
		return
	}
	_, issued, err := m.takeDNSToken(ctx, mesh.Secret(strings.TrimSpace(body.Value)), ans)
	if err != nil {
		if errors.Is(err, errIssue) || errors.Is(err, errEdge) || errors.Is(err, errNames) {
			_ = m.renewalFailed(ctx, row, err)
		}
		m.secretFailed(w, err)
		return
	}
	_ = m.db.ResolveWarnings(ctx, warnCertificate+ans[ansDomain], m.cfg.Now())
	m.log.Info("the DNS token was replaced from the console", "domain", ans[ansDomain], "renewed", issued)
	now, _ := m.db.Certificate(ctx, ans[ansDomain])
	writeJSON(w, http.StatusOK, map[string]any{"renewed": issued, "certificate": now})
}

// secretFailed answers a secret that didn't do its work, saying what to fix and never what it was.
func (m *Manager) secretFailed(w http.ResponseWriter, err error) {
	var forbids *certs.CAAForbids
	switch {
	case errors.Is(err, setup.ErrNotWaiting):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, mesh.ErrTokenRefused), errors.Is(err, ErrDNSRefused):
		fail(w, http.StatusBadRequest, "secret-refused")
	case errors.Is(err, dns.ErrNoZone):
		fail(w, http.StatusBadRequest, "no-zone")
	case errors.As(err, &forbids):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "caa-forbids", "authority": forbids.Authority, "allowed": forbids.Allowed})
	case errors.Is(err, mesh.ErrUnreachable):
		fail(w, http.StatusBadGateway, "mesh-unreachable")
	case errors.Is(err, errJoin):
		fail(w, http.StatusBadGateway, "join-failed")
	case errors.Is(err, errIssue):
		fail(w, http.StatusBadGateway, "issuance-failed")
	case errors.Is(err, errEdge):
		fail(w, http.StatusBadGateway, "edge-failed")
	case errors.Is(err, errNames):
		fail(w, http.StatusBadGateway, "names-failed")
	default:
		m.internal(w, "taking a secret", err)
	}
}

func (m *Manager) getWarnings(w http.ResponseWriter, r *http.Request) {
	ws, err := m.db.Warnings(r.Context(), 100)
	if err != nil {
		m.internal(w, "reading warnings", err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}
