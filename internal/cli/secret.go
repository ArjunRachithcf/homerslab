package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// setupSecret is `homerslab setup secret <name>`: first-time setup's secrets never cross the LAN page's plain HTTP
// (#64), so they come from this machine's terminal, over the control socket. The secret is read from stdin without
// echo, sent once, and never printed, logged or kept by the CLI.
func setupSecret(ctx context.Context, args []string, env Env) error {
	fs := flags("setup secret", env)
	socket := fs.String("control-socket", DefaultControlSocket, "the manager's control socket")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: sudo homerslab setup secret mesh\n\nReads the secret from stdin, without showing it, and gives it to first-time setup.\n")
		fs.PrintDefaults()
	}
	var name string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		name, args = args[0], args[1:]
	}
	if err := parse(fs, args); err != nil {
		return err
	}
	prompt, ok := secretPrompts[name]
	if !ok {
		fs.Usage()
		return errUsage
	}
	value, err := readSecret(env, prompt)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"value": value})
	_, err = newControl(*socket).send(ctx, "PUT", "/api/v1/setup/secrets/"+name, body, nil)
	clear(body)
	var ae *apiError
	if errors.As(err, &ae) {
		if msg, ok := secretRefusals[ae.Code]; ok {
			return errors.New(msg)
		}
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(env.Stdout, secretDone[name])
	return nil
}

var secretPrompts = map[string]string{"mesh": "Your mesh's token (it isn't shown as you paste it): "}

var secretDone = map[string]string{
	"mesh": "Checked. HomersLab made its own token with it, and this machine is on the mesh. Continue on the setup page.",
}

var secretRefusals = map[string]string{
	"not-waiting":      "no step of first-time setup is waiting for this: choose your mesh on the setup page first",
	"secret-refused":   "the mesh refused this token: check it, then run the command again",
	"mesh-unreachable": "the mesh's control plane didn't answer: check the address on the setup page and this machine's internet",
	"join-failed":      "the token works, but this machine didn't join the mesh: see journalctl -u homerslab-agent",
	"no-value":         "nothing was read: paste the secret, then press Enter",
}

// readSecret reads one line from stdin. On a terminal it asks with prompt, on stderr, and turns echo off meanwhile.
func readSecret(env Env, prompt string) (string, error) {
	in := env.Stdin
	if in == nil {
		in = os.Stdin
	}
	if f, ok := in.(*os.File); ok {
		if t, err := unix.IoctlGetTermios(int(f.Fd()), unix.TCGETS); err == nil {
			fmt.Fprint(env.Stderr, prompt)
			quiet := *t
			quiet.Lflag &^= unix.ECHO
			if err := unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, &quiet); err != nil {
				return "", err
			}
			defer func() {
				_ = unix.IoctlSetTermios(int(f.Fd()), unix.TCSETS, t)
				fmt.Fprintln(env.Stderr)
			}()
		}
	}
	line, err := bufio.NewReader(io.LimitReader(in, 16<<10)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New(secretRefusals["no-value"])
	}
	return line, nil
}

// send is do with a JSON body, once.
func (c *control) send(ctx context.Context, method, path string, body []byte, v any) ([]byte, error) {
	return c.request(ctx, method, path, 0, bytes.NewReader(body), v)
}
