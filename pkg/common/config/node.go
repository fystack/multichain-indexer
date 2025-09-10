package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

type Node struct {
	URL       string            `yaml:"url" validate:"required,url"`
	Type      string            `yaml:"type" validate:"omitempty,oneof=http ws"`
	ApiKey    string            `yaml:"api_key"`
	ApiKeyEnv string            `yaml:"api_key_env"`
	Headers   map[string]string `yaml:"headers,omitempty"`
	Query     map[string]string `yaml:"query,omitempty"`
}

// FinalizeNodes: fill api key, env substitution, infer type
func (cc *ChainsConfig) FinalizeNodes() error {
	for chainName, chain := range cc.Items {
		nodes := make([]Node, len(chain.Nodes))
		for i, n := range chain.Nodes {
			if n.Headers == nil {
				n.Headers = map[string]string{}
			}
			if n.Query == nil {
				n.Query = map[string]string{}
			}

			// fill API key
			key := n.ApiKey
			if key == "" && n.ApiKeyEnv != "" {
				key = os.Getenv(n.ApiKeyEnv)
			}

			// substitute ${VAR} in URL / headers / query
			n.URL = substituteKey(n.URL, key)
			for k, v := range n.Headers {
				n.Headers[k] = substituteEnvVars(v)
			}
			for k, v := range n.Query {
				n.Query[k] = substituteEnvVars(v)
			}

			// infer type
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

			// attach query into URL
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
		chain.Nodes = nodes
		cc.Items[chainName] = chain
	}
	return nil
}

// helpers
func substituteKey(s, key string) string {
	if s == "" || key == "" {
		return s
	}
	return strings.ReplaceAll(s, "${API_KEY}", key)
}

func substituteEnvVars(s string) string {
	if s == "" {
		return s
	}
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
		varName := s[start+2 : end]
		envValue := os.Getenv(varName)
		s = strings.ReplaceAll(s, "${"+varName+"}", envValue)
	}
	return s
}
