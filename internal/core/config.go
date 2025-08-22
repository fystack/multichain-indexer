package core

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Chains      ChainsConfig   `yaml:"chains"`
	NATS        NATSConfig     `yaml:"nats"`
	Storage     StorageCfg     `yaml:"storage"`
	DB          DBCfg          `yaml:"db"`
	Redis       RedisCfg       `yaml:"redis"`
	BloomFilter BloomFilterCfg `yaml:"bloomfilter"`
}

// ChainsConfig supports:
// chains:
//
//	defaults: {...}
//	tron: {...}
//	evm: {...}
type ChainsConfig struct {
	Defaults ChainConfig
	Items    map[string]ChainConfig
}

// Custom unmarshal to split defaults vs chain entries.
func (c *ChainsConfig) UnmarshalYAML(b []byte) error {
	// decode into a generic map, then pull out "defaults"
	var raw map[string]ChainConfig
	if err := yaml.Unmarshal(b, &raw); err != nil {
		return err
	}
	if raw == nil {
		raw = map[string]ChainConfig{}
	}
	def, ok := raw["defaults"]
	if ok {
		delete(raw, "defaults")
	}
	c.Defaults = def
	c.Items = raw
	return nil
}

type ChainConfig struct {
	Name         ChainType     `yaml:"name"`
	Nodes        []Node        `yaml:"nodes"` // supports http & ws
	StartBlock   uint64        `yaml:"start_block"`
	FromLatest   bool          `yaml:"from_latest" default:"true"`
	BatchSize    int           `yaml:"batch_size"`
	PollInterval time.Duration `yaml:"poll_interval"`
	Client       ClientCfg     `yaml:"client"`
}

type Node struct {
	// URL can include ${API_KEY} which will be replaced from ApiKey/ApiKeyEnv.
	// Example: "https://eth-mainnet.g.alchemy.com/v2/${API_KEY}"
	URL string `yaml:"url"`

	// Optional; inferred from URL if empty: "http" for http/https, "ws" for ws/wss.
	Type string `yaml:"type"` // "http" | "ws" (optional)

	// API key handling (either supply directly or via env).
	ApiKey    string `yaml:"api_key"`     // optional
	ApiKeyEnv string `yaml:"api_key_env"` // optional, e.g. "ALCHEMY_KEY"

	// If you don't embed the key in URL, you can attach it via headers or query.
	Headers map[string]string `yaml:"headers,omitempty"` // e.g. {"Authorization": "Bearer ${API_KEY}"}
	Query   map[string]string `yaml:"query,omitempty"`   // e.g. {"apikey": "${API_KEY}"}
}

type ClientCfg struct {
	Timeout    time.Duration `yaml:"timeout"`
	MaxRetries int           `yaml:"max_retries"`
	RetryDelay time.Duration `yaml:"retry_delay"`
	Throttle   ThrottleCfg   `yaml:"throttle"`
}

type ThrottleCfg struct {
	RPS   int `yaml:"rps"`
	Burst int `yaml:"burst"`
}

type NATSConfig struct {
	URL           string `yaml:"url"`
	SubjectPrefix string `yaml:"subject_prefix"`
}

type StorageCfg struct {
	Type      string `yaml:"type"`      // memory | badger | postgres
	Directory string `yaml:"directory"` // for badger
}

type BloomFilterCfg struct {
	Backend  BFBackend              `yaml:"backend"`
	Redis    RedisBloomFilterCfg    `yaml:"redis"`
	InMemory InMemoryBloomFilterCfg `yaml:"in_memory"`
}

type DBCfg struct {
	URL         string `yaml:"url"`
	Environment string `yaml:"environment"`
}

