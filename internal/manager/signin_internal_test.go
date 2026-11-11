package manager

import (
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// The first admin's username is their mail address's local part, never one of the provider's or HomersLab's own
// accounts (review of #17).
func TestTheFirstAdminsUsernameIsNeverAReservedOne(t *testing.T) {
	testenv.Isolate(t)
	for mail, want := range map[string]string{
		"ines@ferreira.family": "ines", "Ines.Ferreira+home@x.org": "ines.ferreira-home", "homerslab@x.org": "homerslab-admin",
		"akadmin@x.org": "akadmin-admin", "admin@x.org": "admin-admin", "ak-outpost@x.org": "ak-outpost-admin", "@x.org": "first-admin",
	} {
		if got := adminUsername(mail); got != want {
			t.Errorf("%s: %q, want %q", mail, got, want)
		}
	}
}

// A peer flooding the sign-in never evicts a sign-in under way: new ones are refused instead (review of #17).
func TestSignInAttemptsAreNeverEvictedByAFlood(t *testing.T) {
	testenv.Isolate(t)
	var as attempts
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	if !as.put("admin", attempt{expires: now.Add(attemptLife)}, now) {
		t.Fatal("the admin's attempt was refused")
	}
	refused := 0
	for i := range 2 * maxAttempts {
		if !as.put(string(rune('a'+i%26))+time.Duration(i).String(), attempt{expires: now.Add(attemptLife)}, now) {
			refused++
		}
	}
	if _, ok := as.take("admin", now); !ok || refused == 0 {
		t.Errorf("the admin's attempt survived: %v; refused %d", ok, refused)
	}
	if !as.put("later", attempt{expires: now.Add(2 * attemptLife)}, now.Add(attemptLife+time.Second)) {
		t.Error("expired attempts weren't cleared for a new one")
	}
}
