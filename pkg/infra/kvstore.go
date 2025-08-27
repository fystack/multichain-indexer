package infra

import (
	"bytes"
	"encoding/gob"
	"encoding/json"

	"github.com/hashicorp/consul/api"
)

// KVStore is an interface for key-value stores.
// There are multiple implementations available like Consul, Postgres, Redis, BoltDB, BadgerDB, etcd, etc.

type KVPair struct {
	Key   string
	Value []byte
}

type KVStore interface {
	GetName() string
	Set(k string, v string) error
	Get(k string) (v string, err error)
	GetWithOptions(k string, queryOptions *api.QueryOptions) (v string, err error)
	// This method if you want to set v as struct or map
	SetAny(k string, v any) error
	GetAny(k string, v any) (found bool, err error)

	List(prefix string) ([]*KVPair, error)
	Delete(k string) error
	Close() error
}

// Codec encodes/decodes Go values to/from slices of bytes.
type Codec interface {
	// Marshal encodes a Go value to a slice of bytes.
	Marshal(v any) ([]byte, error)
	// Unmarshal decodes a slice of bytes into a Go value.
	Unmarshal(data []byte, v any) error
}

// Convenience variables
var (
	// JSON is a JSONcodec that encodes/decodes Go values to/from JSON.
	JSON = JSONcodec{}
	// Gob is a GobCodec that encodes/decodes Go values to/from gob.
	Gob = GobCodec{}
)

// JSONcodec encodes/decodes Go values to/from JSON.
// You can use encoding.JSON instead of creating an instance of this struct.
type JSONcodec struct{}

// Marshal encodes a Go value to JSON.
func (c JSONcodec) Marshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

// Unmarshal decodes a JSON value into a Go value.
func (c JSONcodec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}

// GobCodec encodes/decodes Go values to/from gob.
// You can use encoding.Gob instead of creating an instance of this struct.
type GobCodec struct{}

// Marshal encodes a Go value to gob.
func (c GobCodec) Marshal(v any) ([]byte, error) {
	buffer := new(bytes.Buffer)
	encoder := gob.NewEncoder(buffer)
	err := encoder.Encode(v)
	if err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// Unmarshal decodes a gob value into a Go value.
func (c GobCodec) Unmarshal(data []byte, v any) error {
	reader := bytes.NewReader(data)
	decoder := gob.NewDecoder(reader)
	return decoder.Decode(v)
}
