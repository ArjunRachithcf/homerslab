package agent

import (
	"context"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/engine"
)

type stuck struct{ release <-chan struct{} }

// Read ignores its context, as a parse stuck in the SDK would, until it is released.
func (s stuck) Read(context.Context, string, []byte) (engine.Project, error) {
	<-s.release
	return engine.Project{Name: "homerslab-web", Services: []engine.Service{}}, nil
}

// StuckReads makes every compose read hang until release is closed, with reads given d.
func StuckReads(t *testing.T, d time.Duration, release <-chan struct{}) {
	oldDeadline, oldReader := readDeadline, newProjectReader
	readDeadline = d
	newProjectReader = func() (projectReader, error) { return stuck{release}, nil }
	t.Cleanup(func() { readDeadline, newProjectReader = oldDeadline, oldReader })
}
