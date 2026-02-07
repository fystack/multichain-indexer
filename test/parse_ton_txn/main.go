package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	indexer "github.com/fystack/multichain-indexer/internal/indexer/ton"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/liteclient"
	"github.com/xssnick/tonutils-go/ton"
)

const (
	txHashStr = "uoIXlCHFgKwOWjLEQx1E3Wu4gSbhcvFmHkNHlABx21E="
	configURL = "https://ton.org/global.config.json"
)

type TonApiTx struct {
	Hash    string `json:"hash"`
	Lt      uint64 `json:"lt"`
	Account struct {
		Address string `json:"address"`
	} `json:"account"`
}

func main() {
	// 1. Fetch transaction details from tonapi.io to get Account Address and LT
	// This is necessary because liteservers typically require (Account, LT, Hash) to look up a Tx.
	hexHashBytes, err := base64.StdEncoding.DecodeString(txHashStr)
	if err != nil {
		log.Fatalf("Invalid base64 hash: %v", err)
	}
	hexHash := hex.EncodeToString(hexHashBytes)

	fmt.Printf("Fetching details for hash: %s (hex: %s)\n", txHashStr, hexHash)

	resp, err := http.Get("https://tonapi.io/v2/blockchain/transactions/" + hexHash)
	if err != nil {
		log.Fatalf("Failed to query tonapi.io: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		log.Fatalf("tonapi.io returned status %d. Ensure the hash is correct and tonapi is accessible.", resp.StatusCode)
	}

	var apiTx TonApiTx
	if err := json.NewDecoder(resp.Body).Decode(&apiTx); err != nil {
		log.Fatalf("Failed to decode tonapi response: %v", err)
	}

	fmt.Printf("Found Transaction via API:\n  Account: %s\n  LT: %d\n", apiTx.Account.Address, apiTx.Lt)

	// 2. Initialize TON Lite Client
	ctx := context.Background()
	pool := liteclient.NewConnectionPool()
	if err := pool.AddConnectionsFromConfigUrl(ctx, configURL); err != nil {
		log.Fatalf("Failed to convert config url: %v", err)
	}

	// Use the generic API client
	api := ton.NewAPIClient(pool, ton.ProofCheckPolicyFast).WithRetry()

	// 3. Parse target address
	fmt.Printf("Parsing address: '%s'\n", apiTx.Account.Address)

	var addr *address.Address
	addr, err = address.ParseAddr(apiTx.Account.Address)
	if err != nil {
		fmt.Printf("address.ParseAddr failed: %v. Attempting manual raw parse...\n", err)
		// Fallback: Parse raw address manually (assuming 0:hash format)
		var wc int8 = 0 // Default to workchain 0
		var hashHex string

		if len(apiTx.Account.Address) > 2 && apiTx.Account.Address[1] == ':' {
			// Basic check for workchain
			n, _ := fmt.Sscanf(apiTx.Account.Address, "%d:%s", &wc, &hashHex)
			if n != 2 {
				// Try just using the string as hash if scan failed
				hashHex = apiTx.Account.Address
			}
		} else {
			hashHex = apiTx.Account.Address
		}

		hashBytes, hErr := hex.DecodeString(hashHex)
		if hErr == nil && len(hashBytes) == 32 {
			addr = address.NewAddress(0, byte(wc), hashBytes)
			fmt.Println("Manually parsed raw address.")
		} else {
			log.Fatalf("Could not parse address: %v. Raw parse also failed: %v", err, hErr)
		}
	}

	// 4. Fetch the specific transaction
	// ListTransactions retrieves transactions starting from the specified LT/Hash.
	// To get ONLY this transaction, we ask for limit 1 starting at its LT/Hash.
	txs, err := api.ListTransactions(ctx, addr, 1, apiTx.Lt, hexHashBytes)
	if err != nil {
		log.Fatalf("Failed to fetch transaction from liteserver: %v", err)
	}

	if len(txs) == 0 {
		log.Fatalf("Transaction not found on liteserver (archival node might be needed if old).")
	}

	tx := txs[0]
	// Verify it's the right one
	if hex.EncodeToString(tx.Hash) != hexHash {
		fmt.Printf("Warning: Fetched transaction hash mismatch. Expected %s, got %s\n", hexHash, hex.EncodeToString(tx.Hash))
	}

	fmt.Println("\nSuccessfully fetched transaction from Lite Server.")

	// 5. Parse the transaction
	// We use "TON_MAINNET" as network ID for display
	networkID := "TON_MAINNET"

	fmt.Println("\n--- Parsing Results ---")

	// Parse Native TON Transfers
	tonTransfers := indexer.ParseTonTransfer(tx, addr.String(), networkID)
	if len(tonTransfers) > 0 {
		fmt.Println("Native TON Transfers detected:")
		for i, t := range tonTransfers {
			parsedJSON, _ := json.MarshalIndent(t, "", "  ")
			fmt.Printf("[%d] %s\n", i, string(parsedJSON))
		}
	} else {
		fmt.Println("No Native TON Transfers detected.")
	}

	// Parse Jetton Transfers
	// Passing nil for registry means it will treat the Jetton Master address as the wallet address temporarily,
	// or returns "unknown" for symbol, but parsing logic should hold.
	jettonTransfers := indexer.ParseJettonTransfer(tx, addr.String(), networkID, nil)
	if len(jettonTransfers) > 0 {
		fmt.Println("\nJetton Transfers detected:")
		for i, t := range jettonTransfers {
			parsedJSON, _ := json.MarshalIndent(t, "", "  ")
			fmt.Printf("[%d] %s\n", i, string(parsedJSON))
		}
	} else {
		fmt.Println("No Jetton Transfers detected.")
	}
}
