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

// NewTronClient creates a new Tron client
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

// GetNowBlock returns the latest block
func (t *TronClient) GetNowBlock(ctx context.Context) (*TronBlock, error) {
	data, err := t.Post(ctx, "/wallet/getnowblock", nil)
	if err != nil {
		return nil, fmt.Errorf("getNowBlock failed: %w", err)
	}

	var block TronBlock
	if err := json.Unmarshal(data, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}

	return &block, nil
}

// GetBlockByNumber returns a block by its number
func (t *TronClient) GetBlockByNumber(ctx context.Context, blockNumber string, detail bool) (*TronBlock, error) {
	body := map[string]any{
		"id_or_num": blockNumber, // empty string for latest block
		"detail":    detail,      // true for full block details, false for just the block header
	}

	data, err := t.Post(ctx, "/wallet/getblock", body)
	if err != nil {
		return nil, fmt.Errorf("getBlockByNumber failed: %w", err)
	}

	var block TronBlock
	fmt.Println(string(data))
	if err := json.Unmarshal(data, &block); err != nil {
		return nil, fmt.Errorf("failed to unmarshal block: %w", err)
	}

	return &block, nil
}

// TronTransaction represents a Tron transaction
type TronTransaction struct {
	TxID      string                 `json:"txID"`
	RawData   TronTransactionRawData `json:"raw_data"`
	Signature []string               `json:"signature"`
}

type TronTransactionRawData struct {
	Contract      []TronContract `json:"contract"`
	RefBlockBytes string         `json:"ref_block_bytes"`
	RefBlockHash  string         `json:"ref_block_hash"`
	Expiration    int64          `json:"expiration"`
	Timestamp     int64          `json:"timestamp"`
}

type TronContract struct {
	Parameter TronContractParameter `json:"parameter"`
	Type      string                `json:"type"`
}

type TronContractParameter struct {
	Value   map[string]any `json:"value"`
	TypeURL string         `json:"type_url"`
}

// GetTransactionByID returns a transaction by its ID
func (t *TronClient) GetTransactionByID(ctx context.Context, txID string) (*TronTransaction, error) {
	body := map[string]any{
		"value": txID,
	}

	data, err := t.Post(ctx, "/wallet/gettransactionbyid", body)
	if err != nil {
		return nil, fmt.Errorf("getTransactionByID failed: %w", err)
	}

	var tx TronTransaction
	if err := json.Unmarshal(data, &tx); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction: %w", err)
	}

	return &tx, nil
}

// TronTransferContract represents a TRX transfer contract
type TronTransferContract struct {
	OwnerAddress string `json:"owner_address"`
	ToAddress    string `json:"to_address"`
	Amount       int64  `json:"amount"`
}

// CreateTransaction creates a transfer transaction
func (t *TronClient) CreateTransaction(ctx context.Context, from, to string, amount int64) (*TronTransaction, error) {
	body := map[string]any{
		"owner_address": from,
		"to_address":    to,
		"amount":        amount,
		"visible":       true,
	}

	data, err := t.Post(ctx, "/wallet/createtransaction", body)
	if err != nil {
		return nil, fmt.Errorf("createTransaction failed: %w", err)
	}

	var tx TronTransaction
	if err := json.Unmarshal(data, &tx); err != nil {
		return nil, fmt.Errorf("failed to unmarshal transaction: %w", err)
	}

	return &tx, nil
}

// BroadcastTransaction broadcasts a signed transaction
func (t *TronClient) BroadcastTransaction(ctx context.Context, transaction *TronTransaction) (map[string]any, error) {
	data, err := t.Post(ctx, "/wallet/broadcasttransaction", transaction)
	if err != nil {
		return nil, fmt.Errorf("broadcastTransaction failed: %w", err)
	}

	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal broadcast result: %w", err)
	}

	return result, nil
}

// TronChainParameters represents Tron chain parameters
type TronChainParameters struct {
	ChainParameter []TronChainParameter `json:"chainParameter"`
}

type TronChainParameter struct {
	Key   string `json:"key"`
	Value int64  `json:"value"`
}

// GetChainParameters returns chain parameters
func (t *TronClient) GetChainParameters(ctx context.Context) (*TronChainParameters, error) {
	data, err := t.Post(ctx, "/wallet/getchainparameters", nil)
	if err != nil {
		return nil, fmt.Errorf("getChainParameters failed: %w", err)
	}

	var params TronChainParameters
	if err := json.Unmarshal(data, &params); err != nil {
		return nil, fmt.Errorf("failed to unmarshal chain parameters: %w", err)
	}

	return &params, nil
}

// TronNodeInfo represents Tron node information
type TronNodeInfo struct {
	BeginSyncNum        int64              `json:"beginSyncNum"`
	Block               string             `json:"block"`
	SolidityBlock       string             `json:"solidityBlock"`
	CurrentConnectCount int                `json:"currentConnectCount"`
	ActiveConnectCount  int                `json:"activeConnectCount"`
	PassiveConnectCount int                `json:"passiveConnectCount"`
	TotalFlow           int64              `json:"totalFlow"`
	PeerInfoList        []TronPeerInfo     `json:"peerInfoList"`
	ConfigNodeInfo      TronConfigNodeInfo `json:"configNodeInfo"`
}

