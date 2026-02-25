package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	cosmosIBCDenomPrefix     = "ibc/"

	cosmosNetworkHintOsmosis = "osmosis"
	cosmosNetworkHintOsmo    = "osmo"
	cosmosNetworkHintHub     = "cosmoshub"
	cosmosNetworkHintAtom    = "atom"
	cosmosNetworkHintCosmos  = "cosmos"
	cosmosNetworkHintHubWord = "hub"
	cosmosNetworkHintCosmos1 = "cosmos1"

	cosmosIBCTransferMsgAction = "/ibc.applications.transfer.v1.MsgTransfer"
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
	source    cosmosTransferSource
}

type cosmosTransferSource uint8

const (
	cosmosTransferSourceBank cosmosTransferSource = iota + 1
	cosmosTransferSourceCW20
	cosmosTransferSourcePacketRecv
)

func extractCosmosTransfers(events []cosmos.Event) []cosmosTransfer {
	transfers := make([]cosmosTransfer, 0)
	ibcMsgTransferIndexes := extractCosmosIBCMsgTransferIndexes(events)
	hasIBCTransferEvent := hasCosmosEventType(events, "ibc_transfer")
	skipSourceIBCTransfers := hasCosmosEventType(events, "send_packet") && !hasCosmosEventType(events, "recv_packet")
	ibcSourceDenoms := extractCosmosSourceIBCDenoms(events)

	transfers = append(
		transfers,
		extractCosmosBankTransfers(
			events,
			ibcMsgTransferIndexes,
			hasIBCTransferEvent,
			skipSourceIBCTransfers,
			ibcSourceDenoms,
		)...,
	)
	transfers = append(transfers, extractCosmosCW20Transfers(events)...)
	transfers = append(transfers, extractCosmosRecvPacketTransfers(events)...)
	return dedupCosmosTransfers(transfers)
}

func extractCosmosBankTransfers(
	events []cosmos.Event,
	ibcMsgTransferIndexes map[string]struct{},
	hasIBCTransferEvent bool,
	skipSourceIBCTransfers bool,
	ibcSourceDenoms map[string]struct{},
) []cosmosTransfer {
	transfers := make([]cosmosTransfer, 0)

	for _, event := range events {
		if event.Type != "transfer" {
			continue
		}

		var (
			senders    []string
			recipients []string
			amounts    []string
			msgIndexes = make(map[string]struct{})
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
			case "msg_index":
				msgIndexes[value] = struct{}{}
			}
		}

		count := min(len(senders), len(recipients), len(amounts))
		for i := 0; i < count; i++ {
			coins := parseCosmosCoins(amounts[i])
			for _, coin := range coins {
				if shouldSkipIBCSourceTransferCoin(
					msgIndexes,
					ibcMsgTransferIndexes,
					hasIBCTransferEvent,
					skipSourceIBCTransfers,
					coin.Denom,
					ibcSourceDenoms,
				) {
					continue
				}
				transfers = append(transfers, cosmosTransfer{
					sender:    senders[i],
					recipient: recipients[i],
					amount:    coin.Amount,
					denom:     coin.Denom,
					source:    cosmosTransferSourceBank,
				})
			}
		}
	}

	return transfers
}

type cosmosPacketData struct {
	Amount   string `json:"amount"`
	Denom    string `json:"denom"`
	Receiver string `json:"receiver"`
	Sender   string `json:"sender"`
}

func extractCosmosRecvPacketTransfers(events []cosmos.Event) []cosmosTransfer {
	transfers := make([]cosmosTransfer, 0)
	ackByMsgIndex := extractCosmosAckStatusByMsgIndex(events)

	for _, event := range events {
		if event.Type != "recv_packet" {
			continue
		}

		packetDataRaw := ""
		packetDataHex := ""
		msgIndex := ""
		packetSrcPort := ""
		packetSrcChannel := ""
		packetDstPort := ""
		packetDstChannel := ""

		for _, attr := range event.Attributes {
			key := strings.ToLower(strings.TrimSpace(decodeCosmosEventValue(attr.Key)))
			value := strings.TrimSpace(decodeCosmosEventValue(attr.Value))
			if value == "" {
				continue
			}

			switch key {
			case "packet_data":
				packetDataRaw = value
			case "packet_data_hex":
				packetDataHex = value
			case "packet_src_port":
				packetSrcPort = value
			case "packet_src_channel":
				packetSrcChannel = value
			case "packet_dst_port":
				packetDstPort = value
			case "packet_dst_channel":
				packetDstChannel = value
			case "msg_index":
				msgIndex = value
			}
		}

		packetData, ok := parseCosmosPacketData(packetDataRaw, packetDataHex)
		if !ok {
			continue
		}

		denom := strings.TrimSpace(packetData.Denom)
		if ackSuccess, exists := ackByMsgIndex[msgIndex]; exists && !ackSuccess {
			continue
		}
		denom = deriveCosmosRecvDenomFromPacket(
			denom,
			packetSrcPort,
			packetSrcChannel,
			packetDstPort,
			packetDstChannel,
		)

		transfer := cosmosTransfer{
			sender:    strings.TrimSpace(packetData.Sender),
			recipient: strings.TrimSpace(packetData.Receiver),
			amount:    strings.TrimSpace(packetData.Amount),
			denom:     denom,
			source:    cosmosTransferSourcePacketRecv,
		}
		if transfer.sender == "" || transfer.recipient == "" || transfer.amount == "" || transfer.denom == "" {
			continue
		}

		transfers = append(transfers, transfer)
	}

	return transfers
}

