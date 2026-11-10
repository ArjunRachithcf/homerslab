// Command apps runs one tsnet node per app inside a single process, each serving HTTP on :80
// of its own tailnet address. It stands in for HomersLab's agent in this spike.
//
// Each node joins with its own pre-auth key (<keys>/<name>.key) or one shared key (-key-file).
// Optionally one more node (-dns-name) answers DNS on its tailnet address by forwarding to an
// upstream resolver on the local network: a resolver "behind a tailnet node".
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/logtail"
	"tailscale.com/tsnet"
)

type app struct {
	name string
	srv  *tsnet.Server
	lc   *local.Client
	ip4  netip.Addr
	ip6  netip.Addr
	dns  string
	took time.Duration
}

var nodesUp atomic.Int64

func main() {
	control := flag.String("control", "https://headscale.hlspike.test:8443", "control server URL")
	appsFlag := flag.String("apps", "", "comma-separated app names, one tsnet node each")
	n := flag.Int("n", 0, "instead of -apps: N generated names (<prefix>001…)")
	prefix := flag.String("prefix", "app", "name prefix for -n")
	keyDir := flag.String("keys", "/work/keys", "directory with <name>.key pre-auth keys")
	keyFile := flag.String("key-file", "", "one (reusable) pre-auth key for every node, instead of -keys")
	stateDir := flag.String("state", "/work/state", "state directory, one subdirectory per node")
	parallel := flag.Int("parallel", 8, "how many nodes start at once")
	statsEvery := flag.Duration("stats-every", 30*time.Second, "interval of the stats line (0: off)")
	tryTLS := flag.Bool("try-tls", false, "on the first node, try ListenTLS, ListenFunnel and a certificate")
	logs := flag.String("logs", "file", "tsnet backend logs: file (<state>/<name>/tsnet.log), stderr or none")
	dnsName := flag.String("dns-name", "", "also run a resolver node with this hostname")
	dnsUpstream := flag.String("dns-upstream", "10.77.29.30:53", "where the resolver node forwards DNS")
	upTimeout := flag.Duration("up-timeout", 3*time.Minute, "how long a node may take to join")
	exitAfterStart := flag.Bool("exit-after-start", false, "exit once every node is up or has failed")
	flag.Parse()

	// tsnet always builds its own log uploader to log.tailscale.com (it ignores
	// TS_NO_LOGS_NO_SUPPORT). Headscale turns it off with the first full map response
	// (Debug.DisableLogTail); turn it off before any node starts, so nothing leaves early.
	logtail.Disable()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)

	var names []string
	switch {
	case *appsFlag != "":
		names = strings.Split(*appsFlag, ",")
	case *n > 0:
		for i := 1; i <= *n; i++ {
			names = append(names, fmt.Sprintf("%s%03d", *prefix, i))
		}
	}
	if *dnsName != "" {
		names = append(names, *dnsName)
	}
	if len(names) == 0 {
		log.Fatal("nothing to run: give -apps, -n or -dns-name")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *statsEvery > 0 {
		go func() {
			t := time.NewTicker(*statsEvery)
			defer t.Stop()
			for {
				printStats("periodic")
				select {
				case <-ctx.Done():
					return
				case <-t.C:
				}
			}
		}()
	}

	start := time.Now()
	apps := make([]*app, len(names))
	sem := make(chan struct{}, *parallel)
	var wg sync.WaitGroup
	var failed atomic.Int64
	for i, name := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			a, err := startNode(ctx, name, *control, *keyDir, *keyFile, *stateDir, *logs, *upTimeout)
			if err != nil {
				failed.Add(1)
				log.Printf("node %s: FAILED: %v", name, err)
				return
			}
			apps[i] = a
			nodesUp.Add(1)
			if name == *dnsName {
				if err := runDNSForwarder(a, *dnsUpstream); err != nil {
					log.Printf("node %s: resolver: %v", name, err)
				} else {
					log.Printf("node %s: resolver on %s:53 (udp+tcp) → %s", name, a.ip4, *dnsUpstream)
				}
				return
			}
			if err := serveHTTP(a); err != nil {
				log.Printf("node %s: http: %v", name, err)
			}
		}()
	}
	wg.Wait()
	log.Printf("all nodes started: up=%d failed=%d in %v", nodesUp.Load(), failed.Load(), time.Since(start).Round(time.Millisecond))
	writeNodes(filepath.Join(*stateDir, "nodes.json"), apps)
	printStats("after-start")

	if *tryTLS {
		for _, a := range apps {
			if a != nil {
				probeTLS(ctx, a)
				break
			}
		}
	}

	if !*exitAfterStart {
		<-ctx.Done()
	}
	log.Printf("shutting down")
	for _, a := range apps {
		if a != nil {
			a.srv.Close()
		}
	}
}

