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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/engine/compose"
)

// readUser is the user a compose read runs as when the agent is root: one that only the agent's reads use, which the
// packages make (packaging/sysusers.d), so no other process shares its uid to signal or trace a read's child. Never
// nobody, whom many share (#183).
const readUser = "homerslab-read"

// ReadID is readUser's uid and gid, fixed by the packages (packaging/sysusers.d) outside the ranges app images commonly
// run as (#190). The agent refuses any other: a uid sysusers handed out before, 999 say, could be an app's.
const ReadID = 60602

// passwdFile and groupFile are where the agent finds readUser and its group. Tests replace them.
var passwdFile, groupFile = "/etc/passwd", "/etc/group"

// errNoReadUser says readUser is missing or unfit: a root agent then refuses to read, and never falls back to another
// user.
var errNoReadUser = errors.New("no user of the agent's reads (" + readUser + ")")

// readIDs is readUser's uid and gid.
func readIDs() (uid, gid uint32, err error) { return readIDsFrom(passwdFile, groupFile) }

// readIDsFrom is readUser's uid and gid from passwd and group files, read directly rather than through NSS, where the
// packages' sysusers.d writes them. The user must be the reads' own: it refuses a missing or broken entry, root's uid
// or group, ids other than the packages' fixed ReadID, a uid another entry shares (nobody's, say), and a group that
// isn't readUser's own, with its gid, no members and no other user's as its primary group (nogroup, or docker's, which
// would open the engine).
func readIDsFrom(passwd, group string) (uid, gid uint32, err error) {
	users, err := idEntries(passwd)
	if err != nil {
		return 0, 0, err
	}
	groups, err := idEntries(group)
	if err != nil {
		return 0, 0, err
	}
	u, err := onlyOwn(users, readUser, 4)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: in %s, %w", errNoReadUser, passwd, err)
	}
	g, err := onlyOwn(groups, readUser, 4)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: its group in %s, %w", errNoReadUser, group, err)
	}
	primary := func(e idEntry) (uint32, bool) {
		id, err := strconv.ParseUint(e.fields[3], 10, 32)
		return uint32(id), err == nil
	}
	switch gid, ok := primary(u); {
	case u.id == 0 || g.id == 0:
		return 0, 0, fmt.Errorf("%w: it is root's uid or group", errNoReadUser)
	case !ok || gid != g.id:
		return 0, 0, fmt.Errorf("%w: its group isn't %s", errNoReadUser, readUser)
	case u.id != ReadID || g.id != ReadID:
		return 0, 0, fmt.Errorf("%w: it is %d:%d, not the packages' fixed %d:%d", errNoReadUser, u.id, g.id, ReadID, ReadID)
	case g.fields[3] != "":
		return 0, 0, fmt.Errorf("%w: its group has members", errNoReadUser)
	}
	for _, o := range users {
		if gid, ok := primary(o); o.fields[0] != readUser && (!ok || gid == g.id) && !o.broken {
			return 0, 0, fmt.Errorf("%w: its group is %s's too, or %s's is broken", errNoReadUser, o.fields[0], o.fields[0])
		}
	}
	return u.id, g.id, nil
}

// idEntry is a passwd or group entry: its fields, and its id (the third).
type idEntry struct {
	fields []string
	id     uint32
	broken bool
}

// idEntries is file's passwd or group entries.
func idEntries(file string) ([]idEntry, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errNoReadUser, err)
	}
	var out []idEntry
	for line := range strings.Lines(string(b)) {
		f := strings.Split(strings.TrimRight(line, "\n"), ":")
		if len(f) < 4 {
			continue
		}
		id, err := strconv.ParseUint(f[2], 10, 32)
		out = append(out, idEntry{fields: f, id: uint32(id), broken: err != nil})
	}
	return out, nil
}

