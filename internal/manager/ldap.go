package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/idp"
	"github.com/ArjunRachithcf/homerslab/internal/manager/plan"
)

// ensureLDAPApps puts each LDAP app's directory back as its plan says, at every pass of the schedule (#204): its
// audience alone, and whatever the outpost's tidying took off (a binding added in the console, its bind flow changed:
// the provider leaves the outpost, and comes back here). The outpost's token goes back to the agent too, which keeps the
// outpost running as it is when nothing changed. An app's failure doesn't keep the others from theirs.
func (m *Manager) ensureLDAPApps(ctx context.Context) error {
	// One at a time with applies: an apply that fails deletes the directory it made, which must be no pass's.
	m.applying.Lock()
	defer m.applying.Unlock()
	apps, err := m.db.Apps(ctx)
	if err != nil {
		return err
	}
	var want []idp.LDAPApp
	var errs []error
	for _, a := range apps {
		b, err := m.db.Plan(ctx, a.Plan)
		var p plan.Plan
		if err == nil {
			err = json.Unmarshal(b, &p)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s's plan: %w", a.ID, err))
			continue
		}
		if p.SignIn.Method == catalog.LDAP {
			want = append(want, idp.LDAPApp{Slug: "homerslab-" + a.ID, Name: p.App.Name, Roles: roles(p.Audience.Default)})
		}
	}
	if len(want) == 0 {
		return errors.Join(errs...)
	}
	ans, err := m.db.Answers(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	provider, err := m.provider(ctx, ans)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	directory, ok := provider.(idp.LDAP)
	if !ok {
		return errors.Join(append(errs, errors.New("the household account serves no LDAP"))...)
	}
	token, err := directory.LDAPOutpost(ctx)
	if err != nil {
		return errors.Join(append(errs, fmt.Errorf("the LDAP outpost: %w", err))...)
	}
	if err := m.agent.ldapOutpost(ctx, token); err != nil {
		errs = append(errs, fmt.Errorf("the LDAP outpost's token: %w", err))
	}
	if err := directory.EnsureLDAPApps(ctx, want); err != nil {
		errs = append(errs, fmt.Errorf("the LDAP directories: %w", err))
	}
	return errors.Join(errs...)
}