func startNode(ctx context.Context, name, control, keyDir, keyFile, stateDir, logs string, upTimeout time.Duration) (*app, error) {
	dir := filepath.Join(stateDir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	srv := &tsnet.Server{
		Hostname:   name,
		Dir:        dir,
		ControlURL: control,
		UserLogf:   func(format string, args ...any) { log.Printf("node %s: "+format, append([]any{name}, args...)...) },
	}
	switch logs {
	case "stderr":
		srv.Logf = func(format string, args ...any) { log.Printf("[%s] "+format, append([]any{name}, args...)...) }
	case "file":
		f, err := os.OpenFile(filepath.Join(dir, "tsnet.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return nil, err
		}
		l := log.New(f, "", log.LstdFlags|log.Lmicroseconds|log.LUTC)
		srv.Logf = l.Printf
	}
	// The key is only needed for the first join: tsnet ignores AuthKey once the node's state
	// holds a login. A missing key file is fine for a node that already joined.
	kf := keyFile
	if kf == "" {
		kf = filepath.Join(keyDir, name+".key")
	}
	if b, err := os.ReadFile(kf); err == nil {
		srv.AuthKey = strings.TrimSpace(string(b))
	} else if _, serr := os.Stat(filepath.Join(dir, "tailscaled.state")); errors.Is(serr, os.ErrNotExist) {
		return nil, fmt.Errorf("pre-auth key: %w", err)
	}
	t0 := time.Now()
	upCtx, cancel := context.WithTimeout(ctx, upTimeout)
	defer cancel()
	st, err := srv.Up(upCtx)
	if err != nil {
		srv.Close()
		return nil, err
	}
	lc, err := srv.LocalClient()
	if err != nil {
		return nil, err
	}
	a := &app{name: name, srv: srv, lc: lc, dns: strings.TrimSuffix(st.Self.DNSName, "."), took: time.Since(t0)}
	a.ip4, a.ip6 = srv.TailscaleIPs()
	log.Printf("node %s: up in %v: %s %s tags=%v", name, a.took.Round(time.Millisecond), a.dns, a.ip4, tagsOf(st.Self.Tags))
	return a, nil
}

func tagsOf(t interface{ AsSlice() []string }) []string {
	if t == nil {
		return nil
	}
	return t.AsSlice()
}

func serveHTTP(a *app) error {
	ln, err := a.srv.Listen("tcp", ":80")
	if err != nil {
		return err
	}
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller := "unknown"
		if wi, err := a.lc.WhoIs(r.Context(), r.RemoteAddr); err == nil && wi.Node != nil {
			if len(wi.Node.Tags) > 0 {
				caller = fmt.Sprintf("%s (tags %s)", strings.TrimSuffix(wi.Node.Name, "."), strings.Join(wi.Node.Tags, ","))
			} else if wi.UserProfile != nil {
				caller = fmt.Sprintf("%s (user %s)", strings.TrimSuffix(wi.Node.Name, "."), wi.UserProfile.LoginName)
			}
		}
		fmt.Fprintf(w, "app=%s node=%s ip=%s host=%s caller=%s\n", a.name, a.dns, a.ip4, r.Host, caller)
	})
	go (&http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}).Serve(ln)
	return nil
}

