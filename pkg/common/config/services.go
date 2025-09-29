package config

import "github.com/fystack/multichain-indexer/pkg/common/enum"

type Services struct {
	Port        int                `yaml:"port" validate:"required,min=1,max=65535"`
	Worker      WorkerConfig       `yaml:"worker"`
	Nats        NatsConfig         `yaml:"nats"`
	Database    *DatabaseConfig    `yaml:"database,omitempty"`
	KVS         KVSConfig          `yaml:"kvstore"`
	Badger      BadgerConfig       `yaml:"badger"`
	Redis       RedisConfig        `yaml:"redis"`
	Bloomfilter *BloomfilterConfig `yaml:"bloomfilter,omitempty"`
}

type WorkerConfig struct {
	Regular   WorkerModeConfig `yaml:"regular"`
	Rescanner WorkerModeConfig `yaml:"rescanner"`
	Manual    WorkerModeConfig `yaml:"manual"`
	Catchup   WorkerModeConfig `yaml:"catchup"`
}

type WorkerModeConfig struct {
	Enabled bool `yaml:"enabled"`
}

type NatsConfig struct {
	URL           string        `yaml:"url"`
	SubjectPrefix string        `yaml:"subject_prefix"`
	Username      string        `yaml:"username"`
	Password      string        `yaml:"password"`
	TLS           NatsTLSConfig `yaml:"tls"`
}

type NatsTLSConfig struct {
	ClientCert string `yaml:"client_cert"`
	ClientKey  string `yaml:"client_key"`
	CACert     string `yaml:"ca_cert"`
}

type DatabaseConfig struct {
	URL string `yaml:"url"`
}

type RedisConfig struct {
	URL      string `yaml:"url"`
	Password string `yaml:"password"`
	MTLS     bool   `yaml:"mtls"`
}

type KVSConfig struct {
	Type   enum.KVStoreType `yaml:"type"`
	Consul ConsulConfig     `yaml:"consul"`
	Badger BadgerConfig     `yaml:"badger"`
}

type ConsulConfig struct {
	Scheme   string         `yaml:"scheme"`
	Address  string         `yaml:"address"`
	Folder   string         `yaml:"folder"`
	Token    string         `yaml:"token"`
	HttpAuth HttpAuthConfig `yaml:"http_auth"`
}

type HttpAuthConfig struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type BadgerConfig struct {
	Directory string `yaml:"directory"`
	Prefix    string `yaml:"prefix"`
}

type BloomfilterConfig struct {
	Type              enum.BFType    `yaml:"type"`
	WalletAddressRepo string         `yaml:"wallet_address_repo"`
	BatchSize         int            `yaml:"batch_size"`
	Redis             RedisBFConfig  `yaml:"redis"`
	InMemory          InMemoryConfig `yaml:"in_memory"`
}

type RedisBFConfig struct {
	KeyPrefix string  `yaml:"key_prefix"`
	ErrorRate float64 `yaml:"error_rate"`
	Capacity  int     `yaml:"capacity"`
}

type InMemoryConfig struct {
	ExpectedItems     uint    `yaml:"expected_items"`
	FalsePositiveRate float64 `yaml:"false_positive_rate"`
}
