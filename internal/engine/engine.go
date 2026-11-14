// Package engine is what HomersLab reads of an app's compose project, in its own words. Only the container-engine
// adapter (engine/compose) imports the Compose SDK; everything else reads these types.
package engine

// ProjectPrefix starts every compose project name HomersLab uses, homerslab-<app>: the namespace it reserves, because
// Compose claims a container by its labels alone (ADR 0014).
const ProjectPrefix = "homerslab-"

// Project is a compose project as it would run: its name, its services in name order, and the secrets it declares.
type Project struct {
	Name     string    `json:"name"`
	Services []Service `json:"services"`
	Secrets  []string  `json:"secrets,omitempty"`
}

// Service is one service of a project and the image it runs, with what installing checks before it wires it (#19):
// the networks it names (none: the project's default one) or its network mode, the ports it would publish on the host
// (none, by the floor), and the bind mounts' sources it asks the engine to relabel for SELinux (none: a relabelled
// folder would be shared by every container that mounts it).
type Service struct {
	Name        string   `json:"name"`
	Image       string   `json:"image"`
	Networks    []string `json:"networks,omitempty"`
	NetworkMode string   `json:"networkMode,omitempty"`
	Published   []string `json:"published,omitempty"`
	Relabelled  []string `json:"relabelled,omitempty"`
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
