package kv

import (
	"encoding/binary"
	"fmt"
)

// Location is where a kv conflict is: uvarint(len(key)) · key · sub, sub
// being the field of a hash or the member of a sorted set at fault, and
// empty for a conflict at the key as a whole. Keys are any bytes, so the
// key's length comes first and a key never runs into what is below it.
func Location(key, sub []byte) []byte {
	out := binary.AppendUvarint(make([]byte, 0, binary.MaxVarintLen64+len(key)+len(sub)), uint64(len(key)))
	return append(append(out, key...), sub...)
}

// ParseLocation is Location's inverse: the key and what is below it. It
// refuses a location that is not one: an empty key, a key length past the
// end, a length that does not end.
func ParseLocation(loc []byte) (key, sub []byte, err error) {
	n, w := binary.Uvarint(loc) // n is 0 when the length is missing or does not end
	if n == 0 || n > uint64(len(loc)-w) {
		return nil, nil, fmt.Errorf("%w: a location whose key is %d bytes of %d", ErrKey, n, len(loc)-w)
	}
	return loc[w : w+int(n)], loc[w+int(n):], nil
}
