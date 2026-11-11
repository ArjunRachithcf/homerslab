// Package catalogs carries the catalogs HomersLab ships into its binary: the official catalog, in official/.
package catalogs

import (
	"embed"
	"io/fs"
)

//go:embed all:official
var files embed.FS

// Official is the official catalog's folder.
func Official() fs.FS {
	sub, err := fs.Sub(files, "official")
	if err != nil {
		panic(err) // the folder is embedded at build time
	}
	return sub
}
