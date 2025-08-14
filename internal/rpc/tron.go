package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"idx/internal/common/ratelimiter"
	"strings"
	"time"
)

// Tron specific types and methods
type TronClient struct {
	*GenericClient
}

func NewTronClient(url string, auth *AuthConfig, timeout time.Duration, rateLimiter *ratelimiter.PooledRateLimiter) *TronClient {
	return &TronClient{
		GenericClient: NewGenericClient(url, NetworkTron, ClientTypeREST, auth, timeout, rateLimiter),
	}
}

// TronBlock represents a Tron block
type TronBlock struct {
	BlockID      string            `json:"blockID"`
	BlockHeader  TronBlockHeader   `json:"block_header"`
	Transactions []TronTransaction `json:"transactions"`
}

type TronBlockHeader struct {
	RawData          TronBlockRawData `json:"raw_data"`
	WitnessSignature string           `json:"witness_signature"`
}

type TronBlockRawData struct {
	Number         int64  `json:"number"`
	TxTrieRoot     string `json:"txTrieRoot"`
	WitnessAddress string `json:"witness_address"`
	ParentHash     string `json:"parentHash"`
	Version        int    `json:"version"`
	Timestamp      int64  `json:"timestamp"`
}

type TronTransaction struct {
	TxID       string                 `json:"txID"`
	RawData    TronTransactionRawData `json:"raw_data"`
	Ret        []TronTransactionRet   `json:"ret"`
	Signature  []string               `json:"signature"`
	RawDataHex string                 `json:"raw_data_hex"`
}

type TronTransactionRet struct {
	ContractRet string `json:"contractRet"`
	Fee         int64  `json:"fee"`
}

type TronTransactionRawData struct {
	Contract      []TronContract `json:"contract"`
	RefBlockBytes string         `json:"ref_block_bytes"`
	RefBlockHash  string         `json:"ref_block_hash"`
	Expiration    int64          `json:"expiration"`
	Timestamp     int64          `json:"timestamp"`
	FeeLimit      int64          `json:"fee_limit,omitempty"`
}

type TronContract struct {
	Parameter TronContractParameter `json:"parameter"`
	Type      string                `json:"type"`
}

// TronContractParameter represents contract parameters for different contract types
type TronContractParameter struct {
	Value   json.RawMessage `json:"value"`
	TypeURL string          `json:"type_url"`
}

// Specific contract parameter types
type TronTransferContract struct {
	OwnerAddress string `json:"owner_address"`
	ToAddress    string `json:"to_address"`
	Amount       int64  `json:"amount"`
}

// TRC10 transfer contract
type TronTransferAssetContract struct {
	OwnerAddress string `json:"owner_address"`
	ToAddress    string `json:"to_address"`
	AssetName    string `json:"asset_name"`
	Amount       int64  `json:"amount"`
}

type TronTriggerSmartContract struct {
	OwnerAddress    string `json:"owner_address"`
	ContractAddress string `json:"contract_address"`
	Data            string `json:"data"`
	CallValue       int64  `json:"call_value,omitempty"`
	CallTokenValue  int64  `json:"call_token_value,omitempty"`
	TokenID         int64  `json:"token_id,omitempty"`
}

type TronCreateSmartContract struct {
	OwnerAddress   string                `json:"owner_address"`
	NewContract    TronSmartContractSpec `json:"new_contract"`
	CallTokenValue int64                 `json:"call_token_value,omitempty"`
	TokenID        int64                 `json:"token_id,omitempty"`
}

type TronSmartContractSpec struct {
	Origin                     string `json:"origin"`
	ContractAddress            string `json:"contract_address"`
	ABI                        string `json:"abi"`
	Bytecode                   string `json:"bytecode"`
	CallValue                  int64  `json:"call_value"`
	ConsumeUserResourcePercent int64  `json:"consume_user_resource_percent"`
	Name                       string `json:"name"`
	OriginEnergyLimit          int64  `json:"origin_energy_limit"`
}

