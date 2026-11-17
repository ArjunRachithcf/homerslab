package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homeward/internal/appunit"
	"github.com/ArjunRachithcf/homeward/internal/atomicfile"
	"github.com/ArjunRachithcf/homeward/internal/migrate"
	"github.com/ArjunRachithcf/homeward/internal/storage"
)

// renamed puts back, under Homeward's names, what the agent derives from its state on a node that was HomersLab
// (#250): migrate.Run moved the state and left its mark, and dropped the old files. Every share's unit, every app's
// compose file and unit, the edge's drop-in and the sign-in's and its LDAP outpost's are written again and enabled,
// and the units restart (queued, not waited for). The LDAPS port follows at the next check, as it always does. The mark
// goes only once every step worked, so an agent that stops halfway does it all again as it next starts. The caller
// holds a.mu.
func (a *Agent) renamed(ctx context.Context) error {
	mark := filepath.Join(a.cfg.StateDir, migrate.Mark)
	if _, err := os.Stat(mark); errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	var enable, restart []string
	for _, id := range slices.Sorted(maps.Keys(a.state.Storage)) {
		s := a.state.Storage[id]
		if s.Kind != "share" {
			continue
		}
		unit := path.Join(UnitsDir, storage.Unit(id))
		if err := a.writeAt(unit, storage.ShareUnit(a.share(id, s))); err != nil {
			return fmt.Errorf("a share's unit: %w", err)
		}
		enable, restart = append(enable, "/"+unit), append(restart, storage.Unit(id))
	}
	if e := a.state.Edge; e != nil && len(e.Domains) > 0 {
		if err := a.writeAt(a.edgeDropIn(), a.edgeCredentials()); err != nil {
			return fmt.Errorf("the edge's drop-in: %w", err)
		}
		enable, restart = append(enable, a.cfg.Edge.Unit), append(restart, a.cfg.Edge.Unit)
	}
	if s := a.state.SignIn; s != nil {
		if err := a.writeAt(a.signInDropIn(), a.signInCredentials(s.Bootstrapping)); err != nil {
			return fmt.Errorf("the sign-in's drop-in: %w", err)
		}
		enable, restart = append(enable, SignInUnit), append(restart, SignInUnit)
		if s.LDAP {
			if err := a.writeAt(a.ldapDropIn(), a.ldapCredentials()); err != nil {
				return fmt.Errorf("the LDAP outpost's drop-in: %w", err)
			}
			enable, restart = append(enable, LDAPUnit), append(restart, LDAPUnit)
		}
	}
	for _, id := range slices.Sorted(maps.Keys(a.state.Apps)) {
		u := a.appUnit(id, a.state.Apps[id])
		if err := atomicfile.Write(u.OverrideFile(), appunit.Override(u)); err != nil {
			return fmt.Errorf("an app's compose file: %w", err)
		}
		unit := path.Join(UnitsDir, appunit.Unit(id))
		if err := a.writeAt(unit, appunit.UnitFile(u)); err != nil {
			return fmt.Errorf("an app's unit: %w", err)
		}
		enable, restart = append(enable, "/"+unit), append(restart, appunit.Unit(id))
	}
	steps := [][]string{{"daemon-reload"}}
	if len(enable) > 0 {
		steps = append(steps, append([]string{"enable"}, enable...), []string{"daemon-reload"},
			append([]string{"--no-block", "restart"}, restart...))
	}
	for _, s := range steps {
		if err := a.run(ctx, "systemctl", s...); err != nil {
			return err
		}
	}
	if err := os.Remove(mark); err != nil {
		return err
	}
	a.log.Info("the node runs under Homeward's names", "units", strings.Join(restart, " "))
	return nil
}

// renameWait is how long writing the units under Homeward's names may take: three systemd reloads and every app's
// unit, more than a check's hostWait on a small machine. It happens once per node.
const renameWait = 2 * time.Minute

// followRename is renamed, at each tick while the mark is there (a stat otherwise), with its own time. A failure is
// said once, like the edge's.
func (a *Agent) followRename(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, renameWait)
	defer cancel()
	a.mu.Lock()
	defer a.mu.Unlock()
	switch err := a.renamed(ctx); {
	case err != nil && a.renameDown != err.Error():
		a.log.Error("writing the node's units under Homeward's names; trying again at each check", "err", err)
		a.renameDown = err.Error()
	case err == nil:
		a.renameDown = ""
	}
}

// renamedMount is a share's mount as an agent before #250 saved it, under Homeward's names: its unit and mount point
// were HomersLab's, and an app's mount must name exactly its share's (appunit.Mount.Checked). Anything else is as it was.
func renamedMount(m appunit.Mount) appunit.Mount {
	if m.Unit == migrate.OldPrefix+"storage-"+m.Location+".service" && m.Source == "/var/lib/homerslab-storage/"+m.Location {
		m.Unit, m.Source = storage.Unit(m.Location), storage.MountPoint(m.Location)
	}
	return m
}
