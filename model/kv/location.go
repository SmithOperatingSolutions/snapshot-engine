package kv

import (
	"encoding/binary"
	"fmt"

	"github.com/SmithOperatingSolutions/snapshot-core/core/wire"
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
// refuses a location Location did not write, so a place has one spelling:
// an empty key, a key over MaxKeySize, a key length past the end, one that
// does not end or is not minimal.
func ParseLocation(loc []byte) (key, sub []byte, err error) {
	r := wire.NewReader(loc)
	key = r.LenBytes(MaxKeySize)
	if err := r.Err(); err != nil {
		return nil, nil, fmt.Errorf("%w: a location whose key is not one: %w", ErrKey, err)
	}
	if len(key) == 0 {
		return nil, nil, fmt.Errorf("%w: a location with an empty key", ErrKey)
	}
	return key, loc[len(loc)-r.Remaining():], nil
}
