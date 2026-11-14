// Package appunit is what the agent writes for an app it installs (#19): the app's one systemd unit, which runs the
// standard compose command (ADR 0014), and the second compose file it runs the app's own with, which wires what
// HomersLab adds. Both are rendered from the app as the agent keeps it, byte for byte the same each time, so they can
// be compared with a golden render and rewritten at every apply.
//
//   - systemd owns the app: every service's restart policy is off, and the unit restarts the app (ADR 0011, amended);
//   - every container runs at the app's own SELinux MCS level, which no other app shares;
//   - the app's secrets are systemd credentials the agent sealed, which the unit decrypts as it starts, copies into its
//     own runtime directory for the one container user that reads each, labelled at the app's level, and hands the
//     containers as Compose secrets: never an environment variable, never a folder two containers share;
//   - the app's one upstream, which the edge proxies to, is a fixed address on a network of its own; the firewall table
//     lets only the edge's user and the agent's open it (ADR 0020). The app publishes no port.
package appunit

import (
	"fmt"
	"maps"
	"net/netip"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ArjunRachithcf/homerslab/internal/catalog"
	"github.com/ArjunRachithcf/homerslab/internal/engine"
)

// UpstreamNetwork is the network of the app's one upstream, in its compose project.
const UpstreamNetwork = "homerslab-upstream"

// App is an app as the agent runs it.
type App struct {
	// ID is the app's id in its catalog (bentopdf); Product its name (BentoPDF); Plan the install plan last applied.
	ID, Product, Plan string
	// Project is the app's compose project, as the agent read it from the catalog's file.
	Project engine.Project
	// Service is the service the edge proxies to; Upstream its fixed address and port on Subnet.
	Service  string
	Upstream netip.AddrPort
	Subnet   netip.Prefix
	// Level is the app's own SELinux MCS level (s0:c600,c601); SELinux says whether the machine has SELinux, so the
	// unit labels the app's secrets. Slot is the app's number on this node, which its bridges are named after.
	Level string
	Slot  int
	// SignIn is the sign-in's name (auth.<main domain>), which every container resolves to the sign-in listener's
	// address, SignInAt (ADR 0020); none while the sign-in isn't served.
	SignIn   string
	SignInAt netip.Addr
	SELinux  bool
	Secrets  []Secret
	// OIDC is the OIDC client of an app that signs people in itself (#20), nil for any other.
	OIDC *OIDC
	// Dir is where the agent keeps the app's compose files and sealed credentials.
	Dir string
}

// OIDC is how an app takes the OIDC client the household account made for it: its settings as environment variables
// of the upstream's service, none of them secret, and the client's secret as a file only, at ClientSecretPath, which
// SecretUID reads. The secret is a credential the agent sealed, like the app's own (ADR 0011).
type OIDC struct {
	Env       map[string]string `json:"env"`
	SecretUID int               `json:"secretUid"`
}

// ClientSecret is the client's secret, as a secret of the app's (the name no manifest may give one of its own);
// ClientSecretPath where its containers find it.
const (
	ClientSecret     = catalog.ClientSecret
	ClientSecretPath = "/run/secrets/" + ClientSecret
)

// Handed are every secret the unit hands the app: its own, and its OIDC client's.
func (a App) Handed() []Secret {
	s := slices.Clone(a.Secrets)
	if a.OIDC != nil {
		s = append(s, Secret{Name: ClientSecret, UID: a.OIDC.SecretUID})
	}
	return sortedSecrets(s)
}

// Secret is one of the app's secrets, and the container user that reads it.
type Secret struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
}

// ID is an app's id as the agent takes it: what a unit, a project and a runtime directory can be named after.
var ID = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// PlanID is an install plan's id, the one thing of a plan the files name besides the app's id.
var PlanID = regexp.MustCompile(`^p-[0-9a-f]{32}$`)

// SecretName is a secret's name, as a file and a credential can carry it.
var SecretName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// Unit is the app's unit's name.
func Unit(id string) string { return "homerslab-app-" + id + ".service" }

// Project is the app's compose project's name.
func Project(id string) string { return engine.ProjectPrefix + id }

// RunDir is the unit's runtime directory, where it puts the files its containers mount: 0700, gone when it stops.
func RunDir(id string) string { return "/run/homerslab-app-" + id }

