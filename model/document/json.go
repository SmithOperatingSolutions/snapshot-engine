// Package document is the document model (model id 4): a collection of
// records by id, each a JSON-like tree of fields, as one object; a prolly map
// from record id to the record's canonical text behind a versioned frame. A
// record is merged by field path through the merge library: two writers on
// different fields both land, one field changed two ways is a conflict at
// that record naming the field. Input is JSON, parsed by this package's own
// bounded parser and stored canonical: one text per value, fields sorted,
// numbers in one spelling, so equal records are equal bytes.
package document

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/SmithOperatingSolutions/snapshot-engine/merge"
)

// Limits on a document: its text, its nesting and a number's digits. A
// document past any of them is refused before anything is built.
const (
	MaxDocument     = 1 << 20 // bytes of text, as given and as stored
	MaxDepth        = 64      // nested arrays and objects
	MaxNumberDigits = 1000    // digits of a number's canonical text, either side of the point
)

// ErrDocument is JSON that is not a document this model takes: malformed,
// over a limit, a field twice in one object, text that is not UTF-8.
var ErrDocument = errors.New("document: not a document")

// Parse reads one JSON value (RFC 8259) into a merge.Node: strings with every
// escape, numbers as their canonical decimal text, objects with unique
// fields sorted by name. Anything else is ErrDocument, and so is a value
// whose canonical text (Encode) would pass MaxDocument: that is counted as
// the value is read, and a number's text measured before it is built, so a
// document is refused before it grows past the limit (snapshot-engine#6).
// Arrays and objects are allocated once, at the size their text shows.
func Parse(text []byte) (merge.Node, error) {
	if len(text) > MaxDocument {
		return merge.Node{}, fmt.Errorf("%w: %d bytes, the limit is %d", ErrDocument, len(text), MaxDocument)
	}
	if !utf8.Valid(text) {
		return merge.Node{}, fmt.Errorf("%w: not UTF-8", ErrDocument)
	}
	p := &parser{s: text}
	p.space()
	n, err := p.value(0)
	if err != nil {
		return merge.Node{}, err
	}
	p.space()
	if p.i != len(p.s) {
		return merge.Node{}, p.fail("bytes after the value")
	}
	return n, nil
}

type parser struct {
	s   []byte
	i   int
	out int // bytes of canonical text the value read so far encodes to
}

func (p *parser) fail(what string) error {
	return fmt.Errorf("%w: %s at byte %d", ErrDocument, what, p.i)
}

// emit counts n more bytes of canonical text and refuses the document once
// its canonical text passes MaxDocument.
func (p *parser) emit(n int) error {
	if p.out += n; p.out > MaxDocument {
		return p.fail(fmt.Sprintf("a document whose canonical text passes %d bytes", MaxDocument))
	}
	return nil
}

// members counts the values of the array or object whose first value
// starts at p.i, by its top-level commas, so its slice is allocated once at
// its size. The count comes from the text itself, so it costs no more than
// the text; on text that is not JSON it is only a wrong size, and the parse
// fails anyway.
func (p *parser) members() int {
	n, depth := 1, 0
	for i := p.i; i < len(p.s); i++ {
		switch p.s[i] {
		case '"':
			for i++; i < len(p.s) && p.s[i] != '"'; i++ {
				if p.s[i] == '\\' {
					i++
				}
			}
		case '[', '{':
			depth++
		case ']', '}':
			if depth == 0 {
				return n
			}
			depth--
		case ',':
			if depth == 0 {
				n++
			}
		}
	}
	return n
}

