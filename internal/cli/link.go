package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/cli/qrterm"
)

func setupLink(ctx context.Context, args []string, env Env) error {
	fs := flags("setup link", env)
	socket := fs.String("control-socket", DefaultControlSocket, "the manager's control socket")
	wait := fs.Duration("wait", 30*time.Second, "how long to wait for the manager to start")
	asJSON := fs.Bool("json", false, "print the API's resource as it is, for scripts")
	if err := parse(fs, args); err != nil {
		return err
	}
	var l struct {
		URL       string    `json:"url"`
		LocalURL  string    `json:"localUrl"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	body, err := newControl(*socket).do(ctx, "POST", "/api/v1/setup/links", *wait, &l)
	var ae *apiError
	if errors.As(err, &ae) && ae.Code == "lan-half-done" {
		return errors.New("first-time setup's first half is done: it continues over the mesh, so there is no link to make")
	}
	if err != nil {
		return err
	}
	if *asJSON {
		_, err := env.Stdout.Write(body)
		return err
	}
	qr, err := qrterm.Render(l.URL, isTerminal(env.Stdout))
	if err != nil {
		return err
	}
	until := l.ExpiresAt.Local().Format("15:04")
	fmt.Fprintf(env.Stdout, `First-time setup is ready. Open this link once, within 30 minutes (until %s),
on this machine or on another device at home:

  %s

%s
Scan the code with a phone's camera to open it there. On this machine itself, this works too:
  %s

Each new link ends the earlier ones. To make another: sudo homerslab setup link
`, until, l.URL, qr, l.LocalURL)
	return nil
}

func isTerminal(w any) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
