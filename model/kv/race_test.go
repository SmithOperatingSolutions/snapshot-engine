//go:build race

package kv_test

// raceScale stretches a timing guard's ceiling under the race detector,
// which runs this package's merges about ten times slower.
const raceScale = 10