func (p *parser) space() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *parser) value(depth int) (merge.Node, error) {
	if p.i >= len(p.s) {
		return merge.Node{}, p.fail("a value is missing")
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object(depth + 1)
	case c == '[':
		return p.array(depth + 1)
	case c == '"':
		s, err := p.str()
		if err != nil {
			return merge.Node{}, err
		}
		return merge.Str(s), p.emit(quotedLen(s))
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	case p.literal("true"):
		return merge.Boolean(true), p.emit(len("true"))
	case p.literal("false"):
		return merge.Boolean(false), p.emit(len("false"))
	case p.literal("null"):
		return merge.Node{Kind: merge.Null}, p.emit(len("null"))
	}
	return merge.Node{}, p.fail("not a value")
}

func (p *parser) literal(word string) bool {
	if strings.HasPrefix(string(p.s[p.i:]), word) {
		p.i += len(word)
		return true
	}
	return false
}

func (p *parser) object(depth int) (merge.Node, error) {
	if depth > MaxDepth {
		return merge.Node{}, p.fail(fmt.Sprintf("nested past %d levels", MaxDepth))
	}
	p.i++ // {
	p.space()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return merge.Obj(), p.emit(len("{}"))
	}
	fields := make([]merge.Field, 0, p.members())
	if err := p.emit(1); err != nil { // the opening brace; each field counts its name, its colon, and the comma or closing brace after it
		return merge.Node{}, err
	}
	for {
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return merge.Node{}, p.fail("a field name is missing")
		}
		name, err := p.str()
		if err != nil {
			return merge.Node{}, err
		}
		if err := p.emit(quotedLen(name) + len(":,")); err != nil {
			return merge.Node{}, err
		}
		p.space()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return merge.Node{}, p.fail("a colon is missing")
		}
		p.i++
		p.space()
		v, err := p.value(depth)
		if err != nil {
			return merge.Node{}, err
		}
		fields = append(fields, merge.Field{Name: name, Value: v})
		p.space()
		if p.i >= len(p.s) {
			return merge.Node{}, p.fail("an object is not closed")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			slices.SortFunc(fields, func(a, b merge.Field) int { return strings.Compare(a.Name, b.Name) })
			for i := 1; i < len(fields); i++ {
				if fields[i].Name == fields[i-1].Name {
					return merge.Node{}, p.fail(fmt.Sprintf("the field %q twice in one object", fields[i].Name))
				}
			}
			return merge.Node{Kind: merge.Object, Fields: fields}, nil
		default:
			return merge.Node{}, p.fail("a comma or a closing brace is missing")
		}
	}
}

func (p *parser) array(depth int) (merge.Node, error) {
	if depth > MaxDepth {
		return merge.Node{}, p.fail(fmt.Sprintf("nested past %d levels", MaxDepth))
	}
	p.i++ // [
	p.space()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return merge.Arr(), p.emit(len("[]"))
	}
	elems := make([]merge.Node, 0, p.members())
	if err := p.emit(1); err != nil { // the opening bracket; each value counts the comma or closing bracket after it
		return merge.Node{}, err
	}
	for {
		p.space()
		v, err := p.value(depth)
		if err != nil {
			return merge.Node{}, err
		}
		elems = append(elems, v)
		p.space()
		if p.i >= len(p.s) {
			return merge.Node{}, p.fail("an array is not closed")
		}
		if err := p.emit(1); err != nil { // a comma or the closing bracket
			return merge.Node{}, err
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return merge.Node{Kind: merge.Array, Elems: elems}, nil
		default:
			return merge.Node{}, p.fail("a comma or a closing bracket is missing")
		}
	}
}

// str reads a JSON string at p.i (an opening quote).
func (p *parser) str() (string, error) {
	p.i++ // "
	var b strings.Builder
	for {
		if p.i >= len(p.s) {
			return "", p.fail("a string is not closed")
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return b.String(), nil
		case c < 0x20:
			return "", p.fail("a control character in a string")
		case c == '\\':
			r, err := p.escape()
			if err != nil {
				return "", err
			}
			b.WriteRune(r)
		default:
			r, size := utf8.DecodeRune(p.s[p.i:])
			b.WriteRune(r)
			p.i += size
		}
	}
}

