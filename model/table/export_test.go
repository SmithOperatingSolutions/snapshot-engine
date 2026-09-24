package table

// EncodeCell and DecodeCell expose the cell codec to the package's tests.
func EncodeCell(c Column, v any) ([]byte, error) { return encodeCell(nil, c, v) }

func DecodeCell(b []byte, c Column) (any, []byte, error) { return decodeCell(b, c) }