type TronPeerInfo struct {
	LastSyncBlock           string  `json:"lastSyncBlock"`
	RemainNum               int64   `json:"remainNum"`
	LastBlockUpdateTime     int64   `json:"lastBlockUpdateTime"`
	SyncFlag                bool    `json:"syncFlag"`
	HeadBlockTimeWeBothHave int64   `json:"headBlockTimeWeBothHave"`
	NeedSyncFromPeer        bool    `json:"needSyncFromPeer"`
	NeedSyncFromUs          bool    `json:"needSyncFromUs"`
	Host                    string  `json:"host"`
	Port                    int     `json:"port"`
	NodeId                  string  `json:"nodeId"`
	ConnectTime             int64   `json:"connectTime"`
	AvgLatency              float64 `json:"avgLatency"`
	SyncToUs                int     `json:"syncToUs"`
	ConnectCount            int64   `json:"connectCount"`
	Reputation              int     `json:"reputation"`
	DisconnectTimes         int     `json:"disconnectTimes"`
	BlockInPorcSize         string  `json:"blockInPorcSize"`
	UnFetchSynNum           string  `json:"unFetchSynNum"`
	IsActive                bool    `json:"isActive"`
	Score                   int     `json:"score"`
	NodeCount               int     `json:"nodeCount"`
	InFlow                  int64   `json:"inFlow"`
	DisconnectReason        string  `json:"disconnectReason"`
	OutFlow                 int64   `json:"outFlow"`
}

type TronConfigNodeInfo struct {
	CodeVersion              string  `json:"codeVersion"`
	P2pVersion               string  `json:"p2pVersion"`
	ListenPort               int     `json:"listenPort"`
	DiscoverEnable           bool    `json:"discoverEnable"`
	ActiveNodeSize           int     `json:"activeNodeSize"`
	PassiveNodeSize          int     `json:"passiveNodeSize"`
	SendNodeSize             int     `json:"sendNodeSize"`
	MaxConnectCount          int     `json:"maxConnectCount"`
	SameIpMaxConnectCount    int     `json:"sameIpMaxConnectCount"`
	BackupListenPort         int     `json:"backupListenPort"`
	BackupMemberSize         int     `json:"backupMemberSize"`
	BackupPriority           int     `json:"backupPriority"`
	DbVersion                int     `json:"dbVersion"`
	MinParticipationRate     int     `json:"minParticipationRate"`
	SupportConstant          bool    `json:"supportConstant"`
	MinTimeRatio             float64 `json:"minTimeRatio"`
	MaxTimeRatio             float64 `json:"maxTimeRatio"`
	AllowCreationOfContracts int64   `json:"allowCreationOfContracts"`
	AllowAdaptiveEnergy      int64   `json:"allowAdaptiveEnergy"`
}

// Helper functions for address conversion
func HexToTronAddress(hexAddr string) string {
	// Remove 0x prefix if present
	hexAddr = strings.TrimPrefix(hexAddr, "0x")

	// Convert hex to bytes
	addrBytes, err := hex.DecodeString(hexAddr)
	if err != nil {
		return ""
	}

	// Base58 encode with Tron prefix (0x41)
	// This is a simplified version - in production you'd use proper Base58 encoding
	return hex.EncodeToString(append([]byte{0x41}, addrBytes...))
}

func TronToHexAddress(tronAddr string) string {
	// This is a simplified version - in production you'd decode Base58
	// and convert to hex format
	addrBytes, err := hex.DecodeString(tronAddr)
	if err != nil {
		return ""
	}

	// Remove Tron prefix (0x41) and return hex
	if len(addrBytes) > 1 && addrBytes[0] == 0x41 {
		return "0x" + hex.EncodeToString(addrBytes[1:])
	}

	return "0x" + hex.EncodeToString(addrBytes)
}

// FailoverManager helpers for Tron
// AddTronProvider adds a Tron provider to the failover manager
func (fm *FailoverManager) AddTronProvider(name, url string, auth *AuthConfig, rateLimiter *ratelimiter.PooledRateLimiter) error {
	return fm.AddProvider(name, url, NetworkTron, ClientTypeREST, auth, rateLimiter)
}

// GetTronClient returns the current provider as a Tron client
func (fm *FailoverManager) GetTronClient() (*TronClient, error) {
	provider, err := fm.GetBestProvider()
	if err != nil {
		return nil, err
	}

	if provider.Network != NetworkTron {
		return nil, fmt.Errorf("current provider is not a Tron network")
	}

	// Convert generic client to Tron client
	genericClient := provider.Client.(*GenericClient)
	return &TronClient{GenericClient: genericClient}, nil
}

// ExecuteTronCall executes a function with a Tron client and automatic failover
func (fm *FailoverManager) ExecuteTronCall(ctx context.Context, fn func(*TronClient) error) error {
	return fm.ExecuteWithRetry(ctx, func(client NetworkClient) error {
		if client.GetNetworkType() != NetworkTron {
			return fmt.Errorf("expected Tron client, got %s", client.GetNetworkType())
		}

		tronClient := &TronClient{GenericClient: client.(*GenericClient)}
		return fn(tronClient)
	})
}
