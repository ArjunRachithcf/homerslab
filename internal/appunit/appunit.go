// Package appunit is what the agent writes for an app it installs (#19): the app's one systemd unit, which runs the
// standard compose command (ADR 0014), and the second compose file it runs the app's own with, which wires what
// Homeward adds. Both are rendered from the app as the agent keeps it, byte for byte the same each time, so they can
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

	"github.com/ArjunRachithcf/homeward/internal/catalog"
	"github.com/ArjunRachithcf/homeward/internal/engine"
	"github.com/ArjunRachithcf/homeward/internal/storage"
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
	// Mounts are the storage locations the app's services see (#28).
	Mounts []Mount
	// OIDC is the OIDC client of an app that signs people in itself (#20), nil for any other.
	OIDC *OIDC
	// LDAP is the directory of an app that signs people in over LDAP (#204), nil for any other.
	LDAP *LDAP
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

// LDAP is how an app takes its directory at the sign-in (#204): its settings as environment variables of the upstream's
// service. None of them is secret: each person binds with an app password of their own.
type LDAP struct {
	Env map[string]string `json:"env"`
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

// Mount is a storage location one of the app's services sees (#28): its source on this machine (the location's folder,
// or where its share is mounted), the path in the container, and whether the service only reads it. Unit is the share's
// unit, which mounts it; "" for a folder.
type Mount struct {
	Location string `json:"location"`
	Service  string `json:"service"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"readOnly"`
	Unit     string `json:"unit,omitempty"`
}

// Checked says whether the mount is one the agent's files may name: a folder its checks allow, or exactly its share's
// mount and unit, at a plain path in the container. Anything else is written as a bind of a folder that is never there,
// so the app doesn't start without its storage, and nothing of it reaches the file: compose would interpolate a ${...}.
func (m Mount) Checked() bool {
	if !storage.ID.MatchString(m.Location) || !storage.ValidTarget(m.Target) {
		return false
	}
	if m.Unit != "" {
		return m.Source == storage.MountPoint(m.Location) && m.Unit == storage.Unit(m.Location)
	}
	return storage.Allowed(m.Source) == nil
}

// shares are the units of the shares the app's services see, in name order, once each.
func (a App) shares() []Mount {
	var out []Mount
	for _, m := range a.Mounts {
		if m.Unit != "" && m.Checked() && !slices.ContainsFunc(out, func(o Mount) bool { return o.Unit == m.Unit }) {
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(x, y Mount) int { return strings.Compare(x.Unit, y.Unit) })
	return out
}

// folders are the folders of the machine the mounts bind, each once, in order: only checked ones, whose paths carry
// nothing a unit's line would read as its own (no quote, backslash, specifier or variable: storage.Allowed).
func folders(mounts []Mount) []string {
	var out []string
	for _, m := range mounts {
		if m.Checked() && m.Unit == "" { // a share's mount is root's own folder, which its unit waits for
			out = append(out, m.Source)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// RefusedMount is the bind a mount no check passed is written as: a folder that is never there, so the engine refuses
// to start the app, rather than start it without its storage.
const RefusedMount = "/nonexistent/homeward-refused-mount"

// ID is an app's id as the agent takes it: what a unit, a project and a runtime directory can be named after.
var ID = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// PlanID is an install plan's id, the one thing of a plan the files name besides the app's id.
var PlanID = regexp.MustCompile(`^p-[0-9a-f]{32}$`)

// SecretName is a secret's name, as a file and a credential can carry it.
var SecretName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,62}[a-z0-9])?$`)

// Unit is the app's unit's name.
func Unit(id string) string { return "homeward-app-" + id + ".service" }

// Project is the app's compose project's name.
func Project(id string) string { return engine.ProjectPrefix + id }

// RunDir is the unit's runtime directory, where it puts the files its containers mount: 0700, gone when it stops.
func RunDir(id string) string { return "/run/homeward-app-" + id }

// Credential is a secret's credential ID, which is also the name sealed inside it: <app>.<name>, so one app's file
// can't stand in for another's (ADR 0011).
func Credential(id, name string) string { return id + "." + name }

// CredentialFile is where the agent keeps a secret's sealed credential.
func (a App) CredentialFile(name string) string { return path.Join(a.Dir, "credentials", name+".cred") }

// ComposeFile is the catalog's compose file, as it is; OverrideFile the agent's, which Override renders.
func (a App) ComposeFile() string  { return path.Join(a.Dir, "compose.yaml") }
func (a App) OverrideFile() string { return path.Join(a.Dir, "homeward.yaml") }

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
	w("# Written by Homeward's agent, from install plan %s. Never edit by hand.", plan(a.Plan))
	w("# What Homeward adds to app %s's own compose file, as the second file of the standard compose command (ADR 0014):", id(a))
	w("#   - every service: no restart policy, the engine's nor swarm's (the app's unit restarts the app), at the app's own")
	w("#     SELinux level;")
	w("#   - the app's one upstream, a fixed address on a network of its own, which only the edge and the agent may open;")
	w("#   - every network's bridge named by the agent, so the firewall table knows it before it is made;")
	w("#   - the sign-in's name, at the sign-in listener's address: the one thing of this machine's the app may open;")
	w("#   - its secrets, as files its unit copies from the credentials the agent sealed;")
	w("#   - its storage locations, each a bind of the location's folder, read-only unless the plan says otherwise; a")
	w("#     service that writes to one can't make a device;")
	w("#   - for an app that signs people in itself, its OIDC client's settings, and the client's secret as a file.")
	if a.LDAP != nil {
		w("#   - for an app that signs people in over LDAP, its directory's settings.")
	}
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
		mounts := slices.DeleteFunc(slices.Clone(a.Mounts), func(m Mount) bool { return m.Service != s.Name })
		for i, m := range mounts {
			if !m.Checked() {
				mounts[i] = Mount{Source: RefusedMount, Target: RefusedMount, ReadOnly: true}
			}
		}
		slices.SortFunc(mounts, func(x, y Mount) int { return strings.Compare(x.Target, y.Target) })
		if len(mounts) > 0 {
			w("    volumes:")
		}
		for _, m := range mounts {
			w("      - type: \"bind\"")
			w("        source: %s", q(m.Source))
			w("        target: %s", q(m.Target))
			w("        read_only: %t", m.ReadOnly)
			w("        bind:")
			w("          create_host_path: false")
		}
		if slices.ContainsFunc(mounts, func(m Mount) bool { return !m.ReadOnly }) {
			// What it writes to a location stays on the machine: no device among it (the location's filesystem is
			// nodev and nosuid besides, which the agent checked).
			w("    cap_drop:")
			w("      - \"MKNOD\"")
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
		} else if a.LDAP != nil {
			w("    environment:")
			for _, k := range slices.Sorted(maps.Keys(a.LDAP.Env)) {
				w("      %s: %s", q(k), q(a.LDAP.Env[k]))
			}
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

// Compose is the standard compose command on the catalog's file and the agent's, as the app's unit runs it, and the
// agent too (a dump's command runs in the app's service through it).
func (a App) Compose() []string {
	return []string{"/usr/bin/docker", "compose", "--project-name", Project(a.ID), "--project-directory", a.Dir,
		"--file", a.ComposeFile(), "--file", a.OverrideFile()}
}

// UnitFile is the app's systemd unit: the standard compose command on the catalog's file and the agent's, with the app's
// credentials.
func UnitFile(a App) []byte {
	var b strings.Builder
	w := func(f string, args ...any) { fmt.Fprintf(&b, f+"\n", args...) }
	run := RunDir(a.ID)
	compose := strings.Join(a.Compose(), " ")
	// No free text: the app's id and the plan's only, which no line of a unit can be ended or continued by.
	w("# Written by Homeward's agent, from install plan %s. Never edit by hand.", plan(a.Plan))
	w("# App %s, run by the standard compose command on its catalog's compose file and the agent's (ADR 0014).", id(a))
	w("# systemd owns the containers: their restart policies are off, and this unit restarts them; a container that stops")
	w("# ends the compose command, which stops the others. The app's secrets are credentials the agent sealed (ADR 0011),")
	w("# copied into the runtime directory for the one container user that reads each, at the app's own SELinux level.")
	w("# No mount sandboxing: Docker's daemon must see that directory.")
	w("[Unit]")
	w("Description=Homeward app %s", id(a))
	w("Documentation=https://github.com/ArjunRachithcf/homeward")
	shares := a.shares()
	var units string
	for _, m := range shares {
		units += " " + m.Unit
	}
	w("Requires=docker.service")
	w("After=docker.service network-online.target homeward-agent.service%s", units)
	w("Wants=network-online.target%s", units)
	if units != "" {
		// Stopped and restarted with its shares, so it never runs on a mount that went away.
		w("PartOf=%s", units[1:])
	}
	w("")
	w("[Service]")
	w("Type=exec")
	w("RuntimeDirectory=%s", path.Base(run))
	w("RuntimeDirectoryMode=0700")
	w("UMask=0077")
	for _, p := range a.startProperties() {
		w("%s", p)
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

// secretProperties are the unit's lines that hand the app's secrets to its containers: each credential loaded, copied
// into the runtime directory for the one container user that reads it, at the app's own SELinux level.
func (a App) secretProperties() []string {
	var out, copies []string
	secrets := a.Handed()
	for _, s := range secrets {
		out = append(out, fmt.Sprintf("LoadCredentialEncrypted=%s:%s", Credential(a.ID, s.Name), a.CredentialFile(s.Name)))
	}
	for _, s := range secrets {
		to := path.Join(RunDir(a.ID), s.Name)
		out = append(out, fmt.Sprintf("ExecStartPre=/usr/bin/install -m 0400 -o %d -g %d %%d/%s %s", s.UID, s.UID, Credential(a.ID, s.Name), to))
		copies = append(copies, to)
	}
	if a.SELinux && len(copies) > 0 {
		out = append(out, fmt.Sprintf("ExecStartPre=/usr/bin/chcon -t container_file_t -l %s %s", a.Level, strings.Join(copies, " ")))
	}
	return out
}

// startProperties are the unit's lines before its start: each share mounted, the secrets, and each storage location's
// folder checked again.
func (a App) startProperties() []string {
	var out []string
	// A share's unit retries until the mesh reaches it; the app starts only once it is mounted, never on the empty folder
	// under it.
	for _, m := range a.shares() {
		out = append(out, "ExecStartPre=/usr/bin/mountpoint -q "+m.Source)
	}
	out = append(out, a.secretProperties()...)
	for _, p := range folders(a.Mounts) {
		// Before every start: the folder is still one, reached without a symlink through folders only root can change.
		out = append(out, "ExecStartPre=/usr/bin/homeward agent check-storage "+strconv.Quote(p))
	}
	return out
}

// ServicesRun is systemd-run's arguments for running only some of the app's services, as its unit runs them all (the
// same compose command, the same secrets in the same runtime directory), while the app's own unit is stopped: a
// restore hands a dump to its database with nothing else of the app running (#22): storage locations' folders are
// checked before the start as the unit checks them. Its unit's name is unit.
func (a App) ServicesRun(unit string, services []string) []string {
	args := []string{"--unit=" + unit, "--quiet", "--collect", "--service-type=exec",
		"-p", "RuntimeDirectory=" + path.Base(RunDir(a.ID)), "-p", "RuntimeDirectoryMode=0700", "-p", "UMask=0077",
		"-p", "StandardOutput=null", "-p", "StandardError=null", "-p", "Requires=docker.service", "-p", "After=docker.service"}
	// Its shares mounted first, as for the app's unit: a database never starts on the empty folder under a share.
	for _, m := range a.shares() {
		args = append(args, "-p", "Requires="+m.Unit, "-p", "After="+m.Unit)
	}
	for _, p := range a.startProperties() {
		args = append(args, "-p", p)
	}
	args = append(args, "-p", "ExecStopPost="+strings.Join(append(a.Compose(), "stop"), " "), "--")
	args = append(args, a.Compose()...)
	return append(append(args, "up", "--no-log-prefix", "--no-deps", "--abort-on-container-exit"), services...)
}

func sortedSecrets(s []Secret) []Secret {
	out := slices.Clone(s)
	slices.SortFunc(out, func(x, y Secret) int { return strings.Compare(x.Name, y.Name) })
	return out
}
