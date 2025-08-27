package kvstore

// This is a modified version of :https://github.com/philippgille/gokv/consul
// With extended functionalities:
// Get, Set k,v as string
// GetAny, SetAny, k: string, v: any
// List all keys with prefix

import (
	"errors"
	"fmt"
	"time"

	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/fystack/transaction-indexer/pkg/infra"
	"github.com/hashicorp/consul/api"
)

var (
	ErrKeyNotFound = errors.New("key not found")
	ErrKeyEmpty    = errors.New("key is empty")
)

var DefaultCacheOptions = api.QueryOptions{
	UseCache:     true,
	MaxAge:       30 * time.Minute,
	StaleIfError: 5 * time.Minute,
}

// CheckKeyAndValue returns an error if k == "" or if v == nil
func checkKeyAndValue(k string, v any) error {
	if k == "" {
		return ErrKeyEmpty
	}

	if v == nil {
		return errors.New("the passed value is nil, which is not allowed")
	}

	return nil
}

// ConsulClient implement infra.KVStore
type ConsulClient struct {
	c      *api.KV
	folder string
	codec  infra.Codec
}

func (c ConsulClient) GetName() string {
	return string(enum.KVStoreTypeConsul)
}

func (c ConsulClient) Set(k string, v string) error {
	if err := checkKeyAndValue(k, v); err != nil {
		return err
	}

	if c.folder != "" {
		k = c.folder + "/" + k
	}
	kvPair := api.KVPair{
		Key:   k,
		Value: []byte(v),
	}
	_, err := c.c.Put(&kvPair, nil)
	if err != nil {
		return err
	}

	return nil
}

// Get retrieves the stored value for the given key.
func (c ConsulClient) Get(k string) (v string, err error) {
	if k == "" {
		return "", ErrKeyEmpty
	}

	if c.folder != "" {
		k = c.folder + "/" + k
	}
	kvPair, _, err := c.c.Get(k, nil)
	if err != nil {
		return "", err
	}
	// If no value was found return false
	if kvPair == nil {
		return "", ErrKeyNotFound
	}
	data := kvPair.Value
	return string(data), err
}

// Get retrieves the stored value for the given key with caching options.
func (c ConsulClient) GetWithOptions(k string, queryOptions *api.QueryOptions) (v string, err error) {
	if k == "" {
		return "", ErrKeyEmpty
	}

	if c.folder != "" {
		k = c.folder + "/" + k
	}

	// Use the provided QueryOptions to control caching
	kvPair, _, err := c.c.Get(k, queryOptions)
	if err != nil {
		return "", err
	}

	// If no value was found, return an error
	if kvPair == nil {
		return "", ErrKeyNotFound
	}

	data := kvPair.Value
	return string(data), nil
}

// Set stores the given value for the given key.
// Values are automatically marshalled to JSON or gob (depending on the configuration).
// The key must not be "" and the value must not be nil.
func (c ConsulClient) SetAny(k string, v any) error {
	if err := checkKeyAndValue(k, v); err != nil {
		return err
	}

	// First turn the passed object into something that Consul can handle
	data, err := c.codec.Marshal(v)
	if err != nil {
		return err
	}

	if c.folder != "" {
		k = c.folder + "/" + k
	}
	kvPair := api.KVPair{
		Key:   k,
		Value: data,
	}
	_, err = c.c.Put(&kvPair, nil)
	if err != nil {
		return err
	}

	return nil
}

// Get retrieves the stored value for the given key.
// You need to pass a pointer to the value, so in case of a struct
// the automatic unmarshalling can populate the fields of the object
// that v points to with the values of the retrieved object's values.
// If no value is found it returns (false, nil).
// The key must not be "" and the pointer must not be nil.
func (c ConsulClient) GetAny(k string, v any) (found bool, err error) {
	if err := checkKeyAndValue(k, v); err != nil {
		return false, err
	}

	if c.folder != "" {
		k = c.folder + "/" + k
	}
	kvPair, _, err := c.c.Get(k, nil)
	if err != nil {
		return false, err
	}
	// If no value was found return false
	if kvPair == nil {
		return false, nil
	}
	data := kvPair.Value
	return true, c.codec.Unmarshal(data, v)
}

