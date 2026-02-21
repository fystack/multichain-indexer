package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/fystack/multichain-indexer/internal/rpc"
	"github.com/fystack/multichain-indexer/internal/rpc/cosmos"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/shopspring/decimal"
)

var cosmosCoinRegexp = regexp.MustCompile(`^([0-9]+)([a-zA-Z0-9/._:-]+)$`)

const (
	cosmosNativeDenomOsmosis = "uosmo"
	cosmosNativeDenomAtom    = "uatom"

	cosmosNetworkHintOsmosis = "osmosis"
	cosmosNetworkHintOsmo    = "osmo"
	cosmosNetworkHintHub     = "cosmoshub"
	cosmosNetworkHintAtom    = "atom"
	cosmosNetworkHintCosmos  = "cosmos"
	cosmosNetworkHintHubWord = "hub"
	cosmosNetworkHintCosmos1 = "cosmos1"
)

type CosmosIndexer struct {
	chainName   string
	config      config.ChainConfig
	failover    *rpc.Failover[cosmos.CosmosAPI]
	pubkeyStore PubkeyStore
}

func NewCosmosIndexer(
	chainName string,
	cfg config.ChainConfig,
	failover *rpc.Failover[cosmos.CosmosAPI],
	pubkeyStore PubkeyStore,
) *CosmosIndexer {
	return &CosmosIndexer{
		chainName:   chainName,
		config:      cfg,
		failover:    failover,
		pubkeyStore: pubkeyStore,
	}
}

func (c *CosmosIndexer) GetName() string                  { return strings.ToUpper(c.chainName) }
func (c *CosmosIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeCosmos }
func (c *CosmosIndexer) GetNetworkInternalCode() string   { return c.config.InternalCode }

func (c *CosmosIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	var latest uint64
	err := c.failover.ExecuteWithRetry(ctx, func(client cosmos.CosmosAPI) error {
		height, err := client.GetLatestHeight(ctx)
		latest = height
		return err
	})
	return latest, err
}

func (c *CosmosIndexer) GetBlock(ctx context.Context, number uint64) (*types.Block, error) {
	var (
		blockData   *cosmos.BlockResponse
		blockResult *cosmos.BlockResultsResponse
	)

	if err := c.failover.ExecuteWithRetry(ctx, func(client cosmos.CosmosAPI) error {
		b, err := client.GetBlock(ctx, number)
		blockData = b
		return err
	}); err != nil {
		return nil, fmt.Errorf("get cosmos block %d failed: %w", number, err)
	}

	if err := c.failover.ExecuteWithRetry(ctx, func(client cosmos.CosmosAPI) error {
		r, err := client.GetBlockResults(ctx, number)
		blockResult = r
		return err
	}); err != nil {
		if isMissingFinalizeBlockResponsesError(err) {
			fallback, fallbackErr := c.getBlockResultsByTxLookup(ctx, number, blockData.Block.Data.Txs)
			if fallbackErr != nil {
				return nil, fmt.Errorf(
					"get cosmos block_results %d failed: %w; tx lookup fallback failed: %w",
					number,
					err,
					fallbackErr,
				)
			}
			blockResult = fallback
		} else {
			return nil, fmt.Errorf("get cosmos block_results %d failed: %w", number, err)
		}
	}

	if blockData == nil {
		return nil, fmt.Errorf("cosmos block %d not found", number)
	}

	return c.convertBlock(blockData, blockResult)
}

func (c *CosmosIndexer) GetBlocks(
	ctx context.Context,
	from, to uint64,
	isParallel bool,
) ([]BlockResult, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range: from %d > to %d", from, to)
	}

	nums := make([]uint64, 0, to-from+1)
	for n := from; n <= to; n++ {
		nums = append(nums, n)
	}

	workers := 1
	if isParallel {
		workers = c.config.Throttle.Concurrency
	}
	return c.getBlocks(ctx, nums, workers)
}

func (c *CosmosIndexer) GetBlocksByNumbers(
	ctx context.Context,
	blockNumbers []uint64,
) ([]BlockResult, error) {
	return c.getBlocks(ctx, blockNumbers, c.config.Throttle.Concurrency)
}

func (c *CosmosIndexer) getBlocks(
	ctx context.Context,
	blockNumbers []uint64,
	workers int,
) ([]BlockResult, error) {
	if len(blockNumbers) == 0 {
		return nil, nil
	}
	if workers <= 0 {
		workers = 1
	}
	workers = min(workers, len(blockNumbers))

	results := make([]BlockResult, len(blockNumbers))

	type job struct {
		index int
		num   uint64
	}

	jobs := make(chan job, workers*2)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				block, err := c.GetBlock(ctx, j.num)
				results[j.index] = BlockResult{
					Number: j.num,
					Block:  block,
				}
				if err != nil {
					results[j.index].Error = &Error{
						ErrorType: ErrorTypeUnknown,
						Message:   err.Error(),
					}
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for i, num := range blockNumbers {
			select {
			case <-ctx.Done():
				return
			case jobs <- job{index: i, num: num}:
			}
		}
	}()

	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var firstErr error
	for _, res := range results {
		if res.Error != nil {
			firstErr = fmt.Errorf("block %d: %s", res.Number, res.Error.Message)
			break
		}
	}
	return results, firstErr
}

