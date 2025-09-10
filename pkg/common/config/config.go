package config

import (
	"os"

	"github.com/fystack/transaction-indexer/pkg/common/enum"
	"github.com/go-playground/validator/v10"
	"github.com/goccy/go-yaml"
	"github.com/imdario/mergo"
)

var validate = validator.New()

type Config struct {
	Environment string         `yaml:"environment" validate:"required,oneof=production development"`
	Chains      ChainsConfig   `yaml:"chains" validate:"required"`
	NATS        NATSConfig     `yaml:"nats" validate:"required"`
	KVStore     KVStoreCfg     `yaml:"kvstore" validate:"required"`
	DB          DBCfg          `yaml:"db" validate:"required"`
	Redis       RedisCfg       `yaml:"redis" validate:"required"`
	BloomFilter BloomFilterCfg `yaml:"bloomfilter" validate:"required"`
	Worker      WorkerCfg      `yaml:"worker"`
}

type NATSConfig struct {
	URL           string `yaml:"url" validate:"required,url"`
	SubjectPrefix string `yaml:"subject_prefix" validate:"required"`
}

type KVStoreCfg struct {
	Type   enum.KVStoreType `yaml:"type" validate:"required,oneof=badger consul postgres"`
	Badger BadgerKVCfg      `yaml:"badger"`
	Consul ConsulKVCfg      `yaml:"consul"`
}

type BadgerKVCfg struct {
	Directory string `yaml:"directory"`
	Prefix    string `yaml:"prefix"`
}

type ConsulKVCfg struct {
	Scheme   string      `yaml:"scheme"`
	Address  string      `yaml:"address"`
	Folder   string      `yaml:"folder"`
	Token    string      `yaml:"token"`
	HttpAuth HttpAuthCfg `yaml:"http_auth"`
}

type HttpAuthCfg struct {
	Username string `yaml:"username"`
	Password string `yaml:"password"`
}

type DBCfg struct {
	Type string `yaml:"type" validate:"required,oneof=postgres mysql sqlite"`
	URL  string `yaml:"url" validate:"required"`
}

type RedisCfg struct {
	URL      string `yaml:"url" validate:"required"`
	Password string `yaml:"password"`
}

type BloomFilterCfg struct {
	Backend  enum.BFBackend         `yaml:"backend" validate:"required,oneof=redis in_memory"`
	Redis    RedisBloomFilterCfg    `yaml:"redis"`
	InMemory InMemoryBloomFilterCfg `yaml:"in_memory"`
}

type RedisBloomFilterCfg struct {
	WalletAddressRepo string  `yaml:"wallet_address_repo"`
	BatchSize         int     `yaml:"batch_size"`
	KeyPrefix         string  `yaml:"key_prefix"`
	ErrorRate         float64 `yaml:"error_rate"`
	Capacity          int     `yaml:"capacity"`
}

type InMemoryBloomFilterCfg struct {
	WalletAddressRepo string  `yaml:"wallet_address_repo"`
	ExpectedItems     uint    `yaml:"expected_items"`
	FalsePositiveRate float64 `yaml:"false_positive_rate"`
	BatchSize         int     `yaml:"batch_size"`
}

type WorkerCfg struct {
	Manual    WorkerItem `yaml:"manual"`
	Catchup   WorkerItem `yaml:"catchup"`
	Rescanner WorkerItem `yaml:"rescanner"`
}

type WorkerItem struct {
	Enabled bool `yaml:"enabled"`
}

func Load(path string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}

	// merge defaults
	for name, chain := range cfg.Chains.Items {
		if err := mergo.Merge(&chain, cfg.Chains.Defaults); err != nil {
			return cfg, err
		}
		cfg.Chains.Items[name] = chain
	}

	// finalize nodes
	if err := cfg.Chains.FinalizeNodes(); err != nil {
		return cfg, err
	}

	// validate
	if err := validate.Struct(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}