func extractCosmosIBCMsgTransferIndexes(events []cosmos.Event) map[string]struct{} {
	msgIndexes := make(map[string]struct{})
	for _, event := range events {
		if event.Type != "message" {
			continue
		}

		action := ""
		msgIndex := ""
		for _, attr := range event.Attributes {
			key := strings.ToLower(strings.TrimSpace(decodeCosmosEventValue(attr.Key)))
			value := strings.TrimSpace(decodeCosmosEventValue(attr.Value))
			if value == "" {
				continue
			}
			switch key {
			case "action":
				action = value
			case "msg_index":
				msgIndex = value
			}
		}

		if action == cosmosIBCTransferMsgAction && msgIndex != "" {
			msgIndexes[msgIndex] = struct{}{}
		}
	}
	return msgIndexes
}

func extractCosmosCW20Transfers(events []cosmos.Event) []cosmosTransfer {
	transfers := make([]cosmosTransfer, 0)

	for _, event := range events {
		if event.Type != "wasm" && !strings.HasPrefix(event.Type, "wasm-") {
			continue
		}

		contractAddress := ""
		action := ""
		from := ""
		to := ""
		amount := ""

		for _, attr := range event.Attributes {
			key := strings.ToLower(strings.TrimSpace(decodeCosmosEventValue(attr.Key)))
			value := strings.TrimSpace(decodeCosmosEventValue(attr.Value))
			if value == "" {
				continue
			}

			switch key {
			case "_contract_address", "contract_address":
				contractAddress = value
			case "action":
				action = strings.ToLower(value)
			case "from", "owner":
				from = value
			case "sender":
				// Keep sender as fallback only; explicit "from"/"owner" takes precedence.
				if from == "" {
					from = value
				}
			case "to", "recipient", "receiver":
				to = value
			case "amount", "value":
				amount = value
			}
		}

		amount = strings.TrimSpace(amount)
		if contractAddress == "" || from == "" || to == "" || !isCosmosUintString(amount) {
			continue
		}
		if action != "" && !isCW20TransferAction(action) {
			continue
		}

		transfers = append(transfers, cosmosTransfer{
			sender:    from,
			recipient: to,
			amount:    amount,
			denom:     contractAddress,
			source:    cosmosTransferSourceCW20,
		})
	}

	return transfers
}

func isCW20TransferAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "transfer", "transfer_from":
		return true
	default:
		return false
	}
}

