package document_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-core/core/chunk"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
)

// doc parses JSON in a test, failing on what is not a document.
func doc(t *testing.T, text string) merge.Node {
	t.Helper()
	n, err := document.Parse([]byte(text))
	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}
	return n
}

// A record is stored as the format byte then its canonical text, and decodes
// back to the same value.
func TestARecordFrameRoundTrips(t *testing.T) {
	n := doc(t, `{"name": "ada", "tags": ["a", "b"], "n": 1.50}`)
	frame, err := document.EncodeRecord(n)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]byte{document.Format}, []byte(`{"n":1.5,"name":"ada","tags":["a","b"]}`)...)
	if !bytes.Equal(frame, want) {
		t.Errorf("frame = %q, want %q", frame, want)
	}
	back, err := document.DecodeRecord(frame)
	if err != nil {
		t.Fatalf("DecodeRecord: %v", err)
	}
	if !back.Equal(n) {
		t.Errorf("the frame decoded to %s, want %s", back.Canonical(), n.Canonical())
	}
}

// A stored record that is not one is refused as corrupt: no frame, another
// format, text that is not a document, text that is a document but not the
// canonical one (so equal values would not be equal bytes), bytes after the
// text. A node that is not a document is refused before it is stored.
func TestAFrameThatIsNotARecordIsRefused(t *testing.T) {
	good, err := document.EncodeRecord(doc(t, `{"a": 1}`))
	if err != nil || len(good) == 0 {
		t.Fatalf("positive control: %v", err)
	}
	for name, frame := range map[string][]byte{
		"nothing":               {},
		"another format":        append([]byte{2}, good[1:]...),
		"not a document":        append([]byte{document.Format}, `{"a": `...),
		"fields out of order":   append([]byte{document.Format}, `{"b":1,"a":2}`...),
		"a number spelled long": append([]byte{document.Format}, `{"a":1.0}`...),
		"whitespace":            append([]byte{document.Format}, `{"a": 1}`...),
		"bytes after the text":  append(good, ' '),
	} {
		if _, err := document.DecodeRecord(frame); !errors.Is(err, chunk.ErrCorrupt) {
			t.Errorf("%s: DecodeRecord = %v, want ErrCorrupt", name, err)
		}
	}
	deep := merge.Arr()
	for range document.MaxDepth + 1 {
		deep = merge.Arr(deep)
	}
	for name, n := range map[string]merge.Node{
		"a number that is not canonical": merge.Obj(merge.Field{Name: "a", Value: merge.Num("1.0")}),
		"a number that is not a number":  merge.Num("abc"),
		"nesting past the limit":         deep,
		"a string that is not UTF-8":     merge.Str("\xff"),
		"a field twice":                  {Kind: merge.Object, Fields: []merge.Field{{Name: "a", Value: merge.Num("1")}, {Name: "a", Value: merge.Num("2")}}},
		"fields out of order":            {Kind: merge.Object, Fields: []merge.Field{{Name: "b", Value: merge.Num("1")}, {Name: "a", Value: merge.Num("2")}}},
		"text over the limit":            merge.Str(strings.Repeat("x", document.MaxDocument)),
	} {
		if _, err := document.EncodeRecord(n); !errors.Is(err, document.ErrDocument) {
			t.Errorf("%s: EncodeRecord = %v, want ErrDocument", name, err)
		}
	}
}

// Every frame the decoder takes encodes back to the same bytes; nothing it
// refuses panics.
func FuzzDecodeRecord(f *testing.F) {
	f.Add(append([]byte{document.Format}, `{"a":[1,"b",null]}`...))
	f.Add([]byte{document.Format, '0'})
	f.Add([]byte{2, '0'})
	f.Fuzz(func(t *testing.T, frame []byte) {
		n, err := document.DecodeRecord(frame)
		if err != nil {
			return
		}
		again, err := document.EncodeRecord(n)
		if err != nil || !bytes.Equal(again, frame) {
			t.Fatalf("a frame the decoder took does not encode back to itself (%v)", err)
		}
	})
}
