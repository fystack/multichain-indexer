package rpc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestBitcoinClient(t *testing.T, handler func(req *RPCRequest) any) *BitcoinClient {
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
	return NewBitcoinClient(server.URL, nil, 2*time.Second, nil)
}

func TestBitcoin_GetBlockCount(t *testing.T) {
	c := newTestBitcoinClient(t, func(req *RPCRequest) any {
		if req.Method != "getblockcount" {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		return 1000
	})
	n, err := c.GetBlockCount(context.Background())
	if err != nil || n != 1000 {
		t.Fatalf("unexpected: %v %d", err, n)
	}
}

func TestBitcoin_GetBestBlockHash(t *testing.T) {
	c := newTestBitcoinClient(t, func(req *RPCRequest) any {
		if req.Method != "getbestblockhash" {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		return "hash123"
	})
	h, err := c.GetBestBlockHash(context.Background())
	if err != nil || h != "hash123" {
		t.Fatalf("unexpected: %v %s", err, h)
	}
}

func TestBitcoin_GetBlockHash(t *testing.T) {
	c := newTestBitcoinClient(t, func(req *RPCRequest) any {
		if req.Method != "getblockhash" {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		return "hash000a"
	})
	h, err := c.GetBlockHash(context.Background(), 10)
	if err != nil || h != "hash000a" {
		t.Fatalf("unexpected: %v %s", err, h)
	}
}

func TestBitcoin_GetBlock(t *testing.T) {
	expected := BitcoinBlock{Hash: "hash", Height: 1, PreviousBlockHash: "prev", Tx: []string{"t1"}}
	c := newTestBitcoinClient(t, func(req *RPCRequest) any {
		if req.Method != "getblock" {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		return expected
	})
	blk, err := c.GetBlock(context.Background(), "hash")
	if err != nil || blk == nil || blk.Hash != "hash" {
		t.Fatalf("unexpected: %v %+v", err, blk)
	}
}

func TestBitcoin_GetRawTransaction(t *testing.T) {
	c := newTestBitcoinClient(t, func(req *RPCRequest) any {
		if req.Method != "getrawtransaction" {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		return "deadbeef"
	})
	hex, err := c.GetRawTransaction(context.Background(), "txid")
	if err != nil || hex != "deadbeef" {
		t.Fatalf("unexpected: %v %s", err, hex)
	}
}

func TestBitcoin_SendRawTransaction(t *testing.T) {
	c := newTestBitcoinClient(t, func(req *RPCRequest) any {
		if req.Method != "sendrawtransaction" {
			t.Fatalf("unexpected method: %s", req.Method)
		}
		return "txid123"
	})
	txid, err := c.SendRawTransaction(context.Background(), "0102")
	if err != nil || txid != "txid123" {
		t.Fatalf("unexpected: %v %s", err, txid)
	}
}
