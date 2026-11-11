package node_test

import (
	"errors"
	"testing"
	"testing/fstest"

	"github.com/ArjunRachithcf/homerslab/internal/node"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

type failingStatfs struct{ fstest.MapFS }

func (failingStatfs) Statfs(string) (node.Space, error) {
	return node.Space{}, errors.New("statfs: input/output error")
}

// A filesystem that can't say how much space it has leaves the space unknown; the other facts still stand.
func TestFactsStandWhenTheSpaceIsUnknown(t *testing.T) {
	testenv.Isolate(t)
	f, err := node.Read(failingStatfs{fstest.MapFS{
		"etc/os-release": {Data: []byte("ID=fedora\n")},
		"proc/meminfo":   {Data: []byte("MemTotal: 1024 kB\n")},
	}})
	if err != nil || f.Space != nil || f.MemoryBytes != 1024*1024 {
		t.Errorf("facts %+v, %v; want them without space", f, err)
	}
}