func (c ConsulClient) List(prefix string) ([]*infra.KVPair, error) {
	if prefix == "" {
		return nil, errors.New("prefix is empty")
	}

	if c.folder != "" {
		prefix = c.folder + "/" + prefix
	}

	kvPairs, _, err := c.c.List(prefix, nil)
	if err != nil {
		return nil, err
	}

	result := make([]*infra.KVPair, len(kvPairs))
	for i, kvPair := range kvPairs {
		result[i] = &infra.KVPair{
			Key:   kvPair.Key,
			Value: kvPair.Value,
		}
	}

	return result, nil
}

// Delete deletes the stored value for the given key.
// Deleting a non-existing key-value pair does NOT lead to an error.
// The key must not be "".
func (c ConsulClient) Delete(k string) error {
	if k == "" {
		return ErrKeyEmpty
	}

	if c.folder != "" {
		k = c.folder + "/" + k
	}
	_, err := c.c.Delete(k, nil)
	return err
}

// Close closes the client.
// In the Consul implementation this doesn't have any effect.
func (c ConsulClient) Close() error {
	return nil
}

// Options are the options for the Consul client.
type Options struct {
	// URI scheme for the Consul server.
	// Optional ("http" by default).
	Scheme string
	// Address of the Consul server, including port number.
	// Optional ("127.0.0.1:8500" by default).
	Address string
	// Directory under which to store the key-value pairs.
	// The Consul UI calls this "folder".
	// Optional (none by default).
	Folder string
	// Encoding format.
	// Optional (encoding.JSON by default).
	Codec infra.Codec

	// Client token
	Token    string
	HttpAuth *api.HttpBasicAuth
}

// DefaultConsulOptions is an Options object with default values.
// Scheme: "http", Address: "127.0.0.1:8500", Folder: none, Codec: encoding.JSON
var DefaultConsulOptions = Options{
	Scheme:  "http",
	Address: "127.0.0.1:8500",
	Codec:   infra.JSON,
	// No need to define Folder because its zero value is fine
}

// func GetConsulOptions(environment string) Options {
// 	if environment != constant.EnvProduction {
// 		options := DefaultConsulOptions
// 		options.Address = viper.GetString("consul.address")
// 		return options
// 	}

// 	return Options{
// 		Scheme:  "https",
// 		Address: viper.GetString("consul.address"),
// 		Token:   viper.GetString("consul.token"),
// 		HttpAuth: &api.HttpBasicAuth{
// 			Username: viper.GetString("consul.username"),
// 			Password: viper.GetString("consul.password"),
// 		},
// 	}
// }

// NewClient creates a new Consul client.
func NewConsulClient(options Options) (infra.KVStore, error) {
	result := ConsulClient{}

	// Set default values
	if options.Scheme == "" {
		options.Scheme = DefaultConsulOptions.Scheme
	}
	if options.Address == "" {
		options.Address = DefaultConsulOptions.Address
	}
	if options.Codec == nil {
		options.Codec = DefaultConsulOptions.Codec
	}

	config := api.DefaultConfig()
	config.Scheme = options.Scheme
	config.Address = options.Address
	// Add connection timeout
	config.WaitTime = 10 * time.Second
	if options.Token != "" {
		config.Token = options.Token
	}
	if options.HttpAuth != nil {
		config.HttpAuth = options.HttpAuth
	}

	client, err := api.NewClient(config)
	if err != nil {
		return result, err
	}

	// Ping the Consul server to verify connectivity
	_, err = client.Status().Leader()
	if err != nil {
		return result, fmt.Errorf("failed to connect to Consul: %w", err)
	}

	result.c = client.KV()
	result.folder = options.Folder
	result.codec = options.Codec

	return result, nil
}
