package agent

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"syscall"

	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/engine/compose"
)

// nobody is the user a compose read runs as when the agent is root.
const nobody = 65534

// The child's limits: memory far above what a read of a plain file needs (tens of MB), no core dump, a few files,
// and nothing written beyond its compose file.
const (
	childAddressSpace = 2 << 30
	childFiles        = 32
	childFileSize     = 1 << 20
)

// readResult is what a read's child answers on its standard output.
type readResult struct {
	Project *engine.Project `json:"project,omitempty"`
	Error   string          `json:"error,omitempty"` // not-ours, not-plain or invalid-compose
	Detail  string          `json:"detail,omitempty"`
	// UID is the user the read ran as, and Limits the limits it ran under, for the tests.
	UID    int            `json:"uid"`
	Limits map[string]int `json:"limits"`
}

// ReadProjectMain is the agent's read-project mode (homerslab agent read-project [--cpu-seconds N] PROJECT): the
// child the agent starts for each compose read, with the file on its standard input and the result, as JSON, on its
// standard output.
//
// What the child can do: read its standard input, and write in its own empty folder (its working directory, HOME
// and TMPDIR), where the reader puts the compose file it loads. What it can't:
//   - act as root: when started as root it becomes nobody (65534) first, with no supplementary groups, so it holds no
//     capability and reads only what everyone may read; it never starts another program;
//   - use more than its limits: 2 GiB of address space (not under the race detector, whose shadow memory needs
//     more), --cpu-seconds of CPU, 32 open files, files of at most 1 MiB, no core dump;
//   - outlive the agent's deadline: the agent kills it then, and when the agent dies it dies too (Pdeathsig);
//   - reach a container engine: the reader points at none.
//
// It doesn't apply Landlock or seccomp: in Go both bind only the thread that asks, not the process.
func ReadProjectMain(args []string, stdin io.Reader, stdout io.Writer) int {
	fs := flag.NewFlagSet("homerslab agent read-project", flag.ContinueOnError)
	cpu := fs.Int("cpu-seconds", 10, "the CPU time the read may use")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: homerslab agent read-project [--cpu-seconds N] PROJECT < compose.yaml")
		return 2
	}
	res := readResult{Limits: map[string]int{}}
	if err := confine(*cpu, res.Limits); err != nil {
		fmt.Fprintln(os.Stderr, "confining the read:", err)
		return 1
	}
	res.UID = os.Getuid()
	file, err := io.ReadAll(io.LimitReader(stdin, 64<<10+1))
	if err == nil {
		var r *compose.Reader
		if r, err = compose.NewReader(compose.NoEngine); err == nil {
			var p engine.Project
			if p, err = r.Read(context.Background(), fs.Arg(0), file); err == nil {
				res.Project = &p
			}
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
	if err := json.NewEncoder(stdout).Encode(res); err != nil {
		return 1
	}
	return 0
}

// confine drops root, if the child has it, and sets its limits.
func confine(cpuSeconds int, limits map[string]int) error {
	if os.Geteuid() == 0 {
		// Go applies these to every thread of the process.
		if err := syscall.Setgroups([]int{}); err != nil {
			return err
		}
		if err := syscall.Setgid(nobody); err != nil {
			return err
		}
		if err := syscall.Setuid(nobody); err != nil {
			return err
		}
	}
	set := map[string]struct {
		resource int
		value    uint64
	}{
		"core":     {syscall.RLIMIT_CORE, 0},
		"cpu":      {syscall.RLIMIT_CPU, uint64(cpuSeconds)},
		"files":    {syscall.RLIMIT_NOFILE, childFiles},
		"fileSize": {syscall.RLIMIT_FSIZE, childFileSize},
	}
	if !raceDetector {
		set["addressSpace"] = struct {
			resource int
			value    uint64
		}{syscall.RLIMIT_AS, childAddressSpace}
	}
	for name, l := range set {
		if err := syscall.Setrlimit(l.resource, &syscall.Rlimit{Cur: l.value, Max: l.value}); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		limits[name] = int(l.value)
	}
	return nil
}
