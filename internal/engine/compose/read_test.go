package compose_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/engine"
	"github.com/ArjunRachithcf/homerslab/internal/engine/compose"
	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

const twoServices = `
services:
  web:
    image: ghcr.io/example/web:1.2.3@sha256:1111111111111111111111111111111111111111111111111111111111111111
    environment:
      HOME_OF_THE_AGENT: ${HOME}
  db:
    image: docker.io/library/postgres:16@sha256:2222222222222222222222222222222222222222222222222222222222222222
    secrets: [db-password]
secrets:
  db-password:
    file: ${CREDENTIALS_DIRECTORY}/db-password
`

func reader(t *testing.T) *compose.Reader {
	t.Helper()
	r, err := compose.NewReader("")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Planning reads an app's compose project through the Compose SDK, with no engine anywhere: DOCKER_HOST points at
// nowhere, and reading needs none.
func TestAProjectIsReadThroughTheSDKWithoutAnEngine(t *testing.T) {
	testenv.Isolate(t)
	got, err := reader(t).Read(context.Background(), "homerslab-example", []byte(twoServices))
	if err != nil {
		t.Fatal(err)
	}
	want := engine.Project{Name: "homerslab-example", Services: []engine.Service{
		{Name: "db", Image: "docker.io/library/postgres:16@sha256:2222222222222222222222222222222222222222222222222222222222222222"},
		{Name: "web", Image: "ghcr.io/example/web:1.2.3@sha256:1111111111111111111111111111111111111111111111111111111111111111"},
	}}
	if len(got.Services) != 2 || got.Name != want.Name || got.Services[0] != want.Services[0] || got.Services[1] != want.Services[1] {
		t.Errorf("read\n %+v\nwant %+v", got, want)
	}
}

// Compose project names are HomersLab's own: Compose claims a container by its labels alone (ADR 0014).
func TestOnlyHomersLabsProjectNamesAreRead(t *testing.T) {
	testenv.Isolate(t)
	for _, name := range []string{"homelab", "", "homerslab-", "homerslab-Photos", "homerslab-a/b", "other-homerslab-x"} {
		if _, err := reader(t).Read(context.Background(), name, []byte(twoServices)); !errors.Is(err, compose.ErrNotOurs) {
			t.Errorf("project %q: %v, want ErrNotOurs", name, err)
		}
	}
}

// The agent reads as root: a definition's compose file is one plain file that names no other file to read, and the
// agent's environment never reaches it.
func TestAProjectReadsNothingButItsOwnFile(t *testing.T) {
	testenv.Isolate(t)
	for name, file := range map[string]string{
		"include":                     "include: [/etc/shadow]\nservices: {web: {image: x}}\n",
		"extends":                     "services: {web: {image: x, extends: {file: /etc/shadow, service: a}}}\n",
		"env_file":                    "services: {web: {image: x, env_file: [/etc/shadow]}}\n",
		"label_file":                  "services: {web: {image: x, label_file: [/etc/shadow]}}\n",
		"a second document":           "services: {web: {image: x}}\n---\ninclude: [/etc/shadow]\n",
		"extends behind a merge key":  "services: {web: {<<: {extends: {file: /tmp/secret.yaml, service: a}}, image: x}}\n",
		"include behind a merge key":  "<<: {include: [/tmp/secret.yaml]}\nservices: {web: {image: x}}\n",
		"env_file behind a merge key": "services: {web: {image: x, <<: {env_file: [/etc/shadow]}}}\n",
		"a merge tag":                 "services: {web: {image: x, !!merge m: {env_file: [/etc/shadow]}}}\n",
		"a later service":             "services: {web: {image: x}}\n---\nservices: {db: {image: y, env_file: [/etc/shadow]}}\n",
	} {
		if _, err := reader(t).Read(context.Background(), "homerslab-example", []byte(file)); !errors.Is(err, compose.ErrNotPlain) {
			t.Errorf("%s: %v, want ErrNotPlain", name, err)
		}
	}

	t.Setenv("HOMERSLAB_TEST_TAG", "from-the-agent")
	p, err := reader(t).Read(context.Background(), "homerslab-example", []byte("services: {web: {image: 'example/web:${HOMERSLAB_TEST_TAG}'}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Services[0].Image; got != "example/web:${HOMERSLAB_TEST_TAG}" {
		t.Errorf("the agent's environment reached the project: image %q", got)
	}
}

func TestAMalformedProjectSaysWhyWithoutTheAgentsPaths(t *testing.T) {
	testenv.Isolate(t)
	for _, file := range []string{"services:\n  web:\n    image: [\n", "services: {web: {image: x, restart: 7, imagee: y}}\n"} {
		_, err := reader(t).Read(context.Background(), "homerslab-example", []byte(file))
		if err == nil {
			t.Fatalf("a malformed file was read: %s", file)
		}
		if bytes.Contains([]byte(err.Error()), []byte("/tmp")) || bytes.Contains([]byte(err.Error()), []byte("homerslab-read")) {
			t.Errorf("the error names the agent's own folder: %v", err)
		}
	}
}

// laughs is a billion-laughs file: n aliases on each of levels levels, a few hundred bytes that expand to n^levels.
func laughs(n, levels int) string {
	var b strings.Builder
	b.WriteString("x-l0: &l0 [\"lol\"]\n")
	for l := 1; l <= levels; l++ {
		fmt.Fprintf(&b, "x-l%d: &l%d [", l, l)
		for i := range n {
			if i > 0 {
				b.WriteString(",")
			}
			fmt.Fprintf(&b, "*l%d", l-1)
		}
		b.WriteString("]\n")
	}
	b.WriteString("services: {web: {image: x}}\n")
	return b.String()
}

// refusalBudget is far above what a loaded runner needs and far below what unbounded work costs.
const refusalBudget = 10 * time.Second

// The agent reads as root, so a small file can't make it work hard: aliases, deep nesting and big files are refused
// before anything parses them in full.
func TestAProjectThatWouldExhaustTheAgentIsRefusedFast(t *testing.T) {
	testenv.Isolate(t)
	r := reader(t)
	for name, file := range map[string]string{
		"a billion laughs":                        laughs(9, 9),
		"one alias":                               "x-a: &a {image: x}\nservices: {web: *a}\n",
		"nesting 9000 deep":                       "services: {web: {image: x, x-deep: " + strings.Repeat("[", 9000) + strings.Repeat("]", 9000) + "}}\n",
		"nesting 65 deep":                         "services: {web: {image: x, x-deep: " + strings.Repeat("[", 62) + strings.Repeat("]", 62) + "}}\n",
		"a file over 64 KiB":                      "services: {web: {image: x}}\n" + strings.Repeat("# padding\n", 7000),
		"a hidden profile":                        "services: {web: {image: x@sha256:" + strings.Repeat("1", 64) + "}, extra: {image: 'evil:latest', profiles: [later]}}\n",
		"a profile behind a merge key":            "services: {web: {image: x}, extra: {<<: {image: 'evil:latest', profiles: [later]}}}\n",
		"extends /dev/zero behind a merge key":    "services: {web: {image: x, <<: {extends: {file: /dev/zero, service: a}}}}\n",
		"include /dev/urandom behind a merge key": "<<: {include: [/dev/urandom]}\nservices: {web: {image: x}}\n",
		"65 services":                             services(65, 0),
		"a depends_on fan":                        services(64, 40),
		"129 references, few paths":               hub(129),
		"more paths than the cap, few references": fib(20),
		"a links fan":                             fan("links"),
		"a volumes_from fan":                      fan("volumes_from"),
		"block nesting":                           "services:\n  web:\n    image: x\n    x-deep:\n" + deepBlock(70),
	} {
		start := time.Now()
		_, err := r.Read(context.Background(), "homerslab-example", []byte(file))
		// The refusal itself is the property: ErrNotPlain, not a parse that finished or failed otherwise. The clock only
		// catches a file that is expanded or walked before it is refused (a billion laughs takes hours, not seconds),
		// so it has headroom for a loaded CI runner (a 100 ms bound flaked at 1.7 s under load, #159).
		if !errors.Is(err, compose.ErrNotPlain) {
			t.Errorf("%s: %v, want ErrNotPlain", name, err)
		}
		if took := time.Since(start); took > refusalBudget {
			t.Errorf("%s refused after %s, want within %s", name, took, refusalBudget)
		}
	}
	// The longest chains the caps allow read quickly.
	for _, ref := range []string{"depends_on: [s%d]", "links: [s%d]", "volumes_from: [s%d]", "network_mode: 'service:s%d'", "ipc: 'service:s%d'", "pid: 'service:s%d'"} {
		start := time.Now()
		_, err := r.Read(context.Background(), "homerslab-example", []byte(chain(64, ref)))
		if err != nil {
			t.Errorf("a chain of 64 by %s: %v", ref, err)
		}
		if took := time.Since(start); took > refusalBudget {
			t.Errorf("a chain of 64 by %s: read after %s, want within %s", ref, took, refusalBudget)
		}
	}
	// 64 services, a few references each, and 64 levels are still a plain file.
	for name, file := range map[string]string{"64 services": services(64, 1), "a short chain": chain(16, "links: [s%d]"), "128 references": hub(128), "2567 paths": fib(14)} {
		if _, err := r.Read(context.Background(), "homerslab-example", []byte(file)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	ok := "services: {web: {image: x, x-deep: " + strings.Repeat("[", 60) + strings.Repeat("]", 60) + "}}\n"
	if _, err := r.Read(context.Background(), "homerslab-example", []byte(ok)); err != nil {
		t.Errorf("nesting 64 deep: %v", err)
	}
}

// services is n services, each depending on up to fan of the ones before it.
func services(n, fan int) string {
	var b strings.Builder
	b.WriteString("services:\n")
	for i := range n {
		fmt.Fprintf(&b, "  s%d:\n    image: x\n", i)
		if d := min(i, fan); d > 0 {
			b.WriteString("    depends_on: [")
			for j := i - d; j < i; j++ {
				if j > i-d {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "s%d", j)
			}
			b.WriteString("]\n")
		}
	}
	return b.String()
}

// hub is s0 and 63 services that refer to it (depends_on, links, and for the first few volumes_from) with exactly n
// references between them, and few paths: n up to 189.
func hub(n int) string {
	var b strings.Builder
	b.WriteString("services:\n  s0: {image: x}\n")
	extra := n - 126
	for i := 1; i <= 63; i++ {
		refs := "depends_on: [s0], links: [s0]"
		if i <= extra {
			refs += ", volumes_from: [s0]"
		}
		fmt.Fprintf(&b, "  s%d: {image: x, %s}\n", i, refs)
	}
	return b.String()
}

// fan is 64 services, each referring to all the earlier ones with kind (links, volumes_from).
func fan(kind string) string {
	var b strings.Builder
	b.WriteString("services:\n")
	for i := range 64 {
		fmt.Fprintf(&b, "  s%d:\n    image: x\n", i)
		if i > 0 {
			fmt.Fprintf(&b, "    %s: [", kind)
			for j := range i {
				if j > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "s%d", j)
			}
			b.WriteString("]\n")
		}
	}
	return b.String()
}

// chain is n services, each referring to the one before it with ref.
func chain(n int, ref string) string {
	var b strings.Builder
	b.WriteString("services:\n  s0: {image: x}\n")
	for i := 1; i < n; i++ {
		fmt.Fprintf(&b, "  s%d: {image: x, %s}\n", i, fmt.Sprintf(ref, i-1))
	}
	return b.String()
}

func deepBlock(n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "%s- \n", strings.Repeat("  ", i+3))
	}
	return b.String()
}

// utsWeb is 64 services, each sharing the UTS namespace of the one before it and the network of the one before that:
// Compose counts both as dependencies.
func utsWeb() string {
	var b strings.Builder
	b.WriteString("services:\n  s0: {image: x}\n  s1: {image: x, uts: 'service:s0'}\n")
	for i := 2; i < 64; i++ {
		fmt.Fprintf(&b, "  s%d: {image: x, uts: 'service:s%d', network_mode: 'service:s%d'}\n", i, i-1, i-2)
	}
	return b.String()
}

// fib is n services, each depending on the two before it: few references, but a number of dependency paths that grows
// like the Fibonacci numbers.
func fib(n int) string {
	var b strings.Builder
	b.WriteString("services:\n  s0: {image: x}\n  s1: {image: x, depends_on: [s0]}\n")
	for i := 2; i < n; i++ {
		fmt.Fprintf(&b, "  s%d: {image: x, depends_on: [s%d, s%d]}\n", i, i-1, i-2)
	}
	return b.String()
}

// Compose expands a port range into one entry per port, and walks every path of dependencies: both are refused before
// they cost anything.
func TestAProjectThatMultipliesItsWorkIsRefusedFast(t *testing.T) {
	testenv.Isolate(t)
	r := reader(t)
	var bomb strings.Builder
	bomb.WriteString("services:\n  s:\n    image: x\n    ports:\n")
	for i := range 20 {
		fmt.Fprintf(&bomb, "      - \"10.0.0.%d:1-65535:1-65535\"\n", i)
	}
	for name, file := range map[string]string{
		"20 port ranges":             bomb.String(),
		"one port range":             "services: {s: {image: x, ports: ['1-65535:1-65535']}}\n",
		"a long-form port range":     "services: {s: {image: x, ports: [{target: 80, published: '8000-9000'}]}}\n",
		"a target range":             "services: {s: {image: x, ports: [{target: '1-65535'}]}}\n",
		"an exposed range":           "services: {s: {image: x, expose: ['1-65535']}}\n",
		"a Fibonacci depends_on":     fib(64),
		"a Fibonacci with links":     strings.ReplaceAll(fib(64), "depends_on", "links"),
		"a cycle":                    "services: {a: {image: x, depends_on: [b]}, b: {image: x, depends_on: [a]}}\n",
		"a uts and network_mode web": utsWeb(),
	} {
		start := time.Now()
		_, err := r.Read(context.Background(), "homerslab-example", []byte(file))
		// The refusal itself is the property: ErrNotPlain, not a parse that finished or failed otherwise. The clock only
		// catches a file that is expanded or walked before it is refused (a billion laughs takes hours, not seconds),
		// so it has headroom for a loaded CI runner (a 100 ms bound flaked at 1.7 s under load, #159).
		if !errors.Is(err, compose.ErrNotPlain) {
			t.Errorf("%s: %v, want ErrNotPlain", name, err)
		}
		if took := time.Since(start); took > refusalBudget {
			t.Errorf("%s refused after %s, want within %s", name, took, refusalBudget)
		}
	}
	for name, file := range map[string]string{
		"one port":      "services: {s: {image: x, expose: ['80'], ports: ['127.0.0.1:8080:80']}}\n",
		"a short fib":   fib(10),
		"a shared base": "services: {db: {image: x}, a: {image: x, depends_on: [db]}, b: {image: x, depends_on: [db, a]}}\n",
	} {
		if _, err := r.Read(context.Background(), "homerslab-example", []byte(file)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Every way of writing a merge key that the YAML parser Compose uses understands is refused, and no file a compose
// file names is ever read into the project (the review's probes).
func TestEveryMergeKeyAndFileReferenceIsHarmless(t *testing.T) {
	testenv.Isolate(t)
	r := reader(t)
	hidden := "{image: x, profiles: [p]}"
	for name, f := range map[string]string{
		"plain":       "services:\n  s:\n    image: x\n    <<: {profiles: [p]}\n",
		"tagmerge":    "services:\n  s:\n    image: x\n    !!merge foo: {profiles: [p]}\n",
		"verbatim":    "services:\n  s:\n    image: x\n    !<tag:yaml.org,2002:merge> foo: {profiles: [p]}\n",
		"handle":      "%TAG !m! tag:yaml.org,2002:\n---\nservices:\n  s:\n    image: x\n    !m!merge foo: {profiles: [p]}\n",
		"handle2":     "%TAG !! tag:yaml.org,2002:\n---\nservices:\n  s:\n    image: x\n    !!merge foo: {profiles: [p]}\n",
		"localhandle": "%TAG ! tag:yaml.org,2002:\n---\nservices:\n  s:\n    image: x\n    !merge foo: {profiles: [p]}\n",
		"complexkey":  "services:\n  s:\n    image: x\n    ? <<\n    : {profiles: [p]}\n",
		"flow":        "services: {s: {image: x, <<: {profiles: [p]}}}\n",
		"seq":         "services:\n  s:\n    image: x\n    <<: [{profiles: [p]}]\n",
		"svclevel":    "services:\n  s: {image: x}\n  <<: {t: " + hidden + "}\n",
		"root":        "<<: {services: {t: " + hidden + "}}\n",
		"rootinclude": "!!merge x: {include: [/etc/passwd]}\nservices: {s: {image: x}}\n",
		"bom":         "\ufeffservices:\n  s:\n    image: x\n    <<: {profiles: [p]}\n",
		"pct":         "services:\n  s:\n    image: x\n    !<tag:yaml.org,2002:%6Derge> foo: {profiles: [p]}\n",
		"dq":          "services:\n  s:\n    image: x\n    \"<<\": {profiles: [p]}\n",
		"sq":          "services:\n  s:\n    image: x\n    '<<': {profiles: [p]}\n",
		"strtag":      "services:\n  s:\n    image: x\n    !!str <<: {profiles: [p]}\n",
		"secretfile":  "services: {s: {image: x, secrets: [k]}}\nsecrets: {k: {file: /etc/passwd}}\n",
		"configfile":  "services: {s: {image: x, configs: [k]}}\nconfigs: {k: {file: /etc/passwd}}\n",
		"build":       "services: {s: {image: x, build: {context: /etc, dockerfile: passwd}}}\n",
		"envfilelong": "services: {s: {image: x, env_file: [{path: /etc/passwd, required: true}]}}\n",
		"x-ext":       "x-a: {include: [/etc/passwd]}\nservices: {s: {image: x}}\n",
		"includelong": "include:\n  - path: /etc/passwd\n    env_file: /etc/shadow\nservices: {s: {image: x}}\n",
		"multidoc":    "services: {s: {image: x}}\n---\ninclude: [/etc/passwd]\n",
		"multidoc2":   "services: {s: {image: x}}\n---\nservices: {s: {extends: {file: /etc/passwd, service: x}}}\n",
	} {
		p, err := r.Read(context.Background(), "homerslab-example", []byte(f))
		if err != nil {
			if strings.Contains(err.Error(), "root:") {
				t.Errorf("%s: a file's content reached the error: %v", name, err)
			}
			continue
		}
		if len(p.Services) != 1 || p.Services[0].Name != "s" || p.Services[0].Image != "x" {
			t.Errorf("%s: read %+v, want only s, as written", name, p.Services)
		}
	}
}
