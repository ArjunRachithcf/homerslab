// Package apicheck is code written against Compose SDK v5.5.1 (compose-go v2.15) that a later
// minor release no longer compiles. It is built on its own, never linked into the spike.
package apicheck

import (
	"context"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
)

// A service built as a struct literal (compose-go v2.16 moved Image into an embedded ContainerSpec).
func Literal() types.ServiceConfig { return types.ServiceConfig{Name: "web", Image: "busybox"} }

// Compose.Port (renamed to Ports with another result type in v5.6.0, not in its release notes).
func Port(ctx context.Context, svc api.Compose) (string, int, error) {
	return svc.Port(ctx, "hlspike-compose", "whoami", 80, api.PortOptions{})
}

// DownOptions.Images from a string variable (typed api.ImagePruneMode in v5.7.0).
func Images(mode string) api.DownOptions { return api.DownOptions{Images: mode} }
