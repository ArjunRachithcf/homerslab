// Package home is Home: the surface where people open their apps (ADR 0013, spec: Modules → Surfaces). It is its own
// process, unprivileged, which reads only a projection of apps, audiences and health that the manager writes, and
// signs people in with the household account through an OIDC client of its own.
//
// Home holds no token of HomersLab's and opens nothing of the manager's or the agent's: it is built from none of their
// packages (deps_test.go), and its unit gives it no way to the manager's sockets or database (packaging/).
package home

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Projection is everything Home knows of the household: the manager writes it, Home reads it. None of it is secret.
type Projection struct {
	// Household is the household's name.
	Household string `json:"household"`
	// Home is Home's one origin, https://home.<main domain>[:port] (ADR 0021): the only name it answers, and the
	// exact Origin a change from its page carries.
	Home string `json:"home"`
	// Admin is Admin's URL, which Home links to for admins.
	Admin string `json:"admin,omitempty"`
	// SignIn is Home's client at the household account; nil until the household account is set up.
	SignIn *SignIn `json:"signIn,omitempty"`
	// Apps are the household's apps.
	Apps   []App     `json:"apps"`
	MadeAt time.Time `json:"madeAt"`
}

// SignIn is Home's OIDC client: its issuer and ID. Its secret is a credential of Home's unit, never here.
type SignIn struct {
	Issuer   string `json:"issuer"`
	ClientID string `json:"clientId"`
	// Upstream is where the sign-in's server answers on this machine, for Home's own calls to it (the code's
	// exchange, the issuer's keys).
	Upstream string `json:"upstream"`
}

// App is one of the household's apps, as Home shows it.
type App struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Purpose  string `json:"purpose"`
	Category string `json:"category"`
	// URL is the app's one URL (ADR 0021).
	URL string `json:"url"`
	// Audience are the roles that may open it: admin, adult, kid.
	Audience []string `json:"audience"`
	// Health is the app's health as the agent last found it: healthy, starting, unhealthy, stopped or unknown.
	Health string `json:"health"`
	Icon   *Icon  `json:"icon,omitempty"`
}

// Icon is an app's icon, from its catalog.
type Icon struct {
	Type string `json:"type"`
	Data []byte `json:"data"`
}

// maxProjection is the largest projection Home reads.
const maxProjection = 8 << 20

// Write replaces the projection at path, whole or not at all: a reader sees the old one or the new one. The file is
// 0640, for its folder's group (Home's), which a setgid folder gives it.
func Write(path string, p Projection) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if len(b) > maxProjection {
		return fmt.Errorf("the projection is %d bytes, more than Home reads", len(b))
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".home-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Chmod(0o640)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if err = errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Read reads the projection at path.
func Read(path string) (Projection, error) {
	f, err := os.Open(path)
	if err != nil {
		return Projection{}, err
	}
	defer f.Close()
	var p Projection
	b, err := io.ReadAll(io.LimitReader(f, maxProjection+1))
	if err != nil {
		return Projection{}, err
	}
	if len(b) > maxProjection {
		return Projection{}, errors.New("the projection is larger than Home reads")
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return Projection{}, err
	}
	return p, nil
}
