package atomicfile

import "os"

// SetSync replaces fsync for a test, and returns what puts it back.
func SetSync(f func(*os.File) error) func() {
	old := syncFile
	syncFile = f
	return func() { syncFile = old }
}