// onlyOwn is name's one entry, with at least n fields, whose id no other entry has.
func onlyOwn(entries []idEntry, name string, n int) (idEntry, error) {
	var own []idEntry
	for _, e := range entries {
		if e.fields[0] == name {
			own = append(own, e)
		}
	}
	if len(own) != 1 {
		return idEntry{}, fmt.Errorf("%d entries for %s", len(own), name)
	}
	e := own[0]
	if e.broken || len(e.fields) < n {
		return idEntry{}, errors.New("a broken entry")
	}
	for _, o := range entries {
		if o.fields[0] != name && !o.broken && o.id == e.id {
			return idEntry{}, fmt.Errorf("its id %d is %s's too", e.id, o.fields[0])
		}
	}
	return e, nil
}

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
	Error   string          `json:"error,omitempty"` // not-ours, not-plain, off-floor, invalid-compose, cpu-limit or deadline
	Detail  string          `json:"detail,omitempty"`
	// Confined is how the read ran, as the child read it back from the kernel: its user and groups, its limits and
	// its OOM score, and the deadline and control pipe it was handed, for the tests.
	Confined Confined `json:"confined"`
}

// Confined is how a read's child ran.
type Confined struct {
	UID, GID    int
	Groups      string // /proc/self/status's Groups line
	Limits      map[string]uint64
	OOMScoreAdj string
	DeadlineMs  int64 // the deadline it ends itself at, from its start
	Controlled  bool  // it watches a control pipe, and ends when the pipe's write end closes
	Dumpable    int   // PR_GET_DUMPABLE: 0, so no process of its user may trace it or read its memory
	// DumpableAtStart is PR_GET_DUMPABLE as the child started, before it made itself not dumpable.
	DumpableAtStart int
}

