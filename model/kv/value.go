// Package kv is the key-value model (model id 6): an object is an ordered map
// from key bytes to a typed value, each value kind with a merge policy of its
// own (docs/specs/engine-layers.md). This slice stores the bytes kind, whose
// policy is the plain one: a change on one side is taken, the same change on
// both once, and two different changes to one key are a conflict at that key.
package kv

import "errors"

// Kind is a value's type, the first byte of its frame.
type Kind uint8

// Kinds. Counter, set, hash, sorted set and sequence follow in later slices;
// 0 is never a kind.
const (
	Bytes Kind = 1
)

// MaxValueSize is the longest payload a value may carry.
const MaxValueSize = 256 << 10

// Value is one key's value: its kind and, for Bytes, its payload.
type Value struct {
	Kind  Kind
	Bytes []byte
}

var errNotImplemented = errors.New("kv: not implemented")

// EncodeValue returns a value's frame: kind u8 · payload.
func EncodeValue(v Value) ([]byte, error) { return nil, errNotImplemented }

// DecodeValue parses a frame; every other byte string is refused.
func DecodeValue(b []byte) (Value, error) { return Value{}, errNotImplemented }
