package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/cli/qrterm"
)

// handOver is the manager's answer to setup join and setup passkey: credentials for this terminal only.
type handOver struct {
	DeviceKey          string    `json:"deviceKey"`
	DeviceKeyExpiresAt time.Time `json:"deviceKeyExpiresAt"`
	Enrolment          string    `json:"enrolment"`
	EnrolmentExpiresAt time.Time `json:"enrolmentExpiresAt"`
	SignIn             string    `json:"signIn"`
	Admin              string    `json:"admin"`
	Household          string    `json:"household"`
	Username           string    `json:"username"`
}

// setupJoin is `sudo homerslab setup join`: first-time setup's second half begins in this machine's terminal. It shows
// a one-off key for the admin's device and the one-time link to their first passkey, as QR codes. They are
// credentials, so they are never shown on the LAN page, which travels over plain HTTP (#64).
func setupJoin(ctx context.Context, args []string, env Env) error {
	return handOverCmd(ctx, "setup join", "/api/v1/setup/join", args, env)
}

// setupPasskey is `sudo homerslab setup passkey`: another one-time link, for the admin's second passkey.
func setupPasskey(ctx context.Context, args []string, env Env) error {
	return handOverCmd(ctx, "setup passkey", "/api/v1/setup/passkey", args, env)
}

func handOverCmd(ctx context.Context, name, path string, args []string, env Env) error {
	fs := flags(name, env)
	socket := fs.String("control-socket", DefaultControlSocket, "the manager's control socket")
	wait := fs.Duration("wait", 20*time.Minute, "how long to wait: the household account's first start pulls its images")
	if err := parse(fs, args); err != nil {
		return err
	}
	if path == "/api/v1/setup/join" {
		fmt.Fprintln(env.Stderr, "Setting up the household account. The first time takes a few minutes…")
	}
	var h handOver
	_, err := newControlWaiting(*socket, *wait).do(ctx, "POST", path, 5*time.Second, &h)
	var ae *apiError
	if errors.As(err, &ae) {
		switch ae.Code {
		case "lan-half-open":
			return errors.New("first-time setup's first half isn't done yet: finish it on the setup page first")
		case "setup-done":
			return errors.New("first-time setup is done; to add a passkey for an admin who lost theirs: sudo homerslab admin recover <person>")
		case "no-first-passkey":
			return errors.New("the first passkey comes first: sudo homerslab setup join")
		case "sign-in-failed", "mesh-unreachable":
			return fmt.Errorf("the household account couldn't be set up (%s): sudo journalctl -u homerslab-manager -u homerslab-authentik", ae.Code)
		}
	}
	if err != nil {
		return err
	}
	colour := isTerminal(env.Stdout)
	if h.DeviceKey != "" {
		qr, err := qrterm.Render(h.DeviceKey, colour)
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, `1. Put your device on the mesh. In the NetBird app, add this setup key (works once, until %s):

  %s

%s
`, h.DeviceKeyExpiresAt.Local().Format("2 Jan 15:04"), h.DeviceKey, qr)
	}
	qr, err := qrterm.Render(h.Enrolment, colour)
	if err != nil {
		return err
	}
	step := "2. Then create your passkey"
	if h.DeviceKey == "" {
		step = "Create another passkey, with your other device or a security key,"
	}
	fmt.Fprintf(env.Stdout, `%s at %s, from this link (works once, until %s):

  %s

%s
It is only for %s, the first admin of %s. Afterwards, sign in to Admin: %s
`, step, h.SignIn, h.EnrolmentExpiresAt.Local().Format("15:04"), h.Enrolment, qr, h.Username, h.Household, h.Admin)
	return nil
}
