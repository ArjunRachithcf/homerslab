package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"time"
)

// Client is the edge's admin API on its Unix socket. The agent is the only process that may open it.
type Client struct {
	Socket string
	http   *http.Client
}

// NewClient is the admin API at socket.
func NewClient(socket string) *Client {
	return &Client{Socket: socket, http: &http.Client{
		Timeout: 15 * time.Second,
		// One connection per request: the agent makes a client at every check, and leaves no idle connection behind.
		Transport: &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}},
	}}
}

// Load replaces the edge's whole configuration with cfg.
func (c *Client) Load(ctx context.Context, cfg []byte) error {
	_, err := c.do(ctx, http.MethodPost, "/load", cfg)
	return err
}

// Config is the configuration the edge runs.
func (c *Client) Config(ctx context.Context) ([]byte, error) {
	return c.do(ctx, http.MethodGet, "/config/", nil)
}

// Running says whether the edge runs cfg: the same JSON, whatever its formatting. A restarted edge runs its boot
// configuration, which differs.
func (c *Client) Running(ctx context.Context, cfg []byte) (bool, error) {
	have, err := c.Config(ctx)
	if err != nil {
		return false, err
	}
	return Same(have, cfg), nil
}

// Same says whether two configurations are the same JSON.
func Same(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://edge"+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the edge's admin API: %w", err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		// Caddy's error says what in the configuration it refused; the configuration holds no secret.
		var e struct{ Error string }
		_ = json.Unmarshal(out, &e)
		return nil, fmt.Errorf("the edge refused %s %s: %d %s", method, path, resp.StatusCode, e.Error)
	}
	return out, nil
}
