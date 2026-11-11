package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"syscall"
	"time"
)

// control is the manager's API on its control socket.
type control struct {
	socket string
	http   *http.Client
}

func newControl(socket string) *control {
	return &control{socket: socket, http: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}},
	}}
}

// apiError is the manager's answer when it says no.
type apiError struct {
	Status int
	Code   string `json:"error"`
}

func (e *apiError) Error() string { return fmt.Sprintf("the manager answered %d %s", e.Status, e.Code) }

// do sends a request, with body as JSON unless it is nil, and decodes the JSON answer into v; with wait, it waits
// that long for the manager to start.
func (c *control) do(ctx context.Context, method, path string, body any, wait time.Duration, v any) ([]byte, error) {
	deadline := time.Now().Add(wait)
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	for {
		req, err := http.NewRequestWithContext(ctx, method, "http://manager"+path, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := c.http.Do(req)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) {
				return nil, fmt.Errorf("the manager's control socket %s is for root and the homerslab user: run it with sudo", c.socket)
			}
			if time.Now().Before(deadline) && ctx.Err() == nil {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ECONNREFUSED) {
				return nil, fmt.Errorf("the manager isn't running (no answer on %s): systemctl status homerslab-manager", c.socket)
			}
			return nil, fmt.Errorf("asking the manager: %w", err)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			e := &apiError{Status: resp.StatusCode}
			_ = json.Unmarshal(body, e)
			return body, e
		}
		if v != nil {
			if err := json.Unmarshal(body, v); err != nil {
				return body, fmt.Errorf("the manager's answer: %w", err)
			}
		}
		return body, nil
	}
}
