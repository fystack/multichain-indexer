package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestSolanaClient(t *testing.T, handler func(req *RPCRequest) any) *SolanaClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var rpcReq RPCRequest
		if err := json.NewDecoder(r.Body).Decode(&rpcReq); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		result := handler(&rpcReq)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jsonrpc": "2.0",
			"id":      rpcReq.ID,
			"result":  result,
		})
	}))
	t.Cleanup(server.Close)
	return NewSolanaClient(server.URL, nil, 2*time.Second, nil)
}

func TestSolana_GetSlot(t *testing.T) {
	c := newTestSolanaClient(t, func(req *RPCRequest) any {
		if req.Method != "getSlot" {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		return 12345
	})
	slot, err := c.GetSlot(context.Background())
	if err != nil || slot != 12345 {
		t.Fatalf("unexpected: %v %d", err, slot)
	}
}

func TestSolana_GetBalance(t *testing.T) {
	c := newTestSolanaClient(t, func(req *RPCRequest) any {
		if req.Method != "getBalance" {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		return map[string]any{
			"context": map[string]any{"slot": 100},
			"value":   5000000,
		}
	})
	v, err := c.GetBalance(context.Background(), "someAddr")
	if err != nil || v != 5000000 {
		t.Fatalf("unexpected: %v %d", err, v)
	}
}

func TestSolana_GetBlock(t *testing.T) {
	expected := SolanaBlock{Blockhash: "h1", PreviousBlockhash: "h0", ParentSlot: 999}
	c := newTestSolanaClient(t, func(req *RPCRequest) any {
		if req.Method != "getBlock" {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		return expected
	})
	blk, err := c.GetBlock(context.Background(), 1000)
	if err != nil || blk == nil || blk.Blockhash != "h1" {
		t.Fatalf("unexpected: %v %+v", err, blk)
	}
}

func TestSolana_GetTransaction(t *testing.T) {
	expected := SolanaTransaction{Slot: 1, Signature: "sig"}
	c := newTestSolanaClient(t, func(req *RPCRequest) any {
		if req.Method != "getTransaction" {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		return expected
	})
	tx, err := c.GetTransaction(context.Background(), "sig")
	if err != nil || tx == nil || tx.Signature != "sig" {
		t.Fatalf("unexpected: %v %+v", err, tx)
	}
}

func TestSolana_SendTransaction(t *testing.T) {
	c := newTestSolanaClient(t, func(req *RPCRequest) any {
		if req.Method != "sendTransaction" {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		return "sig123"
	})
	sig, err := c.SendTransaction(context.Background(), "b64tx")
	if err != nil || sig != "sig123" {
		t.Fatalf("unexpected: %v %s", err, sig)
	}
}
