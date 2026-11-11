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
	"time"

	"golang.org/x/sys/unix"

	"github.com/ArjunRachithcf/homerslab/internal/dns"
)

// setupSecret is `homerslab setup secret <name>`: first-time setup's secrets never cross the LAN page's plain HTTP
// (#64), so they come from this machine's terminal, over the control socket. The secret is read from stdin without
// echo, sent once, and never printed, logged or kept by the CLI. Before it reads one, it says where the secret will go,
// from the setup's own answers: a stranger who claimed the link first can't send it somewhere else unseen.
func setupSecret(ctx context.Context, args []string, env Env) error {
	fs := flags("setup secret", env)
	socket := fs.String("control-socket", DefaultControlSocket, "the manager's control socket")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: sudo homerslab setup secret mesh|dns\n\nReads the secret from stdin, without showing it, and gives it to first-time setup.\n")
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
	c := newControl(*socket)
	var st struct {
		Answers map[string]string `json:"answers"`
	}
	if name == "dns" {
		if _, err := c.do(ctx, "GET", "/api/v1/setup", 0, &st); err != nil {
			return err
		}
		if st.Answers["domain"] == "" {
			return errors.New(refusal(name, "not-waiting"))
		}
		fmt.Fprintf(env.Stderr, "This token is for %s, and goes to %s.\n", st.Answers["domain"], where(st.Answers))
	}
	value, err := readSecret(ctx, env, prompt)
	if err != nil {
		return err
	}
	if name == "dns" {
		fmt.Fprintf(env.Stderr, "Checking the token, and getting the certificate for *.%s: this can take a few minutes…\n", st.Answers["domain"])
		c = newControlWaiting(*socket, 10*time.Minute)
	}
	body, _ := json.Marshal(map[string]string{"value": value})
	var after struct {
		Answers map[string]string `json:"answers"`
	}
	_, err = c.send(ctx, "PUT", "/api/v1/setup/secrets/"+name, body, &after)
	clear(body)
	if err != nil {
		return refused(name, err)
	}
	msg := secretDone[name]
	if name == "dns" {
		msg = fmt.Sprintf(msg, after.Answers["domain"], adminURL(after.Answers))
	}
	fmt.Fprintln(env.Stdout, msg)
	return nil
}

// dnsToken is `homerslab dns token`: the console's way to replace the DNS token outside first-time setup, for when an
// expired certificate keeps everyone out of Admin (#80). It reads the token as `setup secret` does.
func dnsToken(ctx context.Context, args []string, env Env) error {
	fs := flags("dns token", env)
	socket := fs.String("control-socket", DefaultControlSocket, "the manager's control socket")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: sudo homerslab dns token\n\nReplaces the DNS provider's token, read from stdin without showing it, and renews the certificate if it is due.\n")
		fs.PrintDefaults()
	}
	if err := parse(fs, args); err != nil {
		return err
	}
	var st struct {
		Domain      string `json:"domain"`
		Where       string `json:"where"`
		Certificate *struct {
			NotAfter     time.Time `json:"notAfter"`
			FailingSince time.Time `json:"failingSince"`
			LastError    string    `json:"lastError"`
		} `json:"certificate"`
		RenewAt time.Time `json:"renewAt"`
	}
	if _, err := newControl(*socket).do(ctx, "GET", "/api/v1/dns", 0, &st); err != nil {
		return refused("dns", err)
	}
	fmt.Fprintf(env.Stderr, "This token is for %s, and goes to %s.\n", st.Domain, st.Where)
	if c := st.Certificate; c != nil {
		fmt.Fprintf(env.Stderr, "Its certificate expires on %s, and renews from %s.\n", day(c.NotAfter), day(st.RenewAt))
		if !c.FailingSince.IsZero() {
			fmt.Fprintf(env.Stderr, "Renewing it has failed since %s: %s\n", day(c.FailingSince), c.LastError)
		}
	}
	value, err := readSecret(ctx, env, secretPrompts["dns"])
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"value": value})
	var after struct {
		Renewed     bool `json:"renewed"`
		Certificate struct {
			NotAfter time.Time `json:"notAfter"`
		} `json:"certificate"`
	}
	_, err = newControlWaiting(*socket, 10*time.Minute).send(ctx, "PUT", "/api/v1/dns/token", body, &after)
	clear(body)
	if err != nil {
		return refused("dns", err)
	}
	if after.Renewed {
		fmt.Fprintf(env.Stdout, "Checked and kept. The certificate is renewed: it expires on %s.\n", day(after.Certificate.NotAfter))
	} else {
		fmt.Fprintf(env.Stdout, "Checked and kept. The certificate is valid until %s, and renews by itself.\n", day(after.Certificate.NotAfter))
	}
	return nil
}

