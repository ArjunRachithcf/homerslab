// Package plantmod is a planted, throwaway import: x/crypto in the module graph, openpgp neither imported nor called.
package plantmod

import "golang.org/x/crypto/blake2b"

func Sum(b []byte) [32]byte { return blake2b.Sum256(b) }
