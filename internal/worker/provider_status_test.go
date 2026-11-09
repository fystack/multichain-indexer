package worker

import (
	"testing"
	"time"

	"github.com/fystack/multichain-indexer/internal/rpc"
)

func TestProviderStatuses_KeepOnlyTheHost(t *testing.T) {
	statuses := providerStatuses([]rpc.ProviderSnapshot{
		{Name: "solana_devnet-1", URL: "https://devnet.helius-rpc.com/?api-key=secret", State: rpc.StateHealthy, AverageResponseTime: 420 * time.Millisecond},
		{Name: "bsc_testnet-1", URL: "https://bsc-testnet.nodereal.io/v1/secret", State: rpc.StateBlacklisted, BlacklistedUntil: time.Now().Add(time.Hour), ConsecutiveErrors: 7},
		{Name: "broken", URL: "::not a url"},
	})

	if statuses[0].Host != "devnet.helius-rpc.com" || statuses[0].AvgLatencyMs != 420 {
		t.Fatalf("unexpected first provider: %+v", statuses[0])
	}
	if statuses[1].Host != "bsc-testnet.nodereal.io" || statuses[1].BlacklistedUntil == nil || statuses[1].ConsecutiveErrors != 7 {
		t.Fatalf("unexpected second provider: %+v", statuses[1])
	}
	if statuses[2].Host != "unknown" {
		t.Fatalf("an unparsable URL must not leak: %+v", statuses[2])
	}
}
