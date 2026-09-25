package engine

import (
	"context"
	"time"
)

// DefaultGrace is how long unreachable data is kept before Collect deletes
// it when no grace window is given: the Storage Core Spec's default.
const DefaultGrace = 7 * 24 * time.Hour

// CollectReport is what one collection did.
type CollectReport struct {
	Rounds         int   // rounds taken: a writer that publishes during a round starts another
	Live           int   // chunks the database reaches
	Condemned      int   // packs found unreachable, deleted once a grace window has passed
	Reprieved      int   // packs condemned earlier and reachable again
	DeletedPacks   int   // packs deleted
	DeletedIndexes int   // index objects deleted (including the ones compacted away)
	Repacked       int   // packs that were mostly dead, their live chunks copied into new ones
	Copied         int64 // bytes of chunks copied by repacking
}

// Collect reclaims what the database no longer reaches. (Stub.)
func (d *Database) Collect(ctx context.Context, p Principal, grace time.Duration) (CollectReport, error) {
	return CollectReport{}, ErrInternal
}
