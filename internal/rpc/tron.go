package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fystack/transaction-indexer/internal/common/ratelimiter"
)

type TronClient struct {
	*GenericClient
}

func NewTronClient(url string, auth *AuthConfig, timeout time.Duration, rateLimiter *ratelimiter.PooledRateLimiter) *TronClient {
	return &TronClient{
		GenericClient: NewGenericClient(url, NetworkTron, ClientTypeREST, auth, timeout, rateLimiter),
	}
}

type (
	TronBlock struct {
		BlockID      string            `json:"blockID"`
		BlockHeader  TronBlockHeader   `json:"block_header"`
		Transactions []TronTransaction `json:"transactions"`
	}

	TronBlockHeader struct {
		RawData TronBlockRawData `json:"raw_data"`
	}

	TronBlockRawData struct {
		Number     int64  `json:"number"`
		Timestamp  int64  `json:"timestamp"`
		ParentHash string `json:"parentHash"`
	}

	TronTransaction struct {
		TxID    string                 `json:"txID"`
		RawData TronTransactionRawData `json:"raw_data"`
		Ret     []TronTransactionRet   `json:"ret"`
	}

	TronTransactionRawData struct {
		Contract  []TronContract `json:"contract"`
		Timestamp int64          `json:"timestamp"`
	}

	TronTransactionRet struct {
		ContractRet string `json:"contractRet"`
		Fee         int64  `json:"fee"`
	}

	TronContract struct {
		Parameter TronContractParameter `json:"parameter"`
		Type      string                `json:"type"`
	}

	TronContractParameter struct {
		Value json.RawMessage `json:"value"`
	}

	// Transfer contract types (simplified)
	TronTransferContract struct {
		OwnerAddress string `json:"owner_address"`
		ToAddress    string `json:"to_address"`
		Amount       int64  `json:"amount"`
	}

	TronTransferAssetContract struct {
		OwnerAddress string `json:"owner_address"`
		ToAddress    string `json:"to_address"`
		AssetName    string `json:"asset_name"`
		Amount       int64  `json:"amount"`
	}

	TronTriggerSmartContract struct {
		OwnerAddress    string `json:"owner_address"`
		ContractAddress string `json:"contract_address"`
		Data            string `json:"data"`
	}

	// Simplified transaction info for transfer analysis
	TronTransactionInfo struct {
		ID             string      `json:"id"`
		Fee            int64       `json:"fee"`
		BlockNumber    int64       `json:"blockNumber"`
		BlockTimestamp int64       `json:"blockTimeStamp"`
		Log            []TronLog   `json:"log"`
		Receipt        TronReceipt `json:"receipt"`
	}

	TronReceipt struct {
		EnergyUsage int64 `json:"energy_usage"`
		NetUsage    int64 `json:"net_usage"`
	}

	TronLog struct {
		Address string   `json:"address"`
		Topics  []string `json:"topics"`
		Data    string   `json:"data"`
	}
)

// GetBlockNumber returns the current block number
func (t *TronClient) GetBlockNumber(ctx context.Context) (uint64, error) {
	data, err := t.Post(ctx, "/walletsolidity/getnowblock", nil)
	if err != nil {
		return 0, fmt.Errorf("getNowBlock failed: %w", err)
	}

	var block TronBlock
	if err := json.Unmarshal(data, &block); err != nil {
		return 0, fmt.Errorf("failed to unmarshal block: %w", err)
	}
	return uint64(block.BlockHeader.RawData.Number), nil
}

// GetBlockByNumber returns a block with full transaction data
func (t *TronClient) GetBlockByNumber(ctx context.Context, blockNumber string, detail bool) (*TronBlock, error) {
	body := map[string]any{
		"id_or_num": blockNumber,
		"detail":    detail,
	}
	data, err := t.Post(ctx, "/walletsolidity/getblock", body)
	if err != nil {
		return nil, fmt.Errorf("getBlockByNumber failed: %w", err)
	}
	var block TronBlock
	if err := json.Unmarshal(data, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}
	return &block, nil
}

func (t *TronClient) BatchGetTransactionReceiptsByBlockNum(ctx context.Context, blockNum int64) ([]*TronTransactionInfo, error) {
	body := map[string]any{"num": blockNum}
	data, err := t.Post(ctx, "/walletsolidity/gettransactioninfobyblocknum", body)
	if err != nil {
		return nil, fmt.Errorf("getTransactionInfoByBlockNum failed: %w", err)
	}
	var txs []*TronTransactionInfo
	if err := json.Unmarshal(data, &txs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transactions: %w", err)
	}
	return txs, nil
}

// Failover helpers
func (fm *FailoverManager) AddTronProvider(name, url string, auth *AuthConfig, rateLimiter *ratelimiter.PooledRateLimiter) error {
	return fm.AddProvider(name, url, NetworkTron, ClientTypeREST, auth, rateLimiter)
}

func (fm *FailoverManager) GetTronClient() (*TronClient, error) {
	provider, err := fm.GetBestProvider()
	if err != nil {
		return nil, err
	}
	if provider.Network != NetworkTron {
		return nil, fmt.Errorf("current provider is not a Tron network")
	}
	return &TronClient{GenericClient: provider.Client.(*GenericClient)}, nil
}

func (fm *FailoverManager) ExecuteTronCall(ctx context.Context, fn func(*TronClient) error) error {
	return fm.ExecuteWithRetry(ctx, func(client NetworkClient) error {
		if client.GetNetworkType() != NetworkTron {
			return fmt.Errorf("expected Tron client, got %s", client.GetNetworkType())
		}
		return fn(&TronClient{GenericClient: client.(*GenericClient)})
	})
}
