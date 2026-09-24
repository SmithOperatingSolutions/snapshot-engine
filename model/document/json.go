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
	"errors"
	"fmt"
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
// fields sorted by name. Anything else is ErrDocument.
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
	s []byte
	i int
}

func (p *parser) fail(what string) error {
	return fmt.Errorf("%w: %s at byte %d", ErrDocument, what, p.i)
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
		return merge.Str(s), nil
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	case p.literal("true"):
		return merge.Boolean(true), nil
	case p.literal("false"):
		return merge.Boolean(false), nil
	case p.literal("null"):
		return merge.Node{Kind: merge.Null}, nil
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
	var fields []merge.Field
	seen := map[string]bool{}
	p.space()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return merge.Obj(), nil
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
		if seen[name] {
			return merge.Node{}, p.fail(fmt.Sprintf("the field %q twice in one object", name))
		}
		seen[name] = true
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
			return merge.Obj(fields...), nil
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
	var elems []merge.Node
	p.space()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return merge.Arr(), nil
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
		switch p.s[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return merge.Arr(elems...), nil
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

// number reads a JSON number and returns it as its canonical text.
func (p *parser) number() (merge.Node, error) {
	start := p.i
	neg := false
	if p.s[p.i] == '-' {
		neg = true
		p.i++
	}
	digits := func() string {
		from := p.i
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
		}
		return string(p.s[from:p.i])
	}
	intPart := digits()
	switch {
	case intPart == "":
		return merge.Node{}, p.fail("a number without digits")
	case len(intPart) > 1 && intPart[0] == '0':
		return merge.Node{}, p.fail("a number with a leading zero")
	}
	frac := ""
	if p.i < len(p.s) && p.s[p.i] == '.' {
		p.i++
		if frac = digits(); frac == "" {
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
		if e == "" {
			return merge.Node{}, p.fail("an exponent without digits")
		}
		if len(strings.TrimLeft(e, "0")) > 6 {
			return merge.Node{}, p.fail("an exponent past the limit")
		}
		exp, _ = strconv.Atoi(e)
		if expNeg {
			exp = -exp
		}
	}
	text, ok := canonicalNumber(neg, intPart, frac, exp)
	if !ok {
		p.i = start
		return merge.Node{}, p.fail(fmt.Sprintf("a number past %d digits", MaxNumberDigits))
	}
	return merge.Num(text), nil
}

// canonicalNumber is the one spelling of a decimal: no exponent, no
// leading zeros but the one before a point, no trailing zeros after it,
// zero as "0"; false when it would take more than MaxNumberDigits digits on
// either side of the point.
func canonicalNumber(neg bool, intPart, frac string, exp int) (string, bool) {
	all := intPart + frac
	point := len(intPart) + exp // digits before the point
	trimmed := strings.TrimLeft(all, "0")
	point -= len(all) - len(trimmed)
	all = strings.TrimRight(trimmed, "0")
	if all == "" {
		return "0", true
	}
	intDigits, fracDigits := point, len(all)-point
	if point <= 0 {
		intDigits, fracDigits = 1, len(all)-point
	} else if point >= len(all) {
		fracDigits = 0
	}
	if intDigits > MaxNumberDigits || fracDigits > MaxNumberDigits {
		return "", false
	}
	var b strings.Builder
	if neg {
		b.WriteByte('-')
	}
	switch {
	case point <= 0:
		b.WriteString("0.")
		b.WriteString(strings.Repeat("0", -point))
		b.WriteString(all)
	case point >= len(all):
		b.WriteString(all)
		b.WriteString(strings.Repeat("0", point-len(all)))
	default:
		b.WriteString(all[:point])
		b.WriteByte('.')
		b.WriteString(all[point:])
	}
	return b.String(), true
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
