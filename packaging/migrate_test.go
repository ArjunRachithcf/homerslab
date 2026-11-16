package packaging_test

import (
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ArjunRachithcf/homeward/internal/migrate"
	"github.com/ArjunRachithcf/homeward/internal/testenv"
)

// A node installed as HomersLab moves to Homeward's names (#250) before systemd-sysusers (which would otherwise make
// the new users with other ids, the fixed ones being the old users'), before the sockets (their users'), the firewall
// table (read from the agent's folder) and the agent, which requires it: an agent never starts without its state.
func TestTheMigrationRunsBeforeTheUsersTheSocketsTheTableAndTheAgent(t *testing.T) {
	testenv.Isolate(t)
	m := section(t, "homeward-migrate.service", "[Unit]")
	before := strings.Fields(strings.Join(m["Before"], " "))
	for _, want := range []string{"systemd-sysusers.service", "homeward-firewall-restore.service", "sysinit.target", "sockets.target"} {
		if !slices.Contains(before, want) {
			t.Errorf("the migration isn't before %s: Before=%q", want, before)
		}
	}
	if one(t, m, "DefaultDependencies") != "no" || !slices.Contains(section(t, "homeward-migrate.service", "[Install]")["WantedBy"], "sysinit.target") {
		t.Error("the migration isn't part of early boot")
	}
	s := unit(t, "homeward-migrate.service")
	if one(t, s, "Type") != "oneshot" || one(t, s, "ExecStart") != "/usr/bin/homeward agent migrate" {
		t.Errorf("the migration runs %v as %v", s["ExecStart"], s["Type"])
	}
	for _, name := range []string{"homeward-admin.socket", "homeward-home.socket", "homeward-home.service", "homeward-edge.service",
		"homeward-ldaps.service", "homeward-manager.service"} {
		u := section(t, name, "[Unit]")
		if !slices.Contains(u["Wants"], "homeward-migrate.service") || !slices.Contains(strings.Fields(strings.Join(u["After"], " ")), "homeward-migrate.service") {
			t.Errorf("%s, which runs as one of the package's users, doesn't wait for the migration", name)
		}
	}
	a := section(t, "homeward-agent.service", "[Unit]")
	if !slices.Contains(a["Requires"], "homeward-migrate.service") || !slices.Contains(strings.Fields(strings.Join(a["After"], " ")), "homeward-migrate.service") {
		t.Errorf("the agent doesn't require the migration: Requires=%q After=%q", a["Requires"], a["After"])
	}
}

// The users the migration renames are the packages' own, with the same fixed ids.
func TestTheMigrationsUsersAreThePackagesOwn(t *testing.T) {
	testenv.Isolate(t)
	b, err := os.ReadFile("sysusers.d/homeward.conf")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range migrate.Users {
		home := u.Home
		if home == "/" {
			home = "/ /usr/sbin/nologin"
		}
		line := "u " + u.New + " " + strconv.Itoa(u.ID) + ":" + u.New + " "
		if !strings.Contains(string(b), "\n"+line) || !strings.Contains(string(b), "\ng "+u.New+" "+strconv.Itoa(u.ID)+"\n") {
			t.Errorf("sysusers.d has no %q with its group", line)
		}
		if !strings.Contains(string(b), home) {
			t.Errorf("sysusers.d gives %s another home than %s", u.New, u.Home)
		}
	}
}
