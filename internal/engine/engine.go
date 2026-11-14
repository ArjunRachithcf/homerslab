// Package engine is what HomersLab reads of an app's compose project, in its own words. Only the container-engine
// adapter (engine/compose) imports the Compose SDK; everything else reads these types.
package engine

// ProjectPrefix starts every compose project name HomersLab uses, homerslab-<app>: the namespace it reserves, because
// Compose claims a container by its labels alone (ADR 0014).
const ProjectPrefix = "homerslab-"

// BridgePrefix starts the name of every bridge of an app's networks: the agent names them (hla<slot>u for the
// upstream's, hla<slot>n<i> for the project's own), so the firewall table knows an app's bridge before the engine makes
// it (#82).
const BridgePrefix = "hla"

// Project is a compose project as it would run: its name, its services in name order, the secrets it declares, and the
// networks its services use, in name order (the default one among them when a service uses it).
type Project struct {
	Name     string    `json:"name"`
	Services []Service `json:"services"`
	Secrets  []string  `json:"secrets,omitempty"`
	Networks []Network `json:"networks,omitempty"`
}

// Network is one of a project's networks, with what installing checks before it wires it (#19): the agent names its
// bridge and pins its range, so it must be the project's own bridge, which the project doesn't name or configure
// itself. Joined is another network the project joins instead (external, or named after one that isn't the
// project's own); Driver and Configured are what the project sets of its own (a driver, its options or its addresses).
type Network struct {
	Name       string `json:"name"`
	Joined     string `json:"joined,omitempty"`
	Driver     string `json:"driver,omitempty"`
	Configured bool   `json:"configured,omitempty"`
}

// Service is one service of a project and the image it runs, with what installing checks before it wires it (#19):
// the networks it names (none: the project's default one) or its network mode, the ports it would publish on the host
// (none, by the floor), and the bind mounts' sources it asks the engine to relabel for SELinux (none: a relabelled
// folder would be shared by every container that mounts it), and what it asks for that loosens a container's
// confinement (none, by the floor and until extras are approved: privileged, a capability, a device, its own security
// options, or the machine's or another container's namespaces).
type Service struct {
	Name        string   `json:"name"`
	Image       string   `json:"image"`
	Networks    []string `json:"networks,omitempty"`
	NetworkMode string   `json:"networkMode,omitempty"`
	Published   []string `json:"published,omitempty"`
	Relabelled  []string `json:"relabelled,omitempty"`
	Loosened    []string `json:"loosened,omitempty"`
	// Hosts are the names its extra_hosts give an address to, lowercase and sorted.
	Hosts []string `json:"hosts,omitempty"`
}

// State is a compose project as its engine runs it now: its containers, in name order. None is a project the engine
// has no container of.
type State struct {
	Name       string      `json:"name"`
	Containers []Container `json:"containers"`
}

// Container is one of a project's containers, as the engine reports it: its service, its state (running, exited,
// created, …; anything but running is not running) and its health check's (healthy, unhealthy, starting, or "" for
// none).
type Container struct {
	Service string `json:"service"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Health  string `json:"health,omitempty"`
}

// Mount is a volume, or a folder of the machine, that one of a service's containers has: Destination inside it, Source
// on the machine.
type Mount struct {
	Destination string `json:"destination"`
	Source      string `json:"source"`
}