func (c *CosmosIndexer) IsHealthy() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.GetLatestBlockNumber(ctx)
	return err == nil
}

func (c *CosmosIndexer) convertBlock(
	blockData *cosmos.BlockResponse,
	blockResults *cosmos.BlockResultsResponse,
) (*types.Block, error) {
	height, err := strconv.ParseUint(blockData.Block.Header.Height, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid block height %q: %w", blockData.Block.Header.Height, err)
	}

	ts, err := parseCosmosBlockTime(blockData.Block.Header.Time)
	if err != nil {
		return nil, err
	}

	txs := c.extractTransferTransactions(
		height,
		ts,
		hashCosmosTxs(blockData.Block.Data.Txs),
		blockResults,
	)

	return &types.Block{
		Number:       height,
		Hash:         blockData.BlockID.Hash,
		ParentHash:   blockData.Block.Header.LastBlockID.Hash,
		Timestamp:    ts,
		Transactions: txs,
	}, nil
}

func (c *CosmosIndexer) extractTransferTransactions(
	blockNumber uint64,
	timestamp uint64,
	txHashes []string,
	blockResults *cosmos.BlockResultsResponse,
) []types.Transaction {
	if blockResults == nil || len(txHashes) == 0 {
		return nil
	}

	results := blockResults.TxsResults
	limit := min(len(txHashes), len(results))
	if limit == 0 {
		return nil
	}

	transactions := make([]types.Transaction, 0, limit)
	for i := 0; i < limit; i++ {
		txResult := results[i]
		if txResult.Code != 0 {
			continue
		}

		fee := extractCosmosFee(txResult.Events)
		transfers := extractCosmosTransfers(txResult.Events)

		feeAssigned := false
		for _, transfer := range transfers {
			if transfer.sender == "" || transfer.recipient == "" || transfer.amount == "" {
				continue
			}
			if c.pubkeyStore != nil && !c.pubkeyStore.Exist(enum.NetworkTypeCosmos, transfer.recipient) {
				continue
			}

			txType, assetAddress := c.classifyDenom(transfer.denom)
			tx := types.Transaction{
				TxHash:        txHashes[i],
				NetworkId:     c.config.NetworkId,
				BlockNumber:   blockNumber,
				FromAddress:   transfer.sender,
				ToAddress:     transfer.recipient,
				AssetAddress:  assetAddress,
				Amount:        transfer.amount,
				Type:          txType,
				Timestamp:     timestamp,
				Confirmations: 1,
				Status:        types.StatusConfirmed,
			}

			if !feeAssigned {
				tx.TxFee = fee
				feeAssigned = true
			}

			transactions = append(transactions, tx)
		}
	}

	return transactions
}

type cosmosTransfer struct {
	sender    string
	recipient string
	amount    string
	denom     string
}

func extractCosmosTransfers(events []cosmos.Event) []cosmosTransfer {
	transfers := make([]cosmosTransfer, 0)

	for _, event := range events {
		if event.Type != "transfer" {
			continue
		}

		var (
			senders    []string
			recipients []string
			amounts    []string
		)

		for _, attr := range event.Attributes {
			key := strings.ToLower(strings.TrimSpace(decodeCosmosEventValue(attr.Key)))
			value := strings.TrimSpace(decodeCosmosEventValue(attr.Value))
			if value == "" {
				continue
			}

			switch key {
			case "sender":
				senders = append(senders, value)
			case "recipient":
				recipients = append(recipients, value)
			case "amount":
				amounts = append(amounts, value)
			}
		}

		count := min(len(senders), len(recipients), len(amounts))
		for i := 0; i < count; i++ {
			coins := parseCosmosCoins(amounts[i])
			for _, coin := range coins {
				transfers = append(transfers, cosmosTransfer{
					sender:    senders[i],
					recipient: recipients[i],
					amount:    coin.Amount,
					denom:     coin.Denom,
				})
			}
		}
	}

	return transfers
}

type cosmosCoin struct {
	Amount string
	Denom  string
}

func parseCosmosCoins(raw string) []cosmosCoin {
	parts := strings.Split(raw, ",")
	coins := make([]cosmosCoin, 0, len(parts))

	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		matches := cosmosCoinRegexp.FindStringSubmatch(part)
		if len(matches) != 3 {
			continue
		}

		coins = append(coins, cosmosCoin{
			Amount: matches[1],
			Denom:  matches[2],
		})
	}

	return coins
}

