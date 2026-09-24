package kv_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/SmithOperatingSolutions/snapshot-engine/model/kv"
)

// A conflict's location names its key and, below it, the hash field or
// sorted-set member at fault. Keys are any bytes, so key "a" with field
// "bc" and key "ab" with field "c" are two places; a location that is not
// one is refused.
func TestALocationNamesAKeyAndWhatIsBelowIt(t *testing.T) {
	for _, tc := range []struct{ key, sub string }{{"user:1", "mail"}, {"k", ""}, {"a", "bc"}, {"ab", "c"}, {"x\x00y", "\xff"}} {
		loc := kv.Location([]byte(tc.key), []byte(tc.sub))
		key, sub, err := kv.ParseLocation(loc)
		if err != nil || string(key) != tc.key || string(sub) != tc.sub {
			t.Errorf("Location(%q, %q) parses back as %q, %q, %v", tc.key, tc.sub, key, sub, err)
		}
	}
	if bytes.Equal(kv.Location([]byte("a"), []byte("bc")), kv.Location([]byte("ab"), []byte("c"))) {
		t.Error("key a with field bc and key ab with field c are one location")
	}
	for name, loc := range map[string][]byte{"empty": {}, "a length past the end": {9, 'a'}, "an empty key": {0, 'f'}, "a varint that never ends": {0x80}} {
		if _, _, err := kv.ParseLocation(loc); !errors.Is(err, kv.ErrKey) {
			t.Errorf("%s: ParseLocation = %v, want ErrKey", name, err)
		}
	}
}
