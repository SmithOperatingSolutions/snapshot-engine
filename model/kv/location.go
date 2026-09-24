package kv

import "errors"

// Location is where a kv conflict is: uvarint(len(key)) · key · sub, sub
// being the field of a hash or the member of a sorted set at fault, and
// empty for a conflict at the key as a whole. Keys are any bytes, so the
// key's length comes first and a key never runs into what is below it.
// (Stub.)
func Location(key, sub []byte) []byte { return nil }

// ParseLocation is Location's inverse: the key and what is below it. It
// refuses a location that is not one. (Stub.)
func ParseLocation(loc []byte) (key, sub []byte, err error) {
	return nil, nil, errors.New("kv: ParseLocation: not implemented")
}
