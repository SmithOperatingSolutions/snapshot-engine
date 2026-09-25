//go:build !race

package kv_test

// raceScale stretches a timing guard's ceiling under the race detector.
const raceScale = 1