func day(t time.Time) string { return t.UTC().Format("2 January 2006") }

// where says where the DNS token goes, from setup's answers.
func where(ans map[string]string) string {
	return dns.Settings{Provider: ans["dnsProvider"], Server: ans["dnsServer"], KeyName: ans["dnsKeyName"]}.Where()
}

// adminURL is Admin's one URL (ADR 0021).
func adminURL(ans map[string]string) string {
	u := "https://admin." + ans["domain"]
	if p := ans["householdPort"]; p != "" && p != "443" {
		u += ":" + p
	}
	return u
}

// refused turns the manager's no into what to do.
func refused(name string, err error) error {
	var ae *apiError
	if errors.As(err, &ae) {
		if msg := refusal(name, ae.Code); msg != "" {
			return errors.New(msg)
		}
	}
	return err
}

var secretPrompts = map[string]string{
	"mesh": "Your mesh's token (it isn't shown as you paste it): ",
	"dns":  "Your DNS provider's token (it isn't shown as you paste it): ",
}

var secretDone = map[string]string{
	"mesh": "Checked. HomersLab made its own token with it, and this machine is on the mesh. Continue on the setup page.",
	"dns":  "Checked. The certificate for *.%s is issued, and Admin answers at %s over the mesh. Continue on the setup page.",
}

// refusal is what to do about a refused secret, by the secret and the manager's code.
func refusal(name, code string) string {
	if msg, ok := secretRefusals[name+":"+code]; ok {
		return msg
	}
	return secretRefusals[code]
}

var secretRefusals = map[string]string{
	"not-waiting":          "no step of first-time setup is waiting for this: choose your mesh on the setup page first",
	"dns:not-waiting":      "no step of first-time setup is waiting for this: give your domain on the setup page first",
	"secret-refused":       "the mesh refused this token: check it, then run the command again",
	"dns:secret-refused":   "the DNS provider refused this token: check that it may edit the domain's records, then run the command again",
	"mesh-unreachable":     "the mesh's control plane didn't answer: check the address on the setup page and this machine's internet",
	"join-failed":          "the token works, but this machine didn't join the mesh: see journalctl -u homerslab-agent",
	"no-value":             "nothing was read: paste the secret, then press Enter",
	"dns:no-zone":          "no public DNS zone holds the domain: check its name on the setup page",
	"dns:caa-forbids":      "the domain's CAA records don't let the certificate authority issue for it: add a CAA record for letsencrypt.org at your DNS provider, then run the command again",
	"dns:issuance-failed":  "the token works, but the certificate wasn't issued: see journalctl -u homerslab-manager, then run the command again",
	"dns:edge-failed":      "the certificate is issued, but the edge didn't take it: see journalctl -u homerslab-agent -u homerslab-edge",
	"dns:names-failed":     "the certificate is issued, but the mesh's names weren't made: see journalctl -u homerslab-manager",
	"dns:no-domain":        "first-time setup hasn't got to the domain yet: give it on the setup page, then use sudo homerslab setup secret dns",
	"dns:mesh-unreachable": "the mesh's control plane didn't answer, so the names weren't made: check this machine's internet",
}

// readSecret reads one line from stdin. On a terminal it asks with prompt, on stderr, and turns echo off meanwhile;
// Ctrl-C (ctx) gives up, with the terminal as it was.
func readSecret(ctx context.Context, env Env, prompt string) (string, error) {
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
	type read struct {
		line string
		err  error
	}
	got := make(chan read, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(in, 16<<10)).ReadString('\n')
		got <- read{line, err}
	}()
	var r read
	select {
	case <-ctx.Done():
		return "", errors.New("stopped: nothing was sent")
	case r = <-got:
	}
	if r.err != nil && !errors.Is(r.err, io.EOF) {
		return "", r.err
	}
	line := strings.TrimSpace(r.line)
	if line == "" {
		return "", errors.New(secretRefusals["no-value"])
	}
	return line, nil
}

// send is do with a JSON body, once.
func (c *control) send(ctx context.Context, method, path string, body []byte, v any) ([]byte, error) {
	return c.request(ctx, method, path, 0, bytes.NewReader(body), v)
}
