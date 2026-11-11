package manager

// FillDisk makes every later write to the manager's database fail as on a full disk.
func (m *Manager) FillDisk() { m.db.FillDisk() }
