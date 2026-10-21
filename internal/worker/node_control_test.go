package worker

import (
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/config"
)

func TestValidateNodeURLs(t *testing.T) {
	if err := ValidateNodeURLs([]string{"https://rpc.example/v1/key", "http://10.0.0.1:8545"}); err != nil {
		t.Fatalf("valid URLs refused: %v", err)
	}
	for _, bad := range [][]string{nil, {"ftp://rpc.example"}, {"rpc.example"}, {"https://"}} {
		if ValidateNodeURLs(bad) == nil {
			t.Fatalf("accepted %v", bad)
		}
	}
}

func TestSetAndResetChainNodes(t *testing.T) {
	var got []config.NodeConfig
	registerNodeController("test_chain", func(nodes []config.NodeConfig) error { got = nodes; return nil })
	setConfiguredNodes("test_chain", []config.NodeConfig{{URL: "https://configured.example"}})
	m := &Manager{internalCodes: map[string]string{"test_chain": "TEST"}, networkChains: map[string]string{}}

	if err := m.SetChainNodes("test_chain", []string{" https://new.example "}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "https://new.example" {
		t.Fatalf("nodes not replaced: %+v", got)
	}
	if err := m.ResetChainNodes("test_chain"); err != nil {
		t.Fatal(err)
	}
	if got[0].URL != "https://configured.example" {
		t.Fatalf("not reset to config: %+v", got)
	}
	if err := m.SetChainNodes("no_such_chain", []string{"https://x.example"}); err != ErrUnknownChain {
		t.Fatalf("want ErrUnknownChain, got %v", err)
	}
}