type TronFreezeBalanceContract struct {
	OwnerAddress    string `json:"owner_address"`
	FrozenBalance   int64  `json:"frozen_balance"`
	FrozenDuration  int64  `json:"frozen_duration"`
	Resource        string `json:"resource"`
	ReceiverAddress string `json:"receiver_address,omitempty"`
}

type TronUnfreezeBalanceContract struct {
	OwnerAddress    string `json:"owner_address"`
	Resource        string `json:"resource"`
	ReceiverAddress string `json:"receiver_address,omitempty"`
}

func (t *TronClient) GetNowBlock(ctx context.Context) (uint64, error) {
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

// TronTransactionInfo represents a Tron transaction
type TronTransactionInfo struct {
	ID                     string                    `json:"id"`
	Fee                    int64                     `json:"fee"`
	BlockNumber            int64                     `json:"blockNumber"`
	BlockTimestamp         int64                     `json:"blockTimestamp"`
	ContractResult         []string                  `json:"contractResult"`
	ContractAddress        string                    `json:"contract_address"`
	Receipt                TronResourceReceipt       `json:"receipt"`
	Log                    []TronLog                 `json:"log"`
	Result                 string                    `json:"result"`
	ResMessage             string                    `json:"resMessage"`
	WithdrawAmount         int64                     `json:"withdraw_amount"`
	UnfreezeAmount         int64                     `json:"unfreeze_amount"`
	InternalTransactions   []TronInternalTransaction `json:"internal_transactions"`
	WithdrawExpireAmount   int64                     `json:"withdraw_expire_amount"`
	CancelUnfreezeV2Amount int64                     `json:"cancel_unfreeze_v2_amount"`
	// Additional fields that might be present in transaction info
	PackingFee       int64 `json:"packing_fee,omitempty"`
	EnergyUsage      int64 `json:"energy_usage,omitempty"`
	EnergyFee        int64 `json:"energy_fee,omitempty"`
	EnergyUsageTotal int64 `json:"energy_usage_total,omitempty"`
	NetUsage         int64 `json:"net_usage,omitempty"`
	NetFee           int64 `json:"net_fee,omitempty"`
}

type TronResourceReceipt struct {
	EnergyUsage        int64  `json:"energy_usage"`
	EnergyFee          int64  `json:"energy_fee"`
	OriginEnergyUsage  int64  `json:"origin_energy_usage"`
	EnergyUsageTotal   int64  `json:"energy_usage_total"`
	NetUsage           int64  `json:"net_usage"`
	NetFee             int64  `json:"net_fee"`
	Result             string `json:"result"`
	EnergyPenaltyTotal int64  `json:"energy_penalty_total"`
}

type TronLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
	// Additional fields for log entries
	BlockNumber      int64  `json:"blockNumber,omitempty"`
	TransactionHash  string `json:"transactionHash,omitempty"`
	TransactionIndex int    `json:"transactionIndex,omitempty"`
	BlockHash        string `json:"blockHash,omitempty"`
	LogIndex         int    `json:"logIndex,omitempty"`
	Removed          bool   `json:"removed,omitempty"`
}

type TronInternalTransaction struct {
	Hash              string              `json:"hash"`
	CallerAddress     string              `json:"caller_address"`
	TransferToAddress string              `json:"transferTo_address"`
	CallValueInfo     []TronCallValueInfo `json:"callValueInfo"`
	Note              string              `json:"note"`
	Rejected          bool                `json:"rejected"`
	Extra             string              `json:"extra"`
}

type TronCallValueInfo struct {
	CallValue int64  `json:"callValue"`
	TokenName string `json:"tokenName,omitempty"`
}