// Credential is a secret's credential ID, which is also the name sealed inside it: <app>.<name>, so one app's file
// can't stand in for another's (ADR 0011).
func Credential(id, name string) string { return id + "." + name }

// CredentialFile is where the agent keeps a secret's sealed credential.
func (a App) CredentialFile(name string) string { return path.Join(a.Dir, "credentials", name+".cred") }

// ComposeFile is the catalog's compose file, as it is; OverrideFile the agent's, which Override renders.
func (a App) ComposeFile() string  { return path.Join(a.Dir, "compose.yaml") }
func (a App) OverrideFile() string { return path.Join(a.Dir, "homerslab.yaml") }

// Bridge is the name of an app's bridge: the upstream's (network -1), or the project's i-th network's, in name order.
func Bridge(slot, network int) string {
	if network < 0 {
		return fmt.Sprintf("%s%du", engine.BridgePrefix, slot)
	}
	return fmt.Sprintf("%s%dn%d", engine.BridgePrefix, slot, network)
}

// q is a string as YAML takes it: double-quoted, with everything but printable ASCII escaped (\xNN, \uNNNN,
// \UNNNNNNNN, which YAML's double-quoted scalars read as Go writes them), so no character of it can end the scalar or
// the line, whatever YAML counts as a line break (U+0085, U+2028, U+2029 among them).
func q(s string) string { return strconv.QuoteToASCII(s) }

// plan is the plan's id for a comment, or "unknown" for anything that isn't one: comments can't be escaped.
func plan(p string) string {
	if PlanID.MatchString(p) {
		return p
	}
	return "unknown"
}

// id is the app's id for a comment or a unit's line, or "unknown" for anything that isn't one.
func id(a App) string {
	if ID.MatchString(a.ID) {
		return a.ID
	}
	return "unknown"
}

// Override is the agent's compose file for the app, the second one its unit runs the app's own with.
func Override(a App) []byte {
	var b strings.Builder
	w := func(f string, args ...any) { fmt.Fprintf(&b, f+"\n", args...) }
	// No free text: comments carry only the app's id and the plan's, which can't end a line (#195's review).
	w("# Written by HomersLab's agent, from install plan %s. Never edit by hand.", plan(a.Plan))
	w("# What HomersLab adds to app %s's own compose file, as the second file of the standard compose command (ADR 0014):", id(a))
	w("#   - every service: no restart policy, the engine's nor swarm's (the app's unit restarts the app), at the app's own")
	w("#     SELinux level;")
	w("#   - the app's one upstream, a fixed address on a network of its own, which only the edge and the agent may open;")
	w("#   - every network's bridge named by the agent, so the firewall table knows it before it is made;")
	w("#   - the sign-in's name, at the sign-in listener's address: the one thing of this machine's the app may open;")
	w("#   - its secrets, as files its unit copies from the credentials the agent sealed;")
	w("#   - for an app that signs people in itself, its OIDC client's settings, and the client's secret as a file.")
	w("name: %s", q(Project(a.ID)))
	w("services:")
	services := slices.Clone(a.Project.Services)
	slices.SortFunc(services, func(x, y engine.Service) int { return strings.Compare(x.Name, y.Name) })
	for _, s := range services {
		w("  %s:", q(s.Name))
		w("    restart: \"no\"")
		w("    deploy:")
		w("      restart_policy:")
		w("        condition: \"none\"")
		w("    security_opt:")
		w("      - %s", q("label=level:"+a.Level))
		if a.SignIn != "" && a.SignInAt.IsValid() {
			w("    extra_hosts:")
			w("      - %s", q(a.SignIn+"="+a.SignInAt.String()))
		}
		if s.Name != a.Service {
			continue
		}
		if a.OIDC != nil {
			w("    environment:")
			for _, k := range slices.Sorted(maps.Keys(a.OIDC.Env)) {
				w("      %s: %s", q(k), q(a.OIDC.Env[k]))
			}
			w("    secrets:")
			w("      - %s", q(ClientSecret))
		}
		w("    networks:")
		if len(s.Networks) == 0 {
			w("      \"default\": {}") // the one the service had: adding a network would otherwise replace it
		}
		w("      %s:", q(UpstreamNetwork))
		w("        ipv4_address: %s", q(a.Upstream.Addr().String()))
	}
	w("networks:")
	for i, n := range a.Project.Networks {
		w("  %s:", q(n.Name))
		w("    driver_opts:")
		w("      com.docker.network.bridge.name: %s", q(Bridge(a.Slot, i)))
	}
	w("  %s:", q(UpstreamNetwork))
	w("    driver: bridge")
	w("    driver_opts:")
	w("      com.docker.network.bridge.name: %s", q(Bridge(a.Slot, -1)))
	w("    ipam:")
	w("      config:")
	w("        - subnet: %s", q(a.Subnet.String()))
	w("          gateway: %s", q(a.Subnet.Addr().Next().String()))
	if secrets := a.Handed(); len(secrets) > 0 {
		w("secrets:")
		for _, s := range secrets {
			w("  %s:", q(s.Name))
			w("    file: %s", q(path.Join(RunDir(a.ID), s.Name)))
		}
	}
	return []byte(b.String())
}

