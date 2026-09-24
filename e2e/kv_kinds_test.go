package e2e_test

import (
	"bytes"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/blob/mem"
	"github.com/SmithOperatingSolutions/snapshot-core/core/vcs"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

// frame is a value as its one spelling, for comparing values of any kind.
func frame(t *testing.T, v kv.Value) []byte {
	t.Helper()
	f, err := kv.EncodeValue(v)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// E3: a Redis-shaped workload of every kind, edited on two branches and
// merged through a repository: a counter both incremented sums; a set
// added to and removed from keeps the observed-remove rule; a hash's fields
// edited on each side both land; a sorted set with a member added on one
// side and a score raised on the other keeps its order; a queue pushed to
// on both sides holds both pushes in one deterministic order; a bytes
// setting changed on one side alone is that side's.
func TestARedisShapedWorkloadBranchesAndMerges(t *testing.T) {
	h := newHost(t, mem.New())
	h.path = "redis/db0"
	m := func(elem string, tag uint64) kv.Member { return kv.Member{Elem: []byte(elem), Tag: tag} }
	z := func(member string, score float64, tag uint64) kv.Scored {
		return kv.Scored{Member: []byte(member), Score: score, Tag: tag}
	}
	hash := func(kvs ...string) kv.Value {
		v := kv.Value{Kind: kv.Hash, Fields: map[string][]byte{}}
		for i := 0; i+1 < len(kvs); i += 2 {
			v.Fields[kvs[i]] = []byte(kvs[i+1])
		}
		return v
	}
	list := func(es ...string) kv.Value {
		v := kv.Value{Kind: kv.Sequence, Seq: [][]byte{}}
		for _, e := range es {
			v.Seq = append(v.Seq, []byte(e))
		}
		return v
	}
	base := map[string]kv.Value{
		"visits": {Kind: kv.Counter, Counter: 100},
		"tags":   {Kind: kv.Set, Members: []kv.Member{m("go", 1), m("db", 2)}},
		"user:1": hash("name", "ann", "mail", "a@x"),
		"board":  {Kind: kv.SortedSet, Scores: []kv.Scored{z("ann", 10, 1), z("bob", 20, 2)}},
		"queue":  list("j1", "j2"),
		"config": {Kind: kv.Bytes, Bytes: []byte("v1")},
	}
	h.put(vcs.MainBranch, base)
	root := h.commit(vcs.MainBranch, "base")
	if err := h.r.CreateBranch(ctx, me, "feature", root.Hash); err != nil {
		t.Fatal(err)
	}
	main := map[string]kv.Value{
		"visits": {Kind: kv.Counter, Counter: 102},                                                             // +2
		"tags":   {Kind: kv.Set, Members: []kv.Member{m("go", 1), m("db", 2), m("kv", 3)}},                     // adds kv
		"user:1": hash("name", "ann", "mail", "ann@x"),                                                         // changes mail
		"board":  {Kind: kv.SortedSet, Scores: []kv.Scored{z("ann", 10, 1), z("bob", 20, 2), z("cid", 15, 3)}}, // adds cid
		"queue":  list("j1", "j2", "j3"),                                                                       // pushes j3
		"config": {Kind: kv.Bytes, Bytes: []byte("v2")},                                                        // main alone
	}
	h.put(vcs.MainBranch, main)
	h.commit(vcs.MainBranch, "main's edits")
	feature := map[string]kv.Value{
		"visits": {Kind: kv.Counter, Counter: 103},                                            // +3
		"tags":   {Kind: kv.Set, Members: []kv.Member{m("go", 1)}},                            // removes db
		"user:1": hash("name", "ann", "mail", "a@x", "city", "oslo"),                          // adds city
		"board":  {Kind: kv.SortedSet, Scores: []kv.Scored{z("ann", 10, 1), z("bob", 30, 2)}}, // raises bob
		"queue":  list("j1", "j2", "j4"),                                                      // pushes j4
		"config": {Kind: kv.Bytes, Bytes: []byte("v1")},
	}
	h.put("feature", feature)
	fc := h.commit("feature", "feature's edits")
	res, err := h.r.Merge(ctx, me, vcs.MainBranch, fc.Hash)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.Conflicts) != 0 {
		t.Fatalf("the workload conflicted: %+v", res.Conflicts)
	}
	h.commit(vcs.MainBranch, "merge feature")
	got := h.read(vcs.MainBranch)
	want := map[string]kv.Value{
		"visits": {Kind: kv.Counter, Counter: 105},
		"tags":   {Kind: kv.Set, Members: []kv.Member{m("go", 1), m("kv", 3)}},
		"user:1": hash("name", "ann", "mail", "ann@x", "city", "oslo"),
		"board":  {Kind: kv.SortedSet, Scores: []kv.Scored{z("ann", 10, 1), z("cid", 15, 3), z("bob", 30, 2)}},
		"queue":  list("j1", "j2", "j3", "j4"),
		"config": {Kind: kv.Bytes, Bytes: []byte("v2")},
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("%s is missing after the merge", k)
			continue
		}
		if !bytes.Equal(frame(t, g), frame(t, w)) {
			t.Errorf("%s merged to %+v, want %+v", k, g, w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("the merged object holds %d keys, want %d", len(got), len(want))
	}
}
