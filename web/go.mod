// Not Go. This file marks web/ as outside the HomersLab module, so `go test ./...` and `go vet ./...` never walk
// into node_modules, where npm packages may ship Go code of their own.
module homerslab.invalid/web
