package certs_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/certs"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// selfSigned is a certificate for names, valid from notBefore for life, and its key.
func selfSigned(t *testing.T, names []string, notBefore time.Time, life time.Duration) ([]byte, mesh.Secret) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(0x16), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: notBefore, NotAfter: notBefore.Add(life)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		mesh.Secret(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd}))
}

// Renewal starts once a third of the lifetime is left, read from the certificate itself: day 60 of 90, day 30 of 45
// (#80).
func TestRenewalStartsWithAThirdOfTheLifetimeLeft(t *testing.T) {
	testenv.Isolate(t)
	start := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	for _, days := range []int{90, 45, 6} {
		life := time.Duration(days) * 24 * time.Hour
		chain, key := selfSigned(t, []string{"*.example.test"}, start, life)
		c, err := certs.Parse("example.test", chain, key)
		if err != nil {
			t.Fatal(err)
		}
		at := start.Add(life * 2 / 3)
		if !c.RenewAt().Equal(at) {
			t.Errorf("%d days: renews at %v, want %v", days, c.RenewAt(), at)
		}
		if c.Due(at.Add(-time.Second)) || !c.Due(at) {
			t.Errorf("%d days: due a second before %v, or not at it", days, at)
		}
	}
}

// A certificate is the domain's wildcard alone, with its own key.
func TestACertificateIsTheDomainsWildcardAlone(t *testing.T) {
	testenv.Isolate(t)
	now := time.Now()
	chain, key := selfSigned(t, []string{"*.example.test"}, now, time.Hour)
	if c, err := certs.Parse("example.test", chain, key); err != nil || c.Serial != "16" || c.Domain != "example.test" {
		t.Fatalf("the wildcard: %+v %v", c, err)
	}
	_, other := selfSigned(t, []string{"*.example.test"}, now, time.Hour)
	if _, err := certs.Parse("example.test", chain, other); err == nil {
		t.Error("a certificate with another certificate's key was taken")
	}
	for _, names := range [][]string{{"*.other.test"}, {"*.example.test", "photos.example.test"}, {"admin.example.test"}} {
		chain, key := selfSigned(t, names, now, time.Hour)
		if _, err := certs.Parse("example.test", chain, key); err == nil {
			t.Errorf("a certificate for %v was taken as *.example.test's", names)
		}
	}
	if k, err := certs.NewKey(); err != nil || !strings.Contains(k.Reveal(), "PRIVATE KEY") || k.String() != "[secret]" {
		t.Errorf("a new key: %v", err)
	}
}
