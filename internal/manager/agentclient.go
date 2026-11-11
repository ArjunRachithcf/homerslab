package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/engine"
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

// agentRefused is the agent's no, with its reason.
type agentRefused struct {
	Status       int
	Code, Detail string
}

func (e *agentRefused) Error() string {
	return fmt.Sprintf("the agent answered %d %s: %s", e.Status, e.Code, e.Detail)
}

// readProject asks the agent to read an app's compose file as the project name, through the Compose SDK.
func (a *agentClient) readProject(ctx context.Context, name string, compose []byte) (engine.Project, error) {
	var p engine.Project
	body, err := json.Marshal(map[string]string{"project": name, "compose": string(compose)})
	if err != nil {
		return p, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://agent/v1/projects/read", bytes.NewReader(body))
	if err != nil {
		return p, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return p, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		e := &agentRefused{Status: resp.StatusCode}
		var b struct{ Error, Detail string }
		if json.NewDecoder(resp.Body).Decode(&b) != nil || resp.StatusCode/100 != 4 {
			return p, fmt.Errorf("the agent answered %s", resp.Status)
		}
		e.Code, e.Detail = b.Error, b.Detail
		return p, e
	}
	return p, json.NewDecoder(resp.Body).Decode(&p)
}