// UnitFile is the app's systemd unit: the standard compose command on the catalog's file and the agent's, with the app's
// credentials.
func UnitFile(a App) []byte {
	var b strings.Builder
	w := func(f string, args ...any) { fmt.Fprintf(&b, f+"\n", args...) }
	run := RunDir(a.ID)
	compose := fmt.Sprintf("/usr/bin/docker compose --project-name %s --project-directory %s --file %s --file %s",
		Project(a.ID), a.Dir, a.ComposeFile(), a.OverrideFile())
	// No free text: the app's id and the plan's only, which no line of a unit can be ended or continued by.
	w("# Written by HomersLab's agent, from install plan %s. Never edit by hand.", plan(a.Plan))
	w("# App %s, run by the standard compose command on its catalog's compose file and the agent's (ADR 0014).", id(a))
	w("# systemd owns the containers: their restart policies are off, and this unit restarts them; a container that stops")
	w("# ends the compose command, which stops the others. The app's secrets are credentials the agent sealed (ADR 0011),")
	w("# copied into the runtime directory for the one container user that reads each, at the app's own SELinux level.")
	w("# No mount sandboxing: Docker's daemon must see that directory.")
	w("[Unit]")
	w("Description=HomersLab app %s", id(a))
	w("Documentation=https://github.com/ArjunRachithcf/homerslab")
	w("Requires=docker.service")
	w("After=docker.service network-online.target homerslab-agent.service")
	w("Wants=network-online.target")
	w("")
	w("[Service]")
	w("Type=exec")
	w("RuntimeDirectory=%s", path.Base(run))
	w("RuntimeDirectoryMode=0700")
	w("UMask=0077")
	secrets := a.Handed()
	for _, s := range secrets {
		w("LoadCredentialEncrypted=%s:%s", Credential(a.ID, s.Name), a.CredentialFile(s.Name))
	}
	var copies []string
	for _, s := range secrets {
		to := path.Join(run, s.Name)
		w("ExecStartPre=/usr/bin/install -m 0400 -o %d -g %d %%d/%s %s", s.UID, s.UID, Credential(a.ID, s.Name), to)
		copies = append(copies, to)
	}
	if a.SELinux && len(copies) > 0 {
		w("ExecStartPre=/usr/bin/chcon -t container_file_t -l %s %s", a.Level, strings.Join(copies, " "))
	}
	w("ExecStart=%s up --no-log-prefix --quiet-pull --abort-on-container-exit", compose)
	w("# Not into the journal: compose writes the containers' output there, secrets and all; the agent reads the app's")
	w("# logs from the engine, its secrets cut out. systemd still logs the unit's starts, stops and exit statuses.")
	w("StandardOutput=null")
	w("StandardError=null")
	w("ExecStopPost=%s stop", compose)
	w("# Always: compose ends with the stopped container's own exit status, 0 included.")
	w("Restart=always")
	w("# systemctl stop: compose ends with 130 when it stops the containers itself.")
	w("SuccessExitStatus=130")
	w("RestartSec=10s")
	w("TimeoutStopSec=90s")
	w("")
	w("[Install]")
	w("WantedBy=multi-user.target")
	return []byte(b.String())
}

func sortedSecrets(s []Secret) []Secret {
	out := slices.Clone(s)
	slices.SortFunc(out, func(x, y Secret) int { return strings.Compare(x.Name, y.Name) })
	return out
}
