package main

import (
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// The swap counter keeps the backend's commit journal reachable: the engine
// finds the journal by type, so a counter that hid it would measure every
// commit as a full publish (as the first v0.3.0 disk run did).
func TestTheSwapCounterKeepsTheBackendsJournal(t *testing.T) {
	disk, err := engine.DiskBlobs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := disk.(engine.Journaler); !ok {
		t.Fatal("fixture: the disk backend keeps no journal")
	}
	wrapped, counter := counting(disk)
	j, ok := wrapped.(engine.Journaler)
	if !ok {
		t.Fatal("the counted disk backend is not a Journaler: the engine would publish every commit in full and the bench would measure a journal-less core")
	}
	if !j.JournalByDefault() {
		t.Error("the counted disk backend does not journal by default, though the backend it wraps does")
	}
	if _, ok := any(counter).(engine.Journaler); ok {
		t.Error("positive control: the bare counter would itself pass as a Journaler, so the test proves nothing")
	}
	// The memory backend keeps a journal too but not by default (core
	// D16): the counter passes both facts through unchanged.
	raw := engine.MemoryBlobs()
	mem, _ := counting(raw)
	rj, rawIs := raw.(engine.Journaler)
	mj, wrappedIs := mem.(engine.Journaler)
	if rawIs != wrappedIs {
		t.Fatalf("the counted memory backend is a Journaler: %t, the backend it wraps: %t", wrappedIs, rawIs)
	}
	if rawIs && rj.JournalByDefault() != mj.JournalByDefault() {
		t.Errorf("the counted memory backend journals by default: %t, the backend it wraps: %t", mj.JournalByDefault(), rj.JournalByDefault())
	}
}
