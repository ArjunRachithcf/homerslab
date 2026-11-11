// Package surfaces carries the surfaces' built pages into the binary. The web app in web/ builds them into dist/
// (`npm run build`).
package surfaces

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist is the built web app; it holds only .gitkeep until `npm run build` has run.
func Dist() fs.FS {
	sub, _ := fs.Sub(dist, "dist")
	return sub
}
