//go:build race

package agent

// raceDetector: the race detector's shadow memory needs more address space than a read's child may have.
const raceDetector = true
