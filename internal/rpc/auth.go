package rpc

type AuthType string

const (
	AuthTypeHeader AuthType = "header"
	AuthTypeQuery  AuthType = "query"
)

// AuthConfig holds authentication configuration
type AuthConfig struct {
	Type  AuthType `json:"type"  yaml:"type"`
	Key   string   `json:"key"   yaml:"key"`
	Value string   `json:"value" yaml:"value"`
}
