package document_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
	"github.com/SmithOperatingSolutions/snapshot-engine/model/document"
)

// JSON parses to a node and back to one canonical text: fields sorted,
// whitespace gone, every number in one spelling, every string escaped one
// way. Equal values, however written, are equal bytes.
func TestJSONParsesToOneCanonicalText(t *testing.T) {
	for given, want := range map[string]string{
		`{"b": 1, "a": [true, false, null]}`:         `{"a":[true,false,null],"b":1}`,
		"  {\n\t\"z\" : \"tab\\tquote\\\"\" }\n":     `{"z":"tab\tquote\""}`,
		`{"n": [1.0, 1e2, -0, 0.5e1, 12.340, 1E-2]}`: `{"n":[1,100,0,5,12.34,0.01]}`,
		`"é😀\u0001"`:            `"é😀\u0001"`,
		`[{}, [], "", 0]`:       `[{},[],"",0]`,
		`-12345678901234567890`: `-12345678901234567890`,
		`1e3`:                   `1000`,
		`{"nested": {"deep": {"er": [1, {"x": "y"}]}}}`: `{"nested":{"deep":{"er":[1,{"x":"y"}]}}}`,
	} {
		n, err := document.Parse([]byte(given))
		if err != nil {
			t.Errorf("Parse(%q): %v", given, err)
			continue
		}
		if got := string(document.Encode(n)); got != want {
			t.Errorf("Parse(%q) encodes to %q, want %q", given, got, want)
		}
		again, err := document.Parse(document.Encode(n))
		if err != nil || !again.Equal(n) {
			t.Errorf("the canonical text of %q does not parse back to the same value (%v)", given, err)
		}
	}
	n, err := document.Parse([]byte(`{"a": 1, "b": "two"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := merge.Obj(merge.Field{Name: "a", Value: merge.Num("1")}, merge.Field{Name: "b", Value: merge.Str("two")})
	if !n.Equal(want) {
		t.Errorf("Parse gave %s, want %s", n.Canonical(), want.Canonical())
	}
}

// What is not a document is refused with ErrDocument and no value:
// malformed text, trailing bytes, a field twice in one object, a number in
// a form JSON does not allow, an escape that is not one, a lone surrogate,
// invalid UTF-8, a control character in a string, text over the limits.
func TestWhatIsNotADocumentIsRefused(t *testing.T) {
	if _, err := document.Parse([]byte(`{"ok": true}`)); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	deep := strings.Repeat("[", document.MaxDepth+1) + strings.Repeat("]", document.MaxDepth+1)
	big := `"` + strings.Repeat("x", document.MaxDocument) + `"`
	for name, text := range map[string]string{
		"nothing":                              ``,
		"only whitespace":                      `   `,
		"an unclosed object":                   `{"a": 1`,
		"trailing bytes":                       `{"a": 1} x`,
		"two values":                           `1 2`,
		"a field twice":                        `{"a": 1, "a": 2}`,
		"a bare word":                          `nope`,
		"a leading zero":                       `01`,
		"a trailing point":                     `1.`,
		"a leading point":                      `.5`,
		"a plus sign":                          `+1`,
		"a hex number":                         `0x10`,
		"NaN":                                  `NaN`,
		"an exponent without digits":           `1e`,
		"an exponent too large":                `1e100000000`,
		"an unknown escape":                    `"\q"`,
		"a short unicode escape":               `"\u12"`,
		"a lone high surrogate":                `"\ud83d"`,
		"a lone low surrogate":                 `"\ude00"`,
		"a high surrogate then a plain escape": `"\ud83d\u0041"`,
		"invalid UTF-8":                        "\"\xff\"",
		"a control character":                  "\"a\x01b\"",
		"a single-quoted string":               `'a'`,
		"a trailing comma":                     `[1,]`,
		"a comment":                            `{"a": 1 /* c */}`,
		"too deep":                             deep,
		"too big":                              big,
		"a number with too many digits":        `1e1001`,
		"a number with too many places":        `1e-1001`,
	} {
		n, err := document.Parse([]byte(text))
		if !errors.Is(err, document.ErrDocument) {
			t.Errorf("%s: Parse = %v (%s), want ErrDocument", name, err, n.Canonical())
		}
	}
}

// Encode is the one text of a value: strings escape the JSON way, control
// characters as \u00XX; nothing else is escaped.
func TestEncodeIsTheOneText(t *testing.T) {
	n := merge.Obj(
		merge.Field{Name: "s", Value: merge.Str("quote \" backslash \\ newline \n tab \t bell \x07 del \x7f é €")},
		merge.Field{Name: "e", Value: merge.Arr()},
	)
	want := `{"e":[],"s":"quote \" backslash \\ newline \n tab \t bell \u0007 del ` + "\x7f é €" + `"}`
	if got := string(document.Encode(n)); got != want {
		t.Errorf("Encode = %q, want %q", got, want)
	}
	back, err := document.Parse(document.Encode(n))
	if err != nil || !back.Equal(n) {
		t.Errorf("Encode's text does not parse back: %v", err)
	}
	if !bytes.Equal(document.Encode(merge.Num("0")), []byte("0")) {
		t.Error("the number zero encodes as something other than 0")
	}
}

// Every text the parser takes encodes to a text the parser takes again, to
// the same value; nothing it refuses panics.
func FuzzParse(f *testing.F) {
	for _, s := range []string{`{"a":[1,2.5,-3e2,"x\u0000",true,null,{}]}`, `"😀"`, `[[[[]]]]`, `0`, `-0.0`, `1e-5`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, text []byte) {
		n, err := document.Parse(text)
		if err != nil {
			return
		}
		enc := document.Encode(n)
		if len(enc) > document.MaxDocument {
			t.Fatalf("%d bytes of input encoded to %d bytes, over MaxDocument", len(text), len(enc))
		}
		again, err := document.Parse(enc)
		if err != nil {
			t.Fatalf("the canonical text of an accepted document is refused: %v", err)
		}
		if !again.Equal(n) || !bytes.Equal(document.Encode(again), enc) {
			t.Fatal("the canonical text is not a fixed point")
		}
	})
}
