package netbird

import (
	"context"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// BootstrapSeeing is Bootstrap, handing the test the owner's setup token so it can check the token is revoked.
func BootstrapSeeing(ctx context.Context, rawURL string, o Owner, keep func(mesh.Token) error, setupToken *mesh.Secret) (mesh.Token, error) {
	return bootstrap(ctx, rawURL, o, keep, func(s mesh.Secret) { *setupToken = s })
}
