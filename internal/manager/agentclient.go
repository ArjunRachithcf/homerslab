package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/agent"
	"github.com/ArjunRachithcf/homerslab/internal/mesh"
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

// errJoin is the agent's join failing.
var errJoin = errors.New("the primary didn't join the mesh")

// call sends body as JSON to the agent and decodes its answer into out. The agent's error codes come back in the
// error; no request body ever does.
func (a *agentClient) call(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, r)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		var e struct{ Error string }
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e)
		return fmt.Errorf("the agent answered %s %s with %s %s", method, path, resp.Status, e.Error)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *agentClient) seal(ctx context.Context, name string, value mesh.Secret) error {
	return a.call(ctx, http.MethodPut, "/v1/credentials/"+name, map[string]string{"value": value.Reveal()}, nil)
}

func (a *agentClient) unseal(ctx context.Context, name string) (mesh.Secret, error) {
	var v struct{ Value string }
	err := a.call(ctx, http.MethodGet, "/v1/credentials/"+name, nil, &v)
	return mesh.Secret(v.Value), err
}

func (a *agentClient) join(ctx context.Context, req agent.JoinRequest) (agent.Joined, error) {
	var j agent.Joined
	// A join waits for the mesh's client to connect: longer than the client's usual timeout.
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	c := *a.http
	c.Timeout = 90 * time.Second
	err := (&agentClient{http: &c}).call(ctx, http.MethodPost, "/v1/mesh/join", req, &j)
	if err != nil {
		return j, fmt.Errorf("%w: %v", errJoin, err)
	}
	return j, nil
}

func (a *agentClient) closeSetupListener(ctx context.Context) error {
	return a.call(ctx, http.MethodPut, "/v1/firewall/setup-listener", map[string]bool{"open": false}, nil)
}

func (a *agentClient) forget(ctx context.Context, name string) error {
	return a.call(ctx, http.MethodDelete, "/v1/credentials/"+name, nil, nil)
}

// meshNode is the node's mesh client as its agent finds it.
func (a *agentClient) meshNode(ctx context.Context) (agent.MeshStatus, error) {
	var s agent.MeshStatus
	return s, a.call(ctx, http.MethodGet, "/v1/mesh", nil, &s)
}