// escape reads one escape at p.i (a backslash), surrogate pairs as one rune.
func (p *parser) escape() (rune, error) {
	if p.i+1 >= len(p.s) {
		return 0, p.fail("an escape is cut short")
	}
	c := p.s[p.i+1]
	p.i += 2
	switch c {
	case '"', '\\', '/':
		return rune(c), nil
	case 'b':
		return '\b', nil
	case 'f':
		return '\f', nil
	case 'n':
		return '\n', nil
	case 'r':
		return '\r', nil
	case 't':
		return '\t', nil
	case 'u':
		r, err := p.hex4()
		if err != nil {
			return 0, err
		}
		if utf16.IsSurrogate(r) {
			if r >= 0xDC00 || p.i+1 >= len(p.s) || p.s[p.i] != '\\' || p.s[p.i+1] != 'u' {
				return 0, p.fail("a lone surrogate")
			}
			p.i += 2
			low, err := p.hex4()
			if err != nil {
				return 0, err
			}
			if low < 0xDC00 || low > 0xDFFF {
				return 0, p.fail("a lone surrogate")
			}
			return utf16.DecodeRune(r, low), nil
		}
		return r, nil
	}
	return 0, p.fail("an escape that is not one")
}

func (p *parser) hex4() (rune, error) {
	if p.i+4 > len(p.s) {
		return 0, p.fail("a unicode escape is cut short")
	}
	v, err := strconv.ParseUint(string(p.s[p.i:p.i+4]), 16, 32)
	if err != nil {
		return 0, p.fail("a unicode escape that is not four hex digits")
	}
	p.i += 4
	return rune(v), nil
}

// number reads a JSON number and returns it as its canonical text, whose
// length is worked out from the digits and counted before it is built.
func (p *parser) number() (merge.Node, error) {
	start := p.i
	neg := false
	if p.s[p.i] == '-' {
		neg = true
		p.i++
	}
	digits := func() []byte {
		from := p.i
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
		}
		return p.s[from:p.i]
	}
	intPart := digits()
	switch {
	case len(intPart) == 0:
		return merge.Node{}, p.fail("a number without digits")
	case len(intPart) > 1 && intPart[0] == '0':
		return merge.Node{}, p.fail("a number with a leading zero")
	}
	var frac []byte
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if frac = digits(); len(frac) == 0 {
			return merge.Node{}, p.fail("a number with no digits after the point")
		}
	}
	exp := 0
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		p.i++
		expNeg := false
		if p.i < len(p.s) && (p.s[p.i] == '+' || p.s[p.i] == '-') {
			expNeg = p.s[p.i] == '-'
			p.i++
		}
		e := digits()
		if len(e) == 0 {
			return merge.Node{}, p.fail("an exponent without digits")
		}
		if e = bytes.TrimLeft(e, "0"); len(e) > 6 {
			return merge.Node{}, p.fail("an exponent past the limit")
		}
		for _, c := range e {
			exp = exp*10 + int(c-'0')
		}
		if expNeg {
			exp = -exp
		}
	}
	d := decimalOf(intPart, frac, exp)
	n, ok := d.textLen(neg)
	if !ok {
		p.i = start
		return merge.Node{}, p.fail(fmt.Sprintf("a number past %d digits", MaxNumberDigits))
	}
	if err := p.emit(n); err != nil {
		return merge.Node{}, err
	}
	return merge.Num(d.text(neg, n)), nil
}

// decimal is a number as written, its digits intPart then frac, with where
// its significant digits are and where its point falls among them.
type decimal struct {
	intPart, frac []byte
	lead, sig     int // leading zeros, then significant digits (0 for zero)
	point         int // digits before the point, from the first significant one
}

func decimalOf(intPart, frac []byte, exp int) decimal {
	d := decimal{intPart: intPart, frac: frac}
	total := len(intPart) + len(frac)
	for d.lead < total && d.digit(d.lead) == '0' {
		d.lead++
	}
	end := total
	for end > d.lead && d.digit(end-1) == '0' {
		end--
	}
	d.sig = end - d.lead
	d.point = len(intPart) + exp - d.lead
	return d
}

