package utils

import (
	"bytes"
	"encoding/gob"
)

// Encode converts an interface into a byte slice using gob encoding.
// Useful for serializing complex data structures into a generic payload.
func Encode(data interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Decode converts a byte slice back into an interface using gob decoding.
// The 'out' parameter must be a pointer to the target data structure.
func Decode(data []byte, out interface{}) error {
	buf := bytes.NewBuffer(data)
	dec := gob.NewDecoder(buf)
	return dec.Decode(out)
}