type RedisCfg struct {
	URL         string `yaml:"url"`
	Password    string `yaml:"password"`
	Environment string `yaml:"environment"`
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

// Load reads YAML, applies defaults & per-chain merges, resolves node auth, and validates.
func Load(path string) (Config, error) {
	var cfg Config

	b, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return cfg, err
	}

	applyDefaults(&cfg)
	if err := finalizeNodes(&cfg); err != nil {
		return cfg, err
	}
	if err := validate(cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// ---- defaults & merging ----

func applyDefaults(cfg *Config) {
	def := cfg.Chains.Defaults
	for name, c := range cfg.Chains.Items {
		if c.BatchSize == 0 {
			c.BatchSize = def.BatchSize
		}
		if c.PollInterval == 0 {
			c.PollInterval = def.PollInterval
		}
		// client defaults
		if c.Client.Timeout == 0 {
			c.Client.Timeout = def.Client.Timeout
		}
		if c.Client.MaxRetries == 0 {
			c.Client.MaxRetries = def.Client.MaxRetries
		}
		if c.Client.RetryDelay == 0 {
			c.Client.RetryDelay = def.Client.RetryDelay
		}
		if c.Client.Throttle.RPS == 0 {
			c.Client.Throttle.RPS = def.Client.Throttle.RPS
		}
		if c.Client.Throttle.Burst == 0 {
			c.Client.Throttle.Burst = def.Client.Throttle.Burst
		}
		cfg.Chains.Items[name] = c
	}
}

// finalizeNodes:
// - fills ApiKey from env if needed
// - substitutes ${API_KEY} in URL/Headers/Query
// - infers node Type from URL scheme if empty
// - materializes query params into the URL (keeps Headers as-is)
func finalizeNodes(cfg *Config) error {
	for chainName, c := range cfg.Chains.Items {
		nodes := make([]Node, len(c.Nodes))
		for i, n := range c.Nodes {
			// ensure maps are non-nil so we can assign safely
			if n.Headers == nil {
				n.Headers = map[string]string{}
			}
			if n.Query == nil {
				n.Query = map[string]string{}
			}

			key := n.ApiKey
			if key == "" && n.ApiKeyEnv != "" {
				key = os.Getenv(n.ApiKeyEnv)
			}
			// Substitute ${API_KEY} in URL, headers, query
			n.URL = substituteKey(n.URL, key)
			for hk, hv := range n.Headers {
				n.Headers[hk] = substituteEnvVars(hv)
			}
			for qk, qv := range n.Query {
				n.Query[qk] = substituteEnvVars(qv)
			}

			// Infer type from scheme if empty
			if n.Type == "" {
				u, err := url.Parse(n.URL)
				if err != nil || u.Scheme == "" {
					return fmt.Errorf("%s: invalid node url: %q", chainName, n.URL)
				}
				switch u.Scheme {
				case "ws", "wss":
					n.Type = "ws"
				default:
					n.Type = "http"
				}
			}

			// Attach query params into URL
			if len(n.Query) > 0 {
				u, err := url.Parse(n.URL)
				if err != nil {
					return fmt.Errorf("%s: invalid node url: %q", chainName, n.URL)
				}
				q := u.Query()
				for k, v := range n.Query {
					q.Set(k, v)
				}
				u.RawQuery = q.Encode()
				n.URL = u.String()
			}

			nodes[i] = n
		}
		c.Nodes = nodes
		cfg.Chains.Items[chainName] = c
	}
	return nil
}

func substituteKey(s, key string) string {
	if s == "" || key == "" {
		return s
	}
	// Replace ${API_KEY} for backward compatibility
	s = strings.ReplaceAll(s, "${API_KEY}", key)

	// Also handle environment variable patterns like ${TRONGRID_TOKEN}
	// Find all ${VAR_NAME} patterns and replace with the key if they match the ApiKeyEnv
	return s
}

// substituteEnvVars replaces all ${VAR_NAME} patterns with their environment values
func substituteEnvVars(s string) string {
	if s == "" {
		return s
	}

	// Find all ${VAR_NAME} patterns
	for {
		start := strings.Index(s, "${")
		if start == -1 {
			break
		}
		end := strings.Index(s[start:], "}")
		if end == -1 {
			break
		}
		end += start

		// Extract variable name
		varName := s[start+2 : end]
		envValue := os.Getenv(varName)

		// Replace the pattern with the environment value
		pattern := "${" + varName + "}"
		s = strings.ReplaceAll(s, pattern, envValue)
	}

	return s
}

// ---- validation ----

func validate(cfg Config) error {
	if cfg.NATS.URL == "" {
		return errors.New("nats.url is required")
	}
	if cfg.Storage.Type == "badger" && cfg.Storage.Directory == "" {
		return errors.New("storage.directory is required for badger")
	}
	if len(cfg.Chains.Items) == 0 {
		return errors.New("no chains configured under chains")
	}
	for name, c := range cfg.Chains.Items {
		if len(c.Nodes) == 0 {
			return fmt.Errorf("%s: chains.<name>.nodes is required", name)
		}
		for _, n := range c.Nodes {
			if n.URL == "" {
				return fmt.Errorf("%s: node url is required", name)
			}
			if n.Type != "" && n.Type != "http" && n.Type != "ws" {
				return fmt.Errorf("%s: node type must be http or ws", name)
			}
		}
		if !c.FromLatest && c.StartBlock == 0 {
			return fmt.Errorf("%s: start_block required when from_latest=false", name)
		}
		if c.BatchSize <= 0 {
			return fmt.Errorf("%s: batch_size must be > 0", name)
		}
		if c.Client.MaxRetries < 0 {
			return fmt.Errorf("%s: client.max_retries must be >= 0", name)
		}
		if c.Client.Throttle.RPS < 0 || c.Client.Throttle.Burst < 0 {
			return fmt.Errorf("%s: throttle rps/burst must be >= 0", name)
		}
	}
	return nil
}
