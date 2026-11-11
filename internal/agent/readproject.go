package agent

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"strings"
	"syscall"

	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/engine/compose"
)

// nobody is the user a compose read runs as when the agent is root.
const nobody = 65534

// The child's limits: 384 MiB of data (RLIMIT_DATA, which counts the memory written to, not the address space Go
// reserves at rest, so it holds for builds with and without cgo; a read of the official catalog's largest file needs
// under 128 MiB without cgo and under 256 MiB with it), no core dump, a few files, and nothing written beyond its
// compose file.
const (
	childData     = 384 << 20
	childFiles    = 32
	childFileSize = 1 << 20
)

// readResult is what a read's child answers on its standard output.
type readResult struct {
	Project *engine.Project `json:"project,omitempty"`
	Error   string          `json:"error,omitempty"` // not-ours, not-plain, invalid-compose, or cpu-limit
	Detail  string          `json:"detail,omitempty"`
	// Confined is how the read ran, as the child read it back from the kernel: its user and groups, its limits and
	// its OOM score, for the tests.
	Confined Confined `json:"confined"`
}

// Confined is how a read's child ran.
type Confined struct {
	UID, GID    int
	Groups      string // /proc/self/status's Groups line
	Limits      map[string]uint64
	OOMScoreAdj string
}

// ReadProjectMain is the agent's read-project mode (homerslab agent read-project [--cpu-seconds N] -- PROJECT): the
// child the agent starts for each compose read, with the file on its standard input and the result, as JSON, on its
// standard output.
//
// What the child can do: read its standard input, and write in its own empty folder (its working directory, HOME
// and TMPDIR), where the reader puts the compose file it loads. What it can't:
//   - act as root: when the agent is root, the child starts as nobody (65534) with no supplementary groups, set in
//     the fork before it runs any of its own code, so it holds no capability and reads only what everyone may read;
//     it never starts another program;
//   - use more than its limits: 384 MiB of data (not under the race detector, whose shadow memory and thread stacks
//     need more), --cpu-seconds of CPU (soft; hard a second later), 32 open files, files of at most 1 MiB, no core
//     dump; and it is the first process
//     the kernel kills when memory runs short (oom_score_adj 1000), with the agent's unit set to carry on (OOMPolicy);
//   - outlive the agent's deadline: the agent kills it then, and when the agent dies it dies too (Pdeathsig, which
//     the fork sets after the IDs, so changing them doesn't clear it);
//   - reach a container engine: the reader points at none.
//
// It doesn't apply Landlock or seccomp: in Go both bind only the thread that asks, not the process.
func ReadProjectMain(args []string, stdin io.Reader, stdout io.Writer) int {
	return readProjectMain(args, stdin, stdout, func(ctx context.Context, name string, file []byte) (engine.Project, error) {
		r, err := compose.NewReader(compose.NoEngine)
		if err != nil {
			return engine.Project{}, err
		}
		return r.Read(ctx, name, file)
	})
}

func readProjectMain(args []string, stdin io.Reader, stdout io.Writer, read func(ctx context.Context, name string, file []byte) (engine.Project, error)) int {
	fs := flag.NewFlagSet("homerslab agent read-project", flag.ContinueOnError)
	cpu := fs.Int("cpu-seconds", 10, "the CPU time the read may use")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: homerslab agent read-project [--cpu-seconds N] -- PROJECT < compose.yaml")
		return 2
	}
	var res readResult
	// Go ignores SIGXCPU unless asked: asked, the child says it reached its soft CPU limit and ends, before the hard
	// limit kills it without a word.
	xcpu := make(chan os.Signal, 1)
	signal.Notify(xcpu, syscall.SIGXCPU)
	// The signal's goroutine and the main answer both write to stdout: one JSON object, whichever comes first.
	var once sync.Once
	answer := func(r readResult) (err error) {
		once.Do(func() { err = json.NewEncoder(stdout).Encode(r) })
		return err
	}
	go func() {
		<-xcpu
		_ = answer(readResult{Error: "cpu-limit"})
		os.Exit(1)
	}()
	var err error
	if res.Confined, err = confine(*cpu); err != nil {
		fmt.Fprintln(os.Stderr, "confining the read:", err)
		return 1
	}
	file, err := io.ReadAll(io.LimitReader(stdin, 64<<10+1))
	if err == nil {
		var p engine.Project
		if p, err = read(context.Background(), fs.Arg(0), file); err == nil {
			res.Project = &p
		}
	}
	switch {
	case err == nil:
	case errors.Is(err, compose.ErrNotOurs):
		res.Error = "not-ours"
	case errors.Is(err, compose.ErrNotPlain):
		res.Error, res.Detail = "not-plain", err.Error()
	default:
		res.Error, res.Detail = "invalid-compose", err.Error()
	}
	if err := answer(res); err != nil {
		return 1
	}
	return 0
}

// confine sets the child's limits and OOM score, and reads back how it runs.
func confine(cpuSeconds int) (Confined, error) {
	if err := os.WriteFile("/proc/self/oom_score_adj", []byte("1000"), 0); err != nil {
		return Confined{}, fmt.Errorf("oom_score_adj: %w", err)
	}
	set := map[string]struct {
		resource int
		value    uint64
	}{
		"core":     {syscall.RLIMIT_CORE, 0},
		"files":    {syscall.RLIMIT_NOFILE, childFiles},
		"fileSize": {syscall.RLIMIT_FSIZE, childFileSize},
	}
	if !raceDetector {
		set["data"] = struct {
			resource int
			value    uint64
		}{syscall.RLIMIT_DATA, childData}
	}
	c := Confined{UID: os.Getuid(), GID: os.Getgid(), Limits: map[string]uint64{}}
	// The CPU limit is soft at cpuSeconds and hard a second later: the kernel tells the child (SIGXCPU, which Go
	// ignores unless notified, as readProjectMain does) before it kills it.
	cpu := uint64(cpuSeconds)
	if err := syscall.Setrlimit(syscall.RLIMIT_CPU, &syscall.Rlimit{Cur: cpu, Max: cpu + 1}); err != nil {
		return c, fmt.Errorf("cpu: %w", err)
	}
	var got syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_CPU, &got); err != nil {
		return c, err
	}
	c.Limits["cpu"], c.Limits["cpuHard"] = got.Cur, got.Max
	for name, l := range set {
		if err := syscall.Setrlimit(l.resource, &syscall.Rlimit{Cur: l.value, Max: l.value}); err != nil {
			return c, fmt.Errorf("%s: %w", name, err)
		}
		var got syscall.Rlimit
		if err := syscall.Getrlimit(l.resource, &got); err != nil {
			return c, err
		}
		c.Limits[name] = got.Max
	}
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for line := range strings.Lines(string(b)) {
			if v, ok := strings.CutPrefix(line, "Groups:"); ok {
				c.Groups = strings.TrimSpace(v)
			}
		}
	}
	if b, err := os.ReadFile("/proc/self/oom_score_adj"); err == nil {
		c.OOMScoreAdj = strings.TrimSpace(string(b))
	}
	return c, nil
}
