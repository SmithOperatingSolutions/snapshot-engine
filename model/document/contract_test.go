package document_test

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

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
)

// serialize is a collection's content in a canonical text form: one line per
// record in id order, the id in hex and the record's canonical text.
func serialize(records map[string]merge.Node) []byte {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b bytes.Buffer
	for _, id := range ids {
		fmt.Fprintf(&b, "%s\t%s\n", hex.EncodeToString([]byte(id)), document.Encode(records[id]))
	}
	return b.Bytes()
}

func parse(t *testing.T, b []byte) map[string]merge.Node {
	t.Helper()
	records := map[string]merge.Node{}
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 2)
		id, err := hex.DecodeString(f[0])
		if err != nil {
			t.Fatal(err)
		}
		records[string(id)] = doc(t, f[1])
	}
	return records
}

func generate(t *testing.T, seed uint64) map[string]merge.Node {
	records := map[string]merge.Node{}
	for i := 0; i < int(seed%20)+3; i++ {
		records[fmt.Sprintf("rec:%d:%d", seed%5, i)] = doc(t, fmt.Sprintf(`{"i": %d, "seed": %d, "tags": ["a", "b"], "nested": {"deep": true}}`, i, seed))
	}
	return records
}

func idsOf(records map[string]merge.Node) []string {
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// set returns the record with one field set to a value.
func set(n merge.Node, field string, v merge.Node) merge.Node {
	fields := make([]merge.Field, 0, len(n.Fields)+1)
	for _, f := range n.Fields {
		if f.Name != field {
			fields = append(fields, f)
		}
	}
	return merge.Obj(append(fields, merge.Field{Name: field, Value: v})...)
}

func subject(t *testing.T) contract.Subject {
	s := memstore.New()
	return contract.Subject{
		Model:    document.Model{Config: cfg()},
		Store:    s,
		Generate: func(seed uint64) []byte { return serialize(generate(t, seed)) },
		Mutate: func(c []byte, seed uint64) []byte { // a record added, a record's field edited
			records := parse(t, c)
			records[fmt.Sprintf("new:%d", seed)] = doc(t, fmt.Sprintf(`{"new": %d}`, seed))
			first := idsOf(records)[0]
			records[first] = set(records[first], "edited", merge.Num(fmt.Sprint(seed)))
			return serialize(records)
		},
		Collide: func(c []byte, seed uint64) (ours, theirs []byte) { // one record, one field, two values
			o, th := parse(t, c), parse(t, c)
			first := idsOf(o)[0]
			o[first] = set(o[first], "i", merge.Str(fmt.Sprintf("ours %d", seed)))
			th[first] = set(th[first], "i", merge.Str(fmt.Sprintf("theirs %d", seed)))
			return serialize(o), serialize(th)
		},
		Write: func(t *testing.T, c []byte) model.Root {
			t.Helper()
			r, err := document.Write(ctx, s, cfg(), parse(t, c))
			if err != nil {
				t.Fatal(err)
			}
			return r
		},
		Read: func(t *testing.T, r model.Root) []byte {
			t.Helper()
			records, err := document.Read(ctx, s, cfg(), r)
			if err != nil {
				t.Fatal(err)
			}
			return serialize(records)
		},
	}
}

// Storage Core Spec: every registered model passes model/contract, the
// collision case included.
func TestContract(t *testing.T) { contract.Run(t, subject) }