// runDNSForwarder answers DNS on the node's tailnet address (UDP and TCP 53) by relaying each
// query to upstream, which lives on the local network and is not on the tailnet itself.
func runDNSForwarder(a *app, upstream string) error {
	pc, err := a.srv.ListenPacket("udp", net.JoinHostPort(a.ip4.String(), "53"))
	if err != nil {
		return err
	}
	go func() {
		buf := make([]byte, 65535)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			q := append([]byte(nil), buf[:n]...)
			go func() {
				c, err := net.DialTimeout("udp", upstream, 2*time.Second)
				if err != nil {
					return
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(3 * time.Second))
				if _, err := c.Write(q); err != nil {
					return
				}
				resp := make([]byte, 65535)
				m, err := c.Read(resp)
				if err != nil {
					return
				}
				pc.WriteTo(resp[:m], from)
			}()
		}
	}()
	ln, err := a.srv.Listen("tcp", ":53")
	if err != nil {
		return err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				u, err := net.DialTimeout("tcp", upstream, 2*time.Second)
				if err != nil {
					return
				}
				defer u.Close()
				go io.Copy(u, c)
				io.Copy(c, u)
			}()
		}
	}()
	return nil
}

// probeTLS records what Headscale v0.29.7 gives a node for HTTPS and Funnel.
func probeTLS(ctx context.Context, a *app) {
	log.Printf("probe %s: CertDomains=%v", a.name, a.srv.CertDomains())
	if ln, err := a.srv.ListenTLS("tcp", ":443"); err != nil {
		log.Printf("probe %s: ListenTLS: %v", a.name, err)
	} else {
		log.Printf("probe %s: ListenTLS: ok", a.name)
		ln.Close()
	}
	if ln, err := a.srv.ListenFunnel("tcp", ":443"); err != nil {
		log.Printf("probe %s: ListenFunnel: %v", a.name, err)
	} else {
		log.Printf("probe %s: ListenFunnel: ok", a.name)
		ln.Close()
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if _, _, err := a.lc.CertPair(cctx, a.dns); err != nil {
		log.Printf("probe %s: CertPair(%s): %v", a.name, a.dns, err)
	} else {
		log.Printf("probe %s: CertPair(%s): ok", a.name, a.dns)
	}
}

func writeNodes(path string, apps []*app) {
	type row struct {
		Name   string  `json:"name"`
		IP4    string  `json:"ip4"`
		IP6    string  `json:"ip6"`
		DNS    string  `json:"dns"`
		JoinMS float64 `json:"join_ms"`
	}
	var rows []row
	for _, a := range apps {
		if a != nil {
			rows = append(rows, row{a.name, a.ip4.String(), a.ip6.String(), a.dns, float64(a.took.Microseconds()) / 1000})
		}
	}
	b, _ := json.MarshalIndent(rows, "", "  ")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		log.Printf("write %s: %v", path, err)
	}
}

// printStats prints one JSON line about this process: memory as the kernel sees it (VmRSS),
// the Go runtime's view, goroutines, OS threads, open files and CPU time used so far.
func printStats(when string) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	st := map[string]any{
		"when":            when,
		"nodes_up":        nodesUp.Load(),
		"goroutines":      runtime.NumGoroutine(),
		"heap_inuse_kib":  ms.HeapInuse / 1024,
		"stack_inuse_kib": ms.StackInuse / 1024,
		"go_sys_kib":      ms.Sys / 1024,
		"num_gc":          ms.NumGC,
	}
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			f := strings.Fields(v)
			if len(f) == 0 {
				continue
			}
			switch k {
			case "VmRSS":
				st["rss_kib"], _ = strconv.Atoi(f[0])
			case "VmHWM":
				st["rss_peak_kib"], _ = strconv.Atoi(f[0])
			case "RssAnon":
				st["rss_anon_kib"], _ = strconv.Atoi(f[0])
			case "RssFile":
				st["rss_file_kib"], _ = strconv.Atoi(f[0])
			case "Threads":
				st["threads"], _ = strconv.Atoi(f[0])
			}
		}
	}
	if ents, err := os.ReadDir("/proc/self/fd"); err == nil {
		st["fds"] = len(ents)
	}
	if b, err := os.ReadFile("/proc/self/stat"); err == nil {
		s := string(b)
		if i := strings.LastIndexByte(s, ')'); i > 0 {
			f := strings.Fields(s[i+1:])
			if len(f) > 12 {
				u, _ := strconv.ParseFloat(f[11], 64)
				k, _ := strconv.ParseFloat(f[12], 64)
				st["cpu_s"] = (u + k) / 100 // USER_HZ is 100 on Linux
			}
		}
	}
	b, _ := json.Marshal(st)
	log.Printf("stats %s", b)
}
