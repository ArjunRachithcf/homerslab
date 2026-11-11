package manager

import "time"

// FillDisk makes every later write to the manager's database fail as on a full disk.
func (m *Manager) FillDisk() { m.db.FillDisk() }

// Elapsed is the monotonic clock the manager uses.
func (m *Manager) Elapsed() time.Duration { return m.cfg.Elapsed() }