func extractCosmosFee(events []cosmos.Event) decimal.Decimal {
	for _, event := range events {
		if event.Type != "tx" {
			continue
		}
		for _, attr := range event.Attributes {
			key := strings.ToLower(strings.TrimSpace(decodeCosmosEventValue(attr.Key)))
			if key != "fee" {
				continue
			}

			value := strings.TrimSpace(decodeCosmosEventValue(attr.Value))
			if value == "" {
				continue
			}

			coins := parseCosmosCoins(value)
			if len(coins) == 0 {
				continue
			}

			fee, err := decimal.NewFromString(coins[0].Amount)
			if err == nil {
				return fee
			}
		}
	}

	return decimal.Zero
}

func decodeCosmosEventValue(raw string) string {
	if raw == "" {
		return ""
	}

	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		if isReadableText(decoded) {
			return string(decoded)
		}
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(raw); err == nil {
		if isReadableText(decoded) {
			return string(decoded)
		}
	}
	return raw
}

func isReadableText(b []byte) bool {
	if len(b) == 0 || !utf8.Valid(b) {
		return false
	}

	for _, r := range string(b) {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			continue
		case r < 32 || r == 127:
			return false
		}
	}
	return true
}

func hashCosmosTxs(encodedTxs []string) []string {
	hashes := make([]string, 0, len(encodedTxs))
	for _, encoded := range encodedTxs {
		if encoded == "" {
			continue
		}

		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(encoded)
			if err != nil {
				continue
			}
		}

		sum := sha256.Sum256(raw)
		hashes = append(hashes, strings.ToUpper(hex.EncodeToString(sum[:])))
	}
	return hashes
}

func parseCosmosBlockTime(raw string) (uint64, error) {
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return 0, fmt.Errorf("invalid block time %q: %w", raw, err)
	}
	return uint64(t.Unix()), nil
}

func (c *CosmosIndexer) classifyDenom(denom string) (constant.TxType, string) {
	nativeDenom := c.nativeDenom()
	if nativeDenom != "" && denom == nativeDenom {
		return constant.TxTypeNativeTransfer, ""
	}
	return constant.TxTypeTokenTransfer, denom
}

func (c *CosmosIndexer) nativeDenom() string {
	if d := strings.TrimSpace(c.config.NativeDenom); d != "" {
		return d
	}

	network := strings.ToLower(c.config.NetworkId + " " + c.chainName)

	switch {
	case strings.Contains(network, cosmosNetworkHintOsmosis), containsCosmosNetworkToken(network, cosmosNetworkHintOsmo):
		return cosmosNativeDenomOsmosis
	case strings.Contains(network, cosmosNetworkHintHub),
		strings.Contains(network, cosmosNetworkHintAtom),
		strings.Contains(network, cosmosNetworkHintCosmos1),
		(containsCosmosNetworkToken(network, cosmosNetworkHintCosmos) &&
			containsCosmosNetworkToken(network, cosmosNetworkHintHubWord)):
		return cosmosNativeDenomAtom
	default:
		return ""
	}
}

func containsCosmosNetworkToken(network, token string) bool {
	if network == "" || token == "" {
		return false
	}

	parts := strings.FieldsFunc(network, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})

	for _, part := range parts {
		if part == token {
			return true
		}
	}
	return false
}

func (c *CosmosIndexer) getBlockResultsByTxLookup(
	ctx context.Context,
	height uint64,
	encodedTxs []string,
) (*cosmos.BlockResultsResponse, error) {
	hashes := hashCosmosTxs(encodedTxs)
	if len(hashes) == 0 {
		return &cosmos.BlockResultsResponse{
			Height:     strconv.FormatUint(height, 10),
			TxsResults: []cosmos.TxResult{},
		}, nil
	}

	workers := c.config.Throttle.Concurrency
	if workers <= 0 {
		workers = 1
	}
	workers = min(workers, len(hashes))

	type job struct {
		index int
		hash  string
	}

	results := make([]cosmos.TxResult, len(hashes))
	errs := make([]error, len(hashes))
	jobs := make(chan job, workers*2)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				var txResp *cosmos.TxResponse
				err := c.failover.ExecuteWithRetry(ctx, func(client cosmos.CosmosAPI) error {
					resp, err := client.GetTxByHash(ctx, j.hash)
					txResp = resp
					return err
				})
				if err != nil {
					errs[j.index] = fmt.Errorf("lookup tx %s: %w", j.hash, err)
					continue
				}
				if txResp == nil {
					errs[j.index] = fmt.Errorf("lookup tx %s: empty response", j.hash)
					continue
				}

				results[j.index] = txResp.TxResult
			}
		}()
	}

	go func() {
		defer close(jobs)
		for i, hash := range hashes {
			select {
			case <-ctx.Done():
				return
			case jobs <- job{index: i, hash: hash}:
			}
		}
	}()

	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	return &cosmos.BlockResultsResponse{
		Height:     strconv.FormatUint(height, 10),
		TxsResults: results,
	}, nil
}

func isMissingFinalizeBlockResponsesError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not persisting finalize block responses")
}
