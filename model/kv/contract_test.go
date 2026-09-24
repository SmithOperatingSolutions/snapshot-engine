package kv_test

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk/memstore"
	"github.com/SmithOperatingSolutions/snapshot-core/core/model"
	"github.com/SmithOperatingSolutions/snapshot-core/model/contract"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

// serialize is an object's content in a canonical text form: one line per
// entry in key order, the key and the value's frame in hex. A frame is the
// value's one spelling, so the text is canonical for every kind.
func serialize(es map[string]kv.Value) []byte {
	keys := make([]string, 0, len(es))
	for k := range es {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for _, k := range keys {
		f, err := kv.EncodeValue(es[k])
		if err != nil {
			panic(err) // the fixtures are values
		}
		fmt.Fprintf(&b, "%s\t%s\n", hex.EncodeToString([]byte(k)), hex.EncodeToString(f))
	}
	return b.Bytes()
}

func parse(t *testing.T, b []byte) map[string]kv.Value {
	t.Helper()
	es := map[string]kv.Value{}
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 2)
		k, err := hex.DecodeString(f[0])
		if err != nil {
			t.Fatal(err)
		}
		frame, err := hex.DecodeString(f[1])
		if err != nil {
			t.Fatal(err)
		}
		v, err := kv.DecodeValue(frame)
		if err != nil {
			t.Fatal(err)
		}
		es[string(k)] = v
	}
	return es
}

// generate is an object holding every kind: bytes, a counter, a set, a
// hash, a sorted set and a sequence, all a pure function of seed.
func generate(seed uint64) map[string]kv.Value {
	es := map[string]kv.Value{}
	for i := 0; i < int(seed%30)+3; i++ {
		es[fmt.Sprintf("key:%d:%d", seed%5, i)] = bytesValue(fmt.Sprintf("value %d/%d", seed, i))
	}
	es["visits"] = counter(int64(seed)*7 - 3)
	es["tags"] = set(member(fmt.Sprintf("t%d", seed), seed+1), member("common", 1), member("common", seed+2))
	es["user"] = fields("name", fmt.Sprintf("u%d", seed), "mail", "u@x", "n", fmt.Sprint(seed))
	es["board"] = zset(scored("ann", float64(seed%7), 1), scored("bob", 2.5, 2), scored(fmt.Sprintf("p%d", seed), -1, seed+3))
	es["queue"] = seq("a", fmt.Sprintf("job%d", seed), "z")
	return es
}

func subject(t *testing.T) contract.Subject {
	s := memstore.New()
	return contract.Subject{
		Model:    kv.Model{Config: cfg()},
		Store:    s,
		Generate: func(seed uint64) []byte { return serialize(generate(seed)) },
		Mutate: func(c []byte, seed uint64) []byte {
			es := parse(t, c)
			es[fmt.Sprintf("new:%d", seed)] = bytesValue(fmt.Sprintf("new %d", seed))
			keys := keysOf(es)
			sort.Strings(keys)
			es[keys[0]] = bytesValue(fmt.Sprintf("edited %d", seed))
			return serialize(es)
		},
		Collide: func(c []byte, seed uint64) (ours, theirs []byte) { // one key, two values
			o, th := parse(t, c), parse(t, c)
			keys := keysOf(o)
			sort.Strings(keys)
			o[keys[0]], th[keys[0]] = bytesValue(fmt.Sprintf("ours %d", seed)), bytesValue(fmt.Sprintf("theirs %d", seed))
			return serialize(o), serialize(th)
		},
		Write: func(t *testing.T, c []byte) model.Root {
			t.Helper()
			r, err := kv.Write(ctx, s, cfg(), parse(t, c))
			if err != nil {
				t.Fatal(err)
			}
			return r
		},
		Read: func(t *testing.T, r model.Root) []byte {
			t.Helper()
			es, err := kv.Read(ctx, s, cfg(), r)
			if err != nil {
				t.Fatal(err)
			}
			return serialize(es)
		},
	}
}

// Storage Core Spec: every registered model passes model/contract; this is
// the first model built outside the core.
func TestContract(t *testing.T) { contract.Run(t, subject) }
