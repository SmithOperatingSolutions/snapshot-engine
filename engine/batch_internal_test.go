package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/core/object"
)

// failingBatch is an object's batch whose flush meets the store's fault.
type failingBatch struct{ err error }

func (b failingBatch) check(context.Context, model.Root, model.Root) (any, error) { return nil, nil }
func (b failingBatch) take(any)                                                   {}
func (b failingBatch) flush(context.Context) (model.Root, error)                  { return model.Root{}, b.err }

// A run whose flush fails gives every member it took that error, and no
// other member of the batch: the members it took published nothing, and
// a member left with no error would report a commit that never happened.
func TestARunsFailureIsEveryTakenMembers(t *testing.T) {
	fault := errors.New("injected: the store failed the flush")
	r := &run{refs: map[string]object.Ref{}, batches: map[string]objectBatch{"people": failingBatch{fault}}, members: []int{0, 2}}
	r.refs["people"] = object.Ref{}
	errs := make([]error, 4)
	errs[3] = ErrSerialization // a member that failed on its own, before the run
	var d Database
	r.d = &d
	if _, ok := r.close(context.Background(), errs); ok {
		t.Fatal("a run whose flush fails closed as if it had published")
	}
	for _, i := range r.members {
		if !errors.Is(errs[i], fault) {
			t.Errorf("member %d, taken by the run, has %v, want the flush's fault: it would report a commit that never published", i, errs[i])
		}
	}
	if errs[1] != nil {
		t.Errorf("member 1, not taken by the run, has %v, want none", errs[1])
	}
	if !errors.Is(errs[3], ErrSerialization) {
		t.Errorf("member 3, failed before the run, has %v, want its own error kept", errs[3])
	}
}
