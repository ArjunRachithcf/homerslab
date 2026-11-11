package compose_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

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
		"include":    "include: [/etc/shadow]\nservices: {web: {image: x}}\n",
		"extends":    "services: {web: {image: x, extends: {file: /etc/shadow, service: a}}}\n",
		"env_file":   "services: {web: {image: x, env_file: [/etc/shadow]}}\n",
		"label_file": "services: {web: {image: x, label_file: [/etc/shadow]}}\n",
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