func (d decimal) digit(i int) byte {
	if i < len(d.intPart) {
		return d.intPart[i]
	}
	return d.frac[i-len(d.intPart)]
}

// textLen is the length of the decimal's one spelling: no exponent, no
// leading zeros but the one before a point, no trailing zeros after it,
// zero as "0"; false when it would take more than MaxNumberDigits digits on
// either side of the point.
func (d decimal) textLen(neg bool) (int, bool) {
	if d.sig == 0 {
		return 1, true
	}
	intDigits, fracDigits := d.point, d.sig-d.point
	if d.point <= 0 {
		intDigits = 1
	} else if d.point >= d.sig {
		fracDigits = 0
	}
	if intDigits > MaxNumberDigits || fracDigits > MaxNumberDigits {
		return 0, false
	}
	n := intDigits + fracDigits
	if fracDigits > 0 {
		n++ // the point
	}
	if neg {
		n++
	}
	return n, true
}

// text builds the spelling textLen measured, n bytes of it.
func (d decimal) text(neg bool, n int) string {
	if d.sig == 0 {
		return "0"
	}
	b := make([]byte, 0, n)
	if neg {
		b = append(b, '-')
	}
	sig := func(from, to int) {
		for i := from; i < to; i++ {
			b = append(b, d.digit(d.lead+i))
		}
	}
	switch {
	case d.point <= 0:
		b = append(b, '0', '.')
		for range -d.point {
			b = append(b, '0')
		}
		sig(0, d.sig)
	case d.point >= d.sig:
		sig(0, d.sig)
		for range d.point - d.sig {
			b = append(b, '0')
		}
	default:
		sig(0, d.point)
		b = append(b, '.')
		sig(d.point, d.sig)
	}
	return string(b)
}

// Encode is the canonical JSON text of a node: fields in the node's order,
// numbers as their text, strings escaped the JSON way with control characters
// as \u00XX, no whitespace. Parse(Encode(n)) is n, and equal values encode to
// equal bytes.
func Encode(n merge.Node) []byte {
	var b []byte
	return encode(b, n)
}

func encode(b []byte, n merge.Node) []byte {
	switch n.Kind {
	case merge.Null:
		return append(b, "null"...)
	case merge.Bool:
		return strconv.AppendBool(b, n.Bool)
	case merge.Number:
		return append(b, n.Number...)
	case merge.String:
		return appendString(b, n.Text)
	case merge.Array:
		b = append(b, '[')
		for i, e := range n.Elems {
			if i > 0 {
				b = append(b, ',')
			}
			b = encode(b, e)
		}
		return append(b, ']')
	default:
		b = append(b, '{')
		for i, f := range n.Fields {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendString(b, f.Name)
			b = append(b, ':')
			b = encode(b, f.Value)
		}
		return append(b, '}')
	}
}

const hexDigits = "0123456789abcdef"

// quotedLen is the length appendString gives s.
func quotedLen(s string) int {
	n := 2
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"' || c == '\\' || c == '\b' || c == '\f' || c == '\n' || c == '\r' || c == '\t':
			n += 2
		case c < 0x20:
			n += 6
		default:
			n++
		}
	}
	return n
}

func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			b = append(b, '\\', '"')
		case c == '\\':
			b = append(b, '\\', '\\')
		case c == '\b':
			b = append(b, '\\', 'b')
		case c == '\f':
			b = append(b, '\\', 'f')
		case c == '\n':
			b = append(b, '\\', 'n')
		case c == '\r':
			b = append(b, '\\', 'r')
		case c == '\t':
			b = append(b, '\\', 't')
		case c < 0x20:
			b = append(b, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xF])
		default:
			b = append(b, c)
		}
	}
	return append(b, '"')
}
