// Package engine is what HomersLab reads of an app's compose project, in its own words. Only the container-engine
// adapter (engine/compose) imports the Compose SDK; everything else reads these types.
package engine

// ProjectPrefix starts every compose project name HomersLab uses, homerslab-<app>: the namespace it reserves, because
// Compose claims a container by its labels alone (ADR 0014).
const ProjectPrefix = "homerslab-"

// Project is a compose project as it would run: its name, and its services in name order.
type Project struct {
	Name     string    `json:"name"`
	Services []Service `json:"services"`
}

// Service is one service of a project and the image it runs.
type Service struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}
