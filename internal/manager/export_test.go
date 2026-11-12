package manager

import (
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/dns"
)

// FillDisk makes every later write to the manager's database fail as on a full disk.
func (m *Manager) FillDisk() { m.db.FillDisk() }

// Elapsed is the monotonic clock the manager uses.
func (m *Manager) Elapsed() time.Duration { return m.cfg.Elapsed() }

// Boot is the boot ID the manager uses.
func (m *Manager) Boot() string { return m.cfg.Boot() }

// IssuanceZone is the zone the real domains give the certificate authority for an issuance, on provider p.
func IssuanceZone(i Issuance, p dns.Provider, b dns.Book) *dns.Owned {
	return realDomains{book: b}.owned(i, p)
}
