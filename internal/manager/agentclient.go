package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/node"
)

// agentClient asks the primary's agent, over its socket.
type agentClient struct{ http *http.Client }

func newAgentClient(socket string) *agentClient {
	return &agentClient{http: &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}},
	}}
}

func (a *agentClient) node(ctx context.Context) (node.Facts, error) {
	var f node.Facts
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://agent/v1/node", nil)
	if err != nil {
		return f, err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return f, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return f, fmt.Errorf("the agent answered %s", resp.Status)
	}
	return f, json.NewDecoder(resp.Body).Decode(&f)
}
