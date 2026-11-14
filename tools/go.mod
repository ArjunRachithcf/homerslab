// The developer tools CI runs, pinned here with their sums so they never join HomersLab's own module graph.
// Run them from the repository root, e.g. `go -C tools tool govulncheck -C .. ./...`.
module homerslab.invalid/tools

go 1.27.4

tool golang.org/x/vuln/cmd/govulncheck

require (
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
)