func (t *TronClient) GetTransactionInfoByID(ctx context.Context, txID string) (*TronTransactionInfo, error) {
	body := map[string]any{"value": txID}
	data, err := t.Post(ctx, "/walletsolidity/gettransactioninfobyid", body)
	if err != nil {
		return nil, fmt.Errorf("getTransactionInfoByID failed: %w", err)
	}
	var tx TronTransactionInfo
	if err := json.Unmarshal(data, &tx); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction: %w", err)
	}
	return &tx, nil
}

func (t *TronClient) GetTransactionInfoByBlockNum(ctx context.Context, blockNum int64) ([]*TronTransactionInfo, error) {
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

// Helper method to parse contract parameter based on contract type
func (cp *TronContractParameter) ParseTransferContract() (*TronTransferContract, error) {
	var transfer TronTransferContract
	if err := json.Unmarshal(cp.Value, &transfer); err != nil {
		return nil, err
	}
	return &transfer, nil
}

func (cp *TronContractParameter) ParseTransferAssetContract() (*TronTransferAssetContract, error) {
	var transfer TronTransferAssetContract
	if err := json.Unmarshal(cp.Value, &transfer); err != nil {
		return nil, err
	}
	return &transfer, nil
}

func (cp *TronContractParameter) ParseTriggerSmartContract() (*TronTriggerSmartContract, error) {
	var trigger TronTriggerSmartContract
	if err := json.Unmarshal(cp.Value, &trigger); err != nil {
		return nil, err
	}
	return &trigger, nil
}

func (cp *TronContractParameter) ParseCreateSmartContract() (*TronCreateSmartContract, error) {
	var create TronCreateSmartContract
	if err := json.Unmarshal(cp.Value, &create); err != nil {
		return nil, err
	}
	return &create, nil
}

func (cp *TronContractParameter) ParseFreezeBalanceContract() (*TronFreezeBalanceContract, error) {
	var freeze TronFreezeBalanceContract
	if err := json.Unmarshal(cp.Value, &freeze); err != nil {
		return nil, err
	}
	return &freeze, nil
}

func (cp *TronContractParameter) ParseUnfreezeBalanceContract() (*TronUnfreezeBalanceContract, error) {
	var unfreeze TronUnfreezeBalanceContract
	if err := json.Unmarshal(cp.Value, &unfreeze); err != nil {
		return nil, err
	}
	return &unfreeze, nil
}

// Address conversion helpers
func HexToTronAddress(hexAddr string) string {
	hexAddr = strings.TrimPrefix(hexAddr, "0x")
	addrBytes, err := hex.DecodeString(hexAddr)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(append([]byte{0x41}, addrBytes...))
}

func TronToHexAddress(tronAddr string) string {
	addrBytes, err := hex.DecodeString(tronAddr)
	if err != nil {
		return ""
	}
	if len(addrBytes) > 1 && addrBytes[0] == 0x41 {
		return "0x" + hex.EncodeToString(addrBytes[1:])
	}
	return "0x" + hex.EncodeToString(addrBytes)
}

// Additional helper methods for working with Tron data
func (t *TronClient) GetAccount(ctx context.Context, address string) (map[string]interface{}, error) {
	body := map[string]any{"address": address}
	data, err := t.Post(ctx, "/walletsolidity/getaccount", body)
	if err != nil {
		return nil, fmt.Errorf("getAccount failed: %w", err)
	}

	var account map[string]interface{}
	if err := json.Unmarshal(data, &account); err != nil {
		return nil, fmt.Errorf("failed to unmarshal account: %w", err)
	}
	return account, nil
}

func (t *TronClient) GetTransactionByID(ctx context.Context, txID string) (*TronTransaction, error) {
	body := map[string]any{"value": txID}
	data, err := t.Post(ctx, "/walletsolidity/gettransactionbyid", body)
	if err != nil {
		return nil, fmt.Errorf("getTransactionByID failed: %w", err)
	}

	var tx TronTransaction
	if err := json.Unmarshal(data, &tx); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction: %w", err)
	}
	return &tx, nil
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
