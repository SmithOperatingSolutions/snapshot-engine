package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SmithOperatingSolutions/snapshot-core/core/repo"
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

// Collect reclaims what the database no longer reaches (the core's garbage
// collection): it marks every chunk a branch, a tag, a working set, a merge
// in progress or a commit in history reaches; condemns the packs none of
// whose chunks is reached; deletes a pack, and the index objects that list
// it, once it has stayed unreached for a grace window (DefaultGrace when
// grace is 0); rewrites packs that are mostly dead; and compacts the index
// objects every publish adds. It is the one operation that deletes, and
// takes Admin on the database.
//
// It runs beside open sessions and transactions, in this process or
// another. What a transaction reads stays readable for the grace window.
// A transaction whose unpublished write counted on data the database had
// stopped reaching, which a collection then deleted, cannot publish: its
// commit is ErrSessionLost and the database refuses every further write
// until it is opened again. Only work that outlives the grace window can
// meet that, so the window must be longer than any transaction or session
// is left open.
func (d *Database) Collect(ctx context.Context, p Principal, grace time.Duration) (_ CollectReport, err error) {
	defer d.scrubInto(ctx, &err)
	if d.isClosed() {
		return CollectReport{}, ErrClosed
	}
	if grace < 0 {
		return CollectReport{}, fmt.Errorf("%w: a grace window of %v", ErrInvalid, grace)
	}
	if grace == 0 {
		grace = DefaultGrace
	}
	rep, err := repo.GC(ctx, p, d.o.repo(d.models), grace)
	if err != nil {
		return CollectReport{}, translate(err)
	}
	out := CollectReport{Rounds: rep.Rounds, Live: rep.Live, Condemned: rep.Condemned, Reprieved: rep.Reprieved, Repacked: rep.Repacked, Copied: rep.Copied}
	for _, name := range rep.Deleted {
		switch {
		case strings.HasPrefix(name, "packs/"):
			out.DeletedPacks++
		case strings.HasPrefix(name, "index/"):
			out.DeletedIndexes++
		}
	}
	return out, nil
}
