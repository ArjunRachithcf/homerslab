package authentik

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/idp"
)

// api is Authentik's REST API, as one token, reached at its upstream with the sign-in's own Host: the brand, the
// issuer and the passkeys' origin all follow the Host, and HTTPS is what the edge serves it as.
type api struct {
	base  *url.URL
	host  string
	token idp.Secret
	http  *http.Client
}

func newAPI(base string, h idp.Household, token idp.Secret) (*api, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("authentik: not an upstream address: %q", base)
	}
	return &api{base: u, host: strings.TrimPrefix(h.Origin(), "https://"), token: token,
		http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}}}, nil
}

// APIError is Authentik saying no: its status and what it said, which never holds a secret HomersLab sent.
type APIError struct {
	Method, Path string
	Status       int
	Detail       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("authentik answered %s %s with %d: %s", e.Method, e.Path, e.Status, e.Detail)
}

// call sends body as JSON and decodes the answer into out. Paths are under /api/v3.
func (a *api) call(ctx context.Context, method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	u := a.base.JoinPath("/api/v3")
	p, q, _ := strings.Cut(path, "?")
	u = u.JoinPath(p)
	u.RawQuery = q
	if strings.HasSuffix(p, "/") && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), r)
	if err != nil {
		return err
	}
	req.Host = a.host
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token.Reveal())
	}
	resp, err := a.http.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) || errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "connect") {
			return fmt.Errorf("%w: %v", idp.ErrUnreachable, err)
		}
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %s %s", idp.ErrTokenRefused, method, p)
	case resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusBadGateway:
		return fmt.Errorf("%w: %s %s answered %d", idp.ErrNotReady, method, p, resp.StatusCode)
	case resp.StatusCode/100 != 2:
		detail := string(b)
		if len(detail) > 300 {
			detail = detail[:300]
		}
		return &APIError{Method: method, Path: p, Status: resp.StatusCode, Detail: detail}
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

// list reads every page of a list endpoint into out, a pointer to a slice.
func list[T any](ctx context.Context, a *api, path string) ([]T, error) {
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	var all []T
	for page := 1; ; page++ {
		var r struct {
			Pagination struct {
				Next float64 `json:"next"`
			} `json:"pagination"`
			Results []T `json:"results"`
		}
		if err := a.call(ctx, http.MethodGet, fmt.Sprintf("%s%spage_size=100&page=%d", path, sep, page), nil, &r); err != nil {
			return nil, err
		}
		all = append(all, r.Results...)
		if r.Pagination.Next == 0 {
			return all, nil
		}
	}
}

// one is the only object of a filtered list, or nil when there is none.
func one[T any](ctx context.Context, a *api, path string) (*T, error) {
	l, err := list[T](ctx, a, path)
	if err != nil || len(l) == 0 {
		return nil, err
	}
	if len(l) > 1 {
		return nil, fmt.Errorf("authentik: %d objects where one was expected at %s", len(l), path)
	}
	return &l[0], nil
}

// q escapes a query value.
func q(s string) string { return url.QueryEscape(s) }
