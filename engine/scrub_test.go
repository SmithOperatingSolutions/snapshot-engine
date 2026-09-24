package engine_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/engine"
)

// scrubLog is a Logger that keeps what it is told.
type scrubLog struct {
	mu      sync.Mutex
	entries map[string]string // correlation -> detail
}

func (l *scrubLog) Log(_ context.Context, correlation, detail string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.entries == nil {
		l.entries = map[string]string{}
	}
	l.entries[correlation] = detail
}

// Engine Spec, security: errors to callers are generic; the details go to
// the log under a correlation id the caller's error carries. An error that
// names a key or a value reaches the caller as its engine error alone, with
// the correlation id; the logger gets the details under that id; errors.Is
// still matches the engine error; the core's error is not reachable from
// what the caller holds. An error the engine does not know becomes
// ErrInternal the same way; a cancelled context stays itself.
func TestErrorsToCallersAreScrubbed(t *testing.T) {
	log := &scrubLog{}
	secret := fmt.Errorf("%w (kv: key %q holds %q)", engine.ErrInvalid, "customer:4411", "card 4242")
	got := engine.Scrub(ctx, log, secret)
	if !errors.Is(got, engine.ErrInvalid) {
		t.Fatalf("the scrubbed error %v does not match ErrInvalid", got)
	}
	var e *engine.Error
	if !errors.As(got, &e) || e.Correlation == "" {
		t.Fatalf("the scrubbed error %v carries no correlation id", got)
	}
	if strings.Contains(got.Error(), "customer") || strings.Contains(got.Error(), "4242") {
		t.Errorf("the caller's error %q carries a key or a value", got)
	}
	if !strings.Contains(got.Error(), e.Correlation) {
		t.Errorf("the caller's error %q does not show its correlation id %s", got, e.Correlation)
	}
	if d := log.entries[e.Correlation]; !strings.Contains(d, "customer:4411") {
		t.Errorf("the log under %s holds %q, want the details", e.Correlation, d)
	}
	if errors.Unwrap(got) != engine.ErrInvalid || errors.Unwrap(errors.Unwrap(got)) != nil {
		t.Errorf("the caller's error unwraps past the engine error: the details are reachable")
	}

	other := engine.Scrub(ctx, log, errors.New("packstore: pack 9f3a… is missing"))
	if !errors.Is(other, engine.ErrInternal) || strings.Contains(other.Error(), "pack") {
		t.Errorf("an error the engine does not know reached the caller as %q, want ErrInternal alone", other)
	}
	var e2 *engine.Error
	if errors.As(other, &e2) && e2.Correlation == e.Correlation {
		t.Error("two errors share a correlation id")
	}

	if got := engine.Scrub(ctx, log, context.Canceled); !errors.Is(got, context.Canceled) {
		t.Errorf("a cancelled context reached the caller as %v, want context.Canceled", got)
	}
	if engine.Scrub(ctx, log, nil) != nil {
		t.Error("no error became one")
	}
	if got := engine.Scrub(ctx, nil, secret); !errors.Is(got, engine.ErrInvalid) || strings.Contains(got.Error(), "customer") {
		t.Errorf("with no logger the error is %q, want the engine error alone", got)
	}
}

// An error scrubbed twice, as happens when one exported call returns
// another's error, keeps its first correlation id and is logged once.
func TestAScrubbedErrorIsNotScrubbedAgain(t *testing.T) {
	log := &scrubLog{}
	once := engine.Scrub(ctx, log, fmt.Errorf("%w: details", engine.ErrNotFound))
	twice := engine.Scrub(ctx, log, once)
	var a, b *engine.Error
	if !errors.As(once, &a) || !errors.As(twice, &b) || a.Correlation != b.Correlation {
		t.Errorf("scrubbing a scrubbed error changed its correlation id: %v, then %v", once, twice)
	}
	if len(log.entries) != 1 {
		t.Errorf("a doubly scrubbed error was logged %d times, want once", len(log.entries))
	}
}