func isCosmosUintString(v string) bool {
	if v == "" {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func shouldSkipIBCSourceTransferCoin(
	msgIndexes map[string]struct{},
	ibcMsgTransferIndexes map[string]struct{},
	hasIBCTransferEvent bool,
	skipSourceIBCTransfers bool,
	coinDenom string,
	ibcSourceDenoms map[string]struct{},
) bool {
	if !skipSourceIBCTransfers {
		return false
	}

	coinDenomKey := normalizeCosmosDenom(coinDenom)
	if len(ibcSourceDenoms) > 0 {
		if _, ok := ibcSourceDenoms[coinDenomKey]; !ok {
			return false
		}
	} else if !hasIBCTransferEvent {
		return false
	}

	if len(ibcMsgTransferIndexes) == 0 {
		return hasIBCTransferEvent
	}

	for msgIndex := range msgIndexes {
		if _, ok := ibcMsgTransferIndexes[msgIndex]; ok {
			return true
		}
	}

	// Some chains omit msg_index in transfer events; if IBC transfer markers exist in
	// the same tx and this event has no index, treat it as source-side noise.
	if len(msgIndexes) == 0 && hasIBCTransferEvent {
		return true
	}

	return false
}

func hasCosmosEventType(events []cosmos.Event, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func extractCosmosSourceIBCDenoms(events []cosmos.Event) map[string]struct{} {
	denoms := make(map[string]struct{})

	for _, event := range events {
		switch event.Type {
		case "ibc_transfer":
			transferDenom := ""
			amountRaw := ""
			for _, attr := range event.Attributes {
				key := strings.ToLower(strings.TrimSpace(decodeCosmosEventValue(attr.Key)))
				value := strings.TrimSpace(decodeCosmosEventValue(attr.Value))
				if value == "" {
					continue
				}

				switch key {
				case "denom":
					transferDenom = value
				case "amount":
					amountRaw = value
				}
			}

			if transferDenom != "" {
				denoms[normalizeCosmosDenom(transferDenom)] = struct{}{}
				continue
			}

			for _, coin := range parseCosmosCoins(amountRaw) {
				denoms[normalizeCosmosDenom(coin.Denom)] = struct{}{}
			}
		case "send_packet":
			packetDataRaw := ""
			packetDataHex := ""
			for _, attr := range event.Attributes {
				key := strings.ToLower(strings.TrimSpace(decodeCosmosEventValue(attr.Key)))
				value := strings.TrimSpace(decodeCosmosEventValue(attr.Value))
				if value == "" {
					continue
				}

				switch key {
				case "packet_data":
					packetDataRaw = value
				case "packet_data_hex":
					packetDataHex = value
				}
			}

			packetData, ok := parseCosmosPacketData(packetDataRaw, packetDataHex)
			if !ok {
				continue
			}

			if packetData.Denom != "" {
				denoms[normalizeCosmosDenom(packetData.Denom)] = struct{}{}
			}
		}
	}

	return denoms
}

func normalizeCosmosDenom(denom string) string {
	return strings.ToLower(strings.TrimSpace(denom))
}

func extractCosmosAckStatusByMsgIndex(events []cosmos.Event) map[string]bool {
	ackByMsgIndex := make(map[string]bool)

	for _, event := range events {
		if event.Type != "write_acknowledgement" {
			continue
		}

		msgIndex := ""
		packetAckRaw := ""
		packetAckHex := ""

		for _, attr := range event.Attributes {
			key := strings.ToLower(strings.TrimSpace(decodeCosmosEventValue(attr.Key)))
			value := strings.TrimSpace(decodeCosmosEventValue(attr.Value))
			if value == "" {
				continue
			}

			switch key {
			case "msg_index":
				msgIndex = value
			case "packet_ack":
				packetAckRaw = value
			case "packet_ack_hex":
				packetAckHex = value
			}
		}

		if msgIndex == "" {
			continue
		}

		if success, ok := parseCosmosPacketAckSuccess(packetAckRaw, packetAckHex); ok {
			ackByMsgIndex[msgIndex] = success
		}
	}

	return ackByMsgIndex
}

func parseCosmosPacketData(rawJSON, rawHex string) (cosmosPacketData, bool) {
	rawJSON = strings.TrimSpace(rawJSON)
	if rawJSON == "" && rawHex != "" {
		decoded, err := hex.DecodeString(strings.TrimSpace(rawHex))
		if err == nil {
			rawJSON = string(decoded)
		}
	}

	if rawJSON == "" {
		return cosmosPacketData{}, false
	}

	var data cosmosPacketData
	if err := json.Unmarshal([]byte(rawJSON), &data); err != nil {
		return cosmosPacketData{}, false
	}

	return data, true
}

func parseCosmosPacketAckSuccess(rawAck, rawAckHex string) (bool, bool) {
	rawAck = strings.TrimSpace(rawAck)
	if rawAck == "" && rawAckHex != "" {
		decoded, err := hex.DecodeString(strings.TrimSpace(rawAckHex))
		if err == nil {
			rawAck = string(decoded)
		}
	}
	if rawAck == "" {
		return false, false
	}

	var packetAck map[string]any
	if err := json.Unmarshal([]byte(rawAck), &packetAck); err == nil {
		if _, hasError := packetAck["error"]; hasError {
			return false, true
		}
		if _, hasResult := packetAck["result"]; hasResult {
			return true, true
		}
	}

	lower := strings.ToLower(rawAck)
	if strings.Contains(lower, `"error"`) {
		return false, true
	}
	if strings.Contains(lower, `"result"`) {
		return true, true
	}
	return false, false
}

func deriveCosmosRecvDenomFromPacket(
	packetDenom string,
	packetSrcPort string,
	packetSrcChannel string,
	packetDstPort string,
	packetDstChannel string,
) string {
	packetDenom = strings.TrimSpace(packetDenom)
	if packetDenom == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(packetDenom), cosmosIBCDenomPrefix) {
		return packetDenom
	}

	trace := packetDenom
	sourcePort := strings.TrimSpace(packetSrcPort)
	sourceChannel := strings.TrimSpace(packetSrcChannel)
	sourcePrefix := ""
	if sourcePort != "" && sourceChannel != "" {
		sourcePrefix = sourcePort + "/" + sourceChannel + "/"
	}

	if sourcePrefix != "" && strings.HasPrefix(trace, sourcePrefix) {
		trace = strings.TrimPrefix(trace, sourcePrefix)
	} else {
		dstPort := strings.TrimSpace(packetDstPort)
		dstChannel := strings.TrimSpace(packetDstChannel)
		if dstPort != "" && dstChannel != "" {
			trace = dstPort + "/" + dstChannel + "/" + trace
		}
	}

	if !strings.Contains(trace, "/") {
		return trace
	}

	sum := sha256.Sum256([]byte(trace))
	return cosmosIBCDenomPrefix + strings.ToUpper(hex.EncodeToString(sum[:]))
}

func dedupCosmosTransfers(transfers []cosmosTransfer) []cosmosTransfer {
	deduped := make([]cosmosTransfer, 0, len(transfers))
	seenByExactKey := make(map[string]int)
	seenByFlowKey := make(map[string]int)
	seenByIBCReceivePair := make(map[string]int)

	for _, transfer := range transfers {
		exactKey := strings.ToLower(transfer.sender) + "|" +
			strings.ToLower(transfer.recipient) + "|" +
			transfer.amount + "|" +
			transfer.denom

		if idx, exists := seenByExactKey[exactKey]; exists {
			if cosmosTransferSourcePriority(transfer.source) > cosmosTransferSourcePriority(deduped[idx].source) {
				deduped[idx] = transfer
			}
			continue
		}

		// For IBC receive txs, chains often emit both:
		// - bank transfer: escrow/module -> receiver
		// - recv_packet: source sender -> receiver
		// They represent the same credit flow and should be emitted once.
		flowKey := strings.ToLower(transfer.recipient) + "|" + transfer.amount + "|" + transfer.denom
		if idx, exists := seenByFlowKey[flowKey]; exists {
			prev := deduped[idx]
			if cosmosTransferSourcePriority(transfer.source) > cosmosTransferSourcePriority(prev.source) {
				delete(seenByExactKey, strings.ToLower(prev.sender)+"|"+strings.ToLower(prev.recipient)+"|"+prev.amount+"|"+prev.denom)
				deduped[idx] = transfer
				seenByExactKey[exactKey] = idx
			}
			continue
		}

		// Additional dedup for destination-side IBC receive, where one tx may contain:
		// - bank transfer (escrow/module -> user)
		// - recv_packet transfer (source user -> user)
		// Their denoms can differ (e.g. base denom vs packet denom trace), so match by
		// recipient+amount and keep recv_packet as canonical.
		ibcPairKey := strings.ToLower(transfer.recipient) + "|" + transfer.amount
		if idx, exists := seenByIBCReceivePair[ibcPairKey]; exists {
			prev := deduped[idx]
			if isCosmosIBCReceiveDuplicatePair(prev.source, transfer.source) {
				if cosmosTransferSourcePriority(transfer.source) > cosmosTransferSourcePriority(prev.source) {
					delete(seenByExactKey, strings.ToLower(prev.sender)+"|"+strings.ToLower(prev.recipient)+"|"+prev.amount+"|"+prev.denom)
					delete(seenByFlowKey, strings.ToLower(prev.recipient)+"|"+prev.amount+"|"+prev.denom)
					deduped[idx] = transfer
					seenByExactKey[exactKey] = idx
					seenByFlowKey[flowKey] = idx
				}
				continue
			}
		}

		seenByExactKey[exactKey] = len(deduped)
		seenByFlowKey[flowKey] = len(deduped)
		seenByIBCReceivePair[ibcPairKey] = len(deduped)
		deduped = append(deduped, transfer)
	}

	return deduped
}

func isCosmosIBCReceiveDuplicatePair(sourceA, sourceB cosmosTransferSource) bool {
	return (sourceA == cosmosTransferSourceBank && sourceB == cosmosTransferSourcePacketRecv) ||
		(sourceA == cosmosTransferSourcePacketRecv && sourceB == cosmosTransferSourceBank)
}

func cosmosTransferSourcePriority(source cosmosTransferSource) int {
	switch source {
	case cosmosTransferSourcePacketRecv:
		return 4
	case cosmosTransferSourceCW20:
		return 2
	case cosmosTransferSourceBank:
		return 1
	default:
		return 0
	}
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
	if fee, ok := extractCosmosFeeFromEvent(events, "tx", "fee"); ok {
		return fee
	}
	if fee, ok := extractCosmosFeeFromEvent(events, "fee_pay", "fee"); ok {
		return fee
	}
	return decimal.Zero
}

func extractCosmosFeeFromEvent(events []cosmos.Event, eventType, feeKey string) (decimal.Decimal, bool) {
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		for _, attr := range event.Attributes {
			key := strings.ToLower(strings.TrimSpace(decodeCosmosEventValue(attr.Key)))
			if key != feeKey {
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
				return fee, true
			}
		}
	}
	return decimal.Zero, false
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
