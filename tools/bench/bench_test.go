package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestSmoke runs every workload at smoke scale on both backends. It asserts
// no timings: only that the tool runs to the end, that every workload
// reports, and that nothing failed along the way.
func TestSmoke(t *testing.T) {
	if testing.Short() {
		t.Log("short mode: running the memory backend only")
	}
	cases := [][]string{{"-scale", "smoke", "-backend", "mem"}}
	if !testing.Short() {
		cases = append(cases, []string{"-scale", "smoke", "-backend", "disk", "-dir", t.TempDir()})
	}
	for _, args := range cases {
		var out bytes.Buffer
		if code := run(args, &out); code != 0 {
			t.Fatalf("bench %v exited %d, so an operator following docs/PERFORMANCE.md gets no baseline:\n%s", args, code, out.String())
		}
		got := out.String()
		for _, w := range []string{"W1", "W2", "W3", "W4", "W5", "W6", "W7", "W8"} {
			if !strings.Contains(got, "\n"+w+" ") {
				t.Errorf("bench %v printed no %s result, so that workload silently measured nothing:\n%s", args, w, got)
			}
		}
		if strings.Contains(got, "first error:") {
			t.Errorf("bench %v hit an unexpected error under smoke load (a failure an application would see):\n%s", args, got)
		}
	}
}