// ReadProjectMain is the agent's read-project mode (homerslab agent read-project [--cpu-seconds N] [--deadline D]
// [--control-fd FD] -- PROJECT): the child the agent starts for each compose read, with the file on its standard input
// and the result, as JSON, on its standard output.
//
// What the child can do: read its standard input, and write in its own empty folder (its working directory, HOME
// and TMPDIR), where the reader puts the compose file it loads. What it can't:
//   - act as root: when the agent is root, the child starts as homerslab-read (readUser), a user only the agent's
//     reads use, with no supplementary groups, set in the fork before it runs any of its own code, so it holds no
//     capability and reads only what everyone may read; it never starts another program;
//   - be traced: a root agent's child runs a copy of the agent it can't read (mode 0711), so the kernel starts it
//     not dumpable (#190), and its first act, before it reads anything, is to make itself so anyway, so no other
//     process of its user, its parent or one an earlier read left behind even, may trace it or read its memory to
//     read its file or fake its answer, whatever Yama's ptrace_scope (#183);
//   - use more than its limits: 384 MiB of data (not under the race detector, whose shadow memory and thread stacks
//     need more), --cpu-seconds of CPU (soft; hard a second later), 32 open files, files of at most 1 MiB, no core
//     dump; and it is the first process
//     the kernel kills when memory runs short (oom_score_adj 1000, from the agent at its start), with the agent's unit
//     set to carry on (OOMPolicy);
//   - outlive the agent's deadline, or the agent: it ends itself at the deadline it was handed (--deadline, on its
//     own clock, ReadDeadline if none), and when its control pipe's write end closes (--control-fd), which the agent
//     does at its deadline and the kernel does when the agent dies, however it dies. The agent, root with its unit's
//     capabilities, has no CAP_KILL, so it can't signal a child that runs as another user, nor send it the
//     parent-death signal (#138): its kill and Pdeathsig hold only where it may; --cpu-seconds is the backstop the
//     kernel enforces; and a child that is stopped holds the agent's read slot no longer than the deadline and its
//     WaitDelay (reads.read);
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
	atStart, _ := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	// First of all, before it reads anything, its flags included: not dumpable.
	if err := undumpable(); err != nil {
		fmt.Fprintln(os.Stderr, "confining the read:", err)
		return 1
	}
	fs := flag.NewFlagSet("homerslab agent read-project", flag.ContinueOnError)
	cpu := fs.Int("cpu-seconds", 10, "the CPU time the read may use")
	deadline := fs.Duration("deadline", ReadDeadline, "how long the read may take, from the child's start")
	controlFD := fs.Int("control-fd", -1, "a pipe whose write end closing ends the read")
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: homerslab agent read-project [--cpu-seconds N] [--deadline D] [--control-fd FD] -- PROJECT < compose.yaml")
		return 2
	}
	var res readResult
	res.Confined.DumpableAtStart = atStart
	// The deadline's timer, the signal's goroutine and the main answer all write to stdout: one JSON object, whichever
	// comes first.
	var once sync.Once
	answer := func(r readResult) (err error) {
		once.Do(func() { err = json.NewEncoder(stdout).Encode(r) })
		return err
	}
	// Next, before the read can start: the deadline, on the child's own clock. The timer runs while the read
	// burns CPU, also on one core: Go preempts the read for it.
	time.AfterFunc(*deadline, func() {
		_ = answer(readResult{Error: "deadline"})
		os.Exit(1)
	})
	res.Confined.DeadlineMs = deadline.Milliseconds()
	// The control pipe: nothing is ever written to it, so a read that returns means its write end closed, or the pipe
	// broke. Either way, the read is over.
	if *controlFD >= 0 {
		control := os.NewFile(uintptr(*controlFD), "control")
		if err := controlPipe(control); err != nil {
			fmt.Fprintln(os.Stderr, "the read's control pipe:", err)
			return 1
		}
		go func() {
			_, _ = io.Copy(io.Discard, control)
			once.Do(func() {}) // an answer being written ends first; none is written after
			os.Exit(1)
		}()
		res.Confined.Controlled = true
	}
	// Go ignores SIGXCPU unless asked: asked, the child says it reached its soft CPU limit and ends, before the hard
	// limit kills it without a word.
	xcpu := make(chan os.Signal, 1)
	signal.Notify(xcpu, syscall.SIGXCPU)
	go func() {
		<-xcpu
		_ = answer(readResult{Error: "cpu-limit"})
		os.Exit(1)
	}()
	var err error
	if res.Confined, err = confine(*cpu, res.Confined); err != nil {
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
	case errors.Is(err, compose.ErrOffFloor):
		res.Error, res.Detail = "off-floor", err.Error()
	default:
		res.Error, res.Detail = "invalid-compose", err.Error()
	}
	if err := answer(res); err != nil {
		return 1
	}
	return 0
}

// controlPipe checks that f is the read end of a pipe: anything else could stay open after the agent is gone.
func controlPipe(f *os.File) error {
	if f == nil {
		return errors.New("no such file")
	}
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeNamedPipe == 0 {
		return fmt.Errorf("%s is not a pipe", fi.Mode())
	}
	// Its write end would be one the child holds itself, so it would never close.
	flags, err := unix.FcntlInt(f.Fd(), unix.F_GETFL, 0)
	if err != nil {
		return err
	}
	if flags&unix.O_ACCMODE != unix.O_RDONLY {
		return errors.New("the control pipe is not the read end")
	}
	return nil
}

// undumpable makes the child not dumpable, so that only a process with CAP_SYS_PTRACE may trace it or read its
// memory. A root agent's child already is, from its exec (the agent's execOnly); this holds for any other. Its OOM
// score of 1000 it has from the agent (startChild): once it isn't dumpable, its /proc files are root's, and it may no
// longer write it.
func undumpable() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("PR_SET_DUMPABLE: %w", err)
	}
	return nil
}

// confine sets the child's limits, and reads back how it runs, into c.
func confine(cpuSeconds int, c Confined) (Confined, error) {
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
	c.UID, c.GID, c.Limits = os.Getuid(), os.Getgid(), map[string]uint64{}
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
	d, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		return c, err
	}
	c.Dumpable = d
	return c, nil
}
