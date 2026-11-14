//go:build cgo

package agent

// cgoOn: with cgo a read's child can die of runtime/cgo (pthread_create) or the GC under its memory limit before
// it says why; releases are built without cgo.
const cgoOn = true
