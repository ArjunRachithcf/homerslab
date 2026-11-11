package netbird

import (
	"context"

	"github.com/ArjunRachithcf/homerslab/internal/mesh"
)

// BootstrapSeeing is Bootstrap, handing the test the owner's setup token so it can check the token is revoked.
func BootstrapSeeing(ctx context.Context, rawURL string, o Owner, setupToken *mesh.Secret) (Bootstrapped, error) {
	return bootstrap(ctx, rawURL, o, func(s mesh.Secret) { *setupToken = s })
}
