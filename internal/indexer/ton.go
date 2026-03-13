package indexer

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	tonrpc "github.com/fystack/multichain-indexer/internal/rpc/ton"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/constant"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/common/utils"
	"github.com/shopspring/decimal"
	"github.com/xssnick/tonutils-go/address"
	"github.com/xssnick/tonutils-go/tlb"
	tonlib "github.com/xssnick/tonutils-go/ton"
)

const (
	tonMasterchainWorkchain             int32  = -1
	tonJettonTransferOpcode             uint64 = 0x0f8a7ea5
	tonJettonInternalTransferOpcode     uint64 = 0x178d4519
	tonJettonTransferNotificationOpcode uint64 = 0x7362d09c
	tonDefaultTxPageSize                uint32 = 256
	tonDefaultShardScanWorkers                 = 3
	tonDefaultTxFetchConcurrency               = 8
	tonGetTxMaxRetry                           = 3
	tonGetTxRetryDelay                         = 200 * time.Millisecond
	tonGetTxCallTimeout                        = 4 * time.Second
)

type TonIndexer struct {
	chainName        string
	cfg              config.ChainConfig
	client           tonrpc.TonAPI
	pubkeyStore      PubkeyStore
	txPageSize       uint32
	txFetchConc      int
	shardScanWorkers int

	stateMu         sync.Mutex
	shardLastSeqno  map[string]uint32
	initialized     bool
	lastMasterSeqno uint32

	jettonMasterMu sync.RWMutex
	jettonMasters  map[string]string // wallet -> master
	jettonOwners   map[string]string // wallet -> owner
	// preloaded raw TON jetton-wallet addresses derived from (owner wallets x jetton masters)
	trackedJettonWallets map[string]struct{}
}

type tonShardScanRange struct {
	head   *tonlib.BlockIDExt
	blocks []*tonlib.BlockIDExt
}

func NewTonIndexer(
	chainName string,
	cfg config.ChainConfig,
	client tonrpc.TonAPI,
	pubkeyStore PubkeyStore,
	pretrackedJettonWallets []string,
) *TonIndexer {
	pageSize := tonDefaultTxPageSize
	// Do not couple block-range batch size with tx page size when batch is small.
	// Small worker batch_size (blocks/tick) would otherwise explode tx-page RPC calls.
	if cfg.Throttle.BatchSize > int(tonDefaultTxPageSize) {
		pageSize = uint32(cfg.Throttle.BatchSize)
	}

	txFetchConc := cfg.Ton.TxFetchWorkers
	if txFetchConc <= 0 {
		txFetchConc = cfg.Throttle.Concurrency
	}
	if txFetchConc <= 0 {
		txFetchConc = tonDefaultTxFetchConcurrency
	}

	shardScanWorkers := cfg.Ton.ShardScanWorkers
	if shardScanWorkers <= 0 {
		shardScanWorkers = cfg.Throttle.Concurrency
	}
	if shardScanWorkers <= 0 {
		shardScanWorkers = tonDefaultShardScanWorkers
	}

	trackedJettonWallets := make(map[string]struct{}, len(pretrackedJettonWallets))
	for _, wallet := range pretrackedJettonWallets {
		normalized := normalizeTONAddressRaw(wallet)
		if normalized == "" {
			continue
		}
		trackedJettonWallets[normalized] = struct{}{}
	}

	return &TonIndexer{
		chainName:            chainName,
		cfg:                  cfg,
		client:               client,
		pubkeyStore:          pubkeyStore,
		txPageSize:           pageSize,
		txFetchConc:          txFetchConc,
		shardScanWorkers:     shardScanWorkers,
		shardLastSeqno:       make(map[string]uint32),
		jettonMasters:        make(map[string]string),
		jettonOwners:         make(map[string]string),
		trackedJettonWallets: trackedJettonWallets,
	}
}

func (t *TonIndexer) GetName() string                  { return strings.ToUpper(t.chainName) }
func (t *TonIndexer) GetNetworkType() enum.NetworkType { return enum.NetworkTypeTon }
func (t *TonIndexer) GetNetworkInternalCode() string   { return t.cfg.InternalCode }

func (t *TonIndexer) GetLatestBlockNumber(ctx context.Context) (uint64, error) {
	if t.client == nil {
		return 0, fmt.Errorf("ton client is not configured")
	}

	master, err := t.client.GetLatestMasterchainInfo(ctx)
	if err != nil {
		return 0, err
	}

	return uint64(master.SeqNo), nil
}

func (t *TonIndexer) GetBlock(ctx context.Context, number uint64) (*types.Block, error) {
	if t.client == nil {
		return nil, fmt.Errorf("ton client is not configured")
	}
	if number == 0 {
		return nil, fmt.Errorf("invalid block number: 0")
	}
	if number > uint64(^uint32(0)) {
		return nil, fmt.Errorf("block number too large for TON: %d", number)
	}

	master, err := t.client.LookupMasterchainBlock(ctx, uint32(number))
	if err != nil {
		return nil, fmt.Errorf("lookup masterchain block %d: %w", number, err)
	}

	// Avoid pinning to a single lite-server node; allow retry/failover across the pool.
	// This reduces long tail latency when one node is degraded.
	requestCtx := ctx

	t.stateMu.Lock()
	defer t.stateMu.Unlock()

	currentShards, err := t.client.GetBlockShardsInfo(requestCtx, master)
	if err != nil {
		return nil, fmt.Errorf("get block shards for master seqno %d: %w", master.SeqNo, err)
	}

	var (
		ranges            []tonShardScanRange
		shouldAdvanceHead bool
	)

	switch {
	case !t.initialized:
		ranges = shardRangesFromHeads(currentShards)
		shouldAdvanceHead = true
	case master.SeqNo > t.lastMasterSeqno:
		// Realtime path: walk shard parent chains and scan intermediate blocks between
		// previous shard heads and current shard heads to avoid silently skipping txs.
		ranges, err = t.buildRealtimeShardRanges(requestCtx, currentShards)
		if err != nil {
			return nil, err
		}
		shouldAdvanceHead = true
	default:
		// Older block fetch (manual/rescan): scan shards from this master only.
		ranges = shardRangesFromHeads(currentShards)
	}

	allTxs, err := t.scanShardRangesParallel(requestCtx, master.SeqNo, ranges)
	if err != nil {
		return nil, err
	}
	allTxs = utils.DedupTransfers(allTxs)

	blockTS := uint64(time.Now().Unix())
	if len(allTxs) > 0 {
		blockTS = 0
		for _, tx := range allTxs {
			if tx.Timestamp > blockTS {
				blockTS = tx.Timestamp
			}
		}
	}

	if shouldAdvanceHead {
		for _, shard := range currentShards {
			if shard == nil || shard.Workchain == tonMasterchainWorkchain {
				continue
			}
			t.shardLastSeqno[getShardID(shard)] = shard.SeqNo
		}
		t.initialized = true
		if master.SeqNo > t.lastMasterSeqno {
			t.lastMasterSeqno = master.SeqNo
		}
	}

	return &types.Block{
		Number:       uint64(master.SeqNo),
		Hash:         masterBlockHash(master),
		ParentHash:   masterParentHash(master),
		Timestamp:    blockTS,
		Transactions: allTxs,
	}, nil
}

func (t *TonIndexer) GetBlocks(
	ctx context.Context,
	from, to uint64,
	_ bool,
) ([]BlockResult, error) {
	if to < from {
		return nil, fmt.Errorf("invalid range")
	}

	results := make([]BlockResult, 0, to-from+1)
	var firstErr error
	for n := from; n <= to; n++ {
		block, err := t.GetBlock(ctx, n)
		res := BlockResult{
			Number: n,
			Block:  block,
		}
		if err != nil {
			res.Error = toBlockError(err)
			if firstErr == nil {
				firstErr = err
			}
		}
		results = append(results, res)
	}

	return results, firstErr
}

func (t *TonIndexer) GetBlocksByNumbers(
	ctx context.Context,
	blockNumbers []uint64,
) ([]BlockResult, error) {
	if len(blockNumbers) == 0 {
		return nil, nil
	}

	results := make([]BlockResult, 0, len(blockNumbers))
	var firstErr error
	for _, n := range blockNumbers {
		block, err := t.GetBlock(ctx, n)
		res := BlockResult{
			Number: n,
			Block:  block,
		}
		if err != nil {
			res.Error = toBlockError(err)
			if firstErr == nil {
				firstErr = err
			}
		}
		results = append(results, res)
	}

	return results, firstErr
}

func (t *TonIndexer) IsHealthy() bool {
	if t.client == nil {
		return false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := t.GetLatestBlockNumber(ctx)
	return err == nil
}

func shardRangesFromHeads(shards []*tonlib.BlockIDExt) []tonShardScanRange {
	deduped := dedupShardBlocks(shards)
	ranges := make([]tonShardScanRange, 0, len(deduped))
	for _, shard := range deduped {
		if shard == nil || shard.Workchain == tonMasterchainWorkchain {
			continue
		}
		ranges = append(ranges, tonShardScanRange{
			head:   shard,
			blocks: []*tonlib.BlockIDExt{shard},
		})
	}
	return ranges
}

func (t *TonIndexer) buildRealtimeShardRanges(
	ctx context.Context,
	currentShards []*tonlib.BlockIDExt,
) ([]tonShardScanRange, error) {
	deduped := dedupShardBlocks(currentShards)
	lastSeen := t.copyShardLastSeqno()
	scanAssigned := make(map[string]struct{}, len(deduped))
	ranges := make([]tonShardScanRange, 0, len(deduped))

	for _, shard := range deduped {
		if shard == nil || shard.Workchain == tonMasterchainWorkchain {
			continue
		}

		lineage, err := t.collectShardLineage(ctx, shard, lastSeen)
		if err != nil {
			return nil, err
		}
		if len(lineage) == 0 {
			continue
		}

		filtered := make([]*tonlib.BlockIDExt, 0, len(lineage))
		for _, block := range lineage {
			key := shardBlockScanKey(block)
			if _, ok := scanAssigned[key]; ok {
				continue
			}
			scanAssigned[key] = struct{}{}
			filtered = append(filtered, block)
		}
		if len(filtered) == 0 {
			continue
		}

		ranges = append(ranges, tonShardScanRange{
			head:   shard,
			blocks: filtered,
		})
	}

	return ranges, nil
}

func (t *TonIndexer) collectShardLineage(
	ctx context.Context,
	head *tonlib.BlockIDExt,
	lastSeen map[string]uint32,
) ([]*tonlib.BlockIDExt, error) {
	if head == nil {
		return nil, nil
	}

	ordered := make([]*tonlib.BlockIDExt, 0, 4)
	visited := make(map[string]struct{}, 8)

	var walk func(block *tonlib.BlockIDExt) error
	walk = func(block *tonlib.BlockIDExt) error {
		if block == nil || block.Workchain == tonMasterchainWorkchain {
			return nil
		}

		blockKey := shardBlockScanKey(block)
		if _, ok := visited[blockKey]; ok {
			return nil
		}
		visited[blockKey] = struct{}{}

		if stopSeq, ok := lastSeen[getShardID(block)]; ok && block.SeqNo <= stopSeq {
			return nil
		}

		blockData, err := t.client.GetBlockData(ctx, block)
		if err != nil {
			return fmt.Errorf(
				"get TON shard block data wc=%d shard=%x seqno=%d: %w",
				block.Workchain,
				uint64(block.Shard),
				block.SeqNo,
				err,
			)
		}

		parents, err := tonlib.GetParentBlocks(&blockData.BlockInfo)
		if err != nil {
			return fmt.Errorf(
				"resolve TON shard parents wc=%d shard=%x seqno=%d: %w",
				block.Workchain,
				uint64(block.Shard),
				block.SeqNo,
				err,
			)
		}

		for _, parent := range parents {
			if err := walk(parent); err != nil {
				return err
			}
		}

		ordered = append(ordered, block)
		return nil
	}

	if err := walk(head); err != nil {
		return nil, err
	}

	return ordered, nil
}

func (t *TonIndexer) scanShardRangesParallel(
	ctx context.Context,
	masterSeqno uint32,
	ranges []tonShardScanRange,
) ([]types.Transaction, error) {
	if len(ranges) == 0 {
		return nil, nil
	}

	workerCount := t.shardScanWorkers
	if workerCount <= 0 {
		workerCount = tonDefaultShardScanWorkers
	}
	if workerCount > len(ranges) {
		workerCount = len(ranges)
	}

	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan tonShardScanRange, len(ranges))
	for _, r := range ranges {
		if len(r.blocks) == 0 {
			continue
		}
		jobs <- r
	}
	close(jobs)

	var (
		wg       sync.WaitGroup
		outMu    sync.Mutex
		outTxs   []types.Transaction
		firstErr error
		errOnce  sync.Once
	)

	worker := func() {
		defer wg.Done()
		for r := range jobs {
			if scanCtx.Err() != nil {
				return
			}

			rangeTxs, err := t.scanShardRange(scanCtx, masterSeqno, r)
			if err != nil {
				errOnce.Do(func() {
					firstErr = err
					cancel()
				})
				return
			}
			if len(rangeTxs) == 0 {
				continue
			}

			outMu.Lock()
			outTxs = append(outTxs, rangeTxs...)
			outMu.Unlock()
		}
	}

	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go worker()
	}
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return outTxs, nil
}

func (t *TonIndexer) scanShardRange(
	ctx context.Context,
	masterSeqno uint32,
	r tonShardScanRange,
) ([]types.Transaction, error) {
	out := make([]types.Transaction, 0)
	for _, block := range r.blocks {
		txs, err := t.scanShardBlock(ctx, masterSeqno, block)
		if err != nil {
			return nil, fmt.Errorf(
				"scan TON shard block wc=%d shard=%x seqno=%d: %w",
				block.Workchain,
				uint64(block.Shard),
				block.SeqNo,
				err,
			)
		}
		out = append(out, txs...)
	}
	return out, nil
}

func (t *TonIndexer) copyShardLastSeqno() map[string]uint32 {
	out := make(map[string]uint32, len(t.shardLastSeqno))
	for k, v := range t.shardLastSeqno {
		out[k] = v
	}
	return out
}

func (t *TonIndexer) scanShardBlock(
	ctx context.Context,
	masterSeqno uint32,
	shard *tonlib.BlockIDExt,
) ([]types.Transaction, error) {
	var (
		after *tonlib.TransactionID3
		more  = true
		out   []types.Transaction
	)

	for more {
		fetchedIDs, hasMore, err := t.client.GetBlockTransactionsV2(
			ctx,
			masterSeqno,
			shard,
			t.txPageSize,
			after,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"get block tx ids master=%d shard=%x wc=%d: %w",
				masterSeqno,
				uint64(shard.Shard),
				shard.Workchain,
				err,
			)
		}

		more = hasMore
		after = nil
		if hasMore && len(fetchedIDs) > 0 {
			after = fetchedIDs[len(fetchedIDs)-1].ID3()
		}

		prefilteredIDs := t.prefilterTransactionIDs(masterSeqno, shard, fetchedIDs)
		if len(prefilteredIDs) == 0 {
			continue
		}

		fetchedTxs, err := t.fetchTransactions(ctx, masterSeqno, shard, prefilteredIDs)
		if err != nil {
			return nil, err
		}

		for i := range prefilteredIDs {
			tx := fetchedTxs[i]
			if tx == nil {
				continue
			}

			parsed := t.parseMatchedTransactions(ctx, tx)
			for i := range parsed {
				parsed[i].BlockNumber = uint64(masterSeqno)
				if parsed[i].ToAddress == "" {
					continue
				}

				if t.pubkeyStore != nil {
					matched, _, _ := t.matchMonitoredTONAddress(parsed[i].ToAddress)
					if !matched {
						continue
					}
				}

				out = append(out, parsed[i])
			}
		}
	}

	return out, nil
}

func (t *TonIndexer) prefilterTransactionIDs(
	_ uint32,
	shard *tonlib.BlockIDExt,
	in []tonlib.TransactionShortInfo,
) []tonlib.TransactionShortInfo {
	if len(in) == 0 {
		return nil
	}
	if t.pubkeyStore == nil {
		return in
	}

	out := make([]tonlib.TransactionShortInfo, 0, len(in))
	for _, id := range in {
		accAddr := address.NewAddress(0, byte(shard.Workchain), id.Account)
		accountRaw := accAddr.StringRaw()

		monitored, _, _ := t.matchMonitoredTONAddress(accountRaw)
		trackedJettonWallet := t.isTrackedJettonWallet(accountRaw)
		shouldFetch := monitored || trackedJettonWallet

		if shouldFetch {
			out = append(out, id)
		}
	}

	return out
}

func (t *TonIndexer) fetchTransactions(
	ctx context.Context,
	masterSeqno uint32,
	shard *tonlib.BlockIDExt,
	fetchedIDs []tonlib.TransactionShortInfo,
) ([]*tlb.Transaction, error) {
	if len(fetchedIDs) == 0 {
		return nil, nil
	}

	workerCount := t.txFetchConc
	if workerCount <= 0 {
		workerCount = tonDefaultTxFetchConcurrency
	}
	if workerCount > len(fetchedIDs) {
		workerCount = len(fetchedIDs)
	}

	out := make([]*tlb.Transaction, len(fetchedIDs))
	jobs := make(chan int, len(fetchedIDs))
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for i := range jobs {
			id := fetchedIDs[i]
			accAddr := address.NewAddress(0, byte(shard.Workchain), id.Account)

			tx, err := t.getTransactionWithTimeout(ctx, shard, accAddr, id.LT)
			if err != nil {
				if isRetryableTONGetTxError(err) {
				retryLoop:
					for attempt := 2; attempt <= tonGetTxMaxRetry; attempt++ {
						select {
						case <-ctx.Done():
							err = ctx.Err()
							break retryLoop
						case <-time.After(tonGetTxRetryDelay):
						}

						tx, err = t.getTransactionWithTimeout(ctx, shard, accAddr, id.LT)
						if err == nil {
							break
						}
					}
				}

				if err != nil && isSkippableTONGetTxError(err) {
					logger.Warn(
						"skip TON tx fetch due to liteserver resolve error",
						"master_seqno", masterSeqno,
						"shard_seqno", shard.SeqNo,
						"workchain", shard.Workchain,
						"shard", fmt.Sprintf("%x", uint64(shard.Shard)),
						"lt", id.LT,
						"account_raw", accAddr.StringRaw(),
						"err", err,
					)
					continue
				}

				if err != nil {
					select {
					case errCh <- fmt.Errorf("get tx data lt=%d: %w", id.LT, err):
					default:
					}
					continue
				}
			}
			out[i] = tx
		}
	}

	wg.Add(workerCount)
	for i := 0; i < workerCount; i++ {
		go worker()
	}

	for i := range fetchedIDs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	select {
	case err := <-errCh:
		return nil, err
	default:
	}

	return out, nil
}

func (t *TonIndexer) getTransactionWithTimeout(
	ctx context.Context,
	shard *tonlib.BlockIDExt,
	addr *address.Address,
	lt uint64,
) (*tlb.Transaction, error) {
	callCtx, cancel := context.WithTimeout(ctx, tonGetTxCallTimeout)
	defer cancel()
	return t.client.GetTransaction(callCtx, shard, addr, lt)
}

func (t *TonIndexer) isTrackedJettonWallet(walletRaw string) bool {
	normalized := normalizeTONAddressRaw(walletRaw)
	if normalized == "" {
		return false
	}

	t.jettonMasterMu.RLock()
	_, ok := t.trackedJettonWallets[normalized]
	t.jettonMasterMu.RUnlock()
	return ok
}

func (t *TonIndexer) parseMatchedTransactions(
	ctx context.Context,
	tx *tlb.Transaction,
) []types.Transaction {
	if tx == nil || tx.IO.In == nil {
		return nil
	}

	if tx.IO.In.MsgType != tlb.MsgTypeInternal {
		return nil
	}

	intMsg, ok := tx.IO.In.Msg.(*tlb.InternalMessage)
	if !ok || intMsg == nil {
		return nil
	}
	if intMsg.Bounced {
		return nil
	}
	opcode, hasOpcode := messageOpcode(intMsg)

	toAddress := normalizeTONAddressFromObj(intMsg.DstAddr)
	if toAddress == "" {
		return nil
	}

	fromAddress := normalizeTONAddressFromObj(intMsg.SrcAddr)
	base := types.Transaction{
		TxHash:       encodeTONTxHash(tx.Hash),
		NetworkId:    t.networkID(),
		FromAddress:  fromAddress,
		ToAddress:    toAddress,
		AssetAddress: "",
		TxFee: decimal.NewFromBigInt(
			tx.TotalFees.Coins.Nano(),
			0,
		).Div(decimal.NewFromInt(1_000_000_000)),
		Timestamp: uint64(tx.Now),
	}

	// Jetton transfer call to jetton wallet. Destination owner is encoded in payload body.
	// This allows bloom check on owner wallet even when message destination is a jetton wallet.
	if hasOpcode && opcode == tonJettonTransferOpcode {
		amount, destination, ok := parseJettonTransferBody(intMsg)
		destinationOwner := normalizeTONAddressFromObj(destination)
		if !ok || amount == "" || amount == "0" || destinationOwner == "" {
			return nil
		}

		jettonWallet := normalizeTONAddressFromObj(intMsg.DstAddr)
		if jettonWallet == "" {
			return nil
		}

		base.Type = constant.TxTypeTokenTransfer
		base.Amount = amount
		base.ToAddress = destinationOwner
		base.AssetAddress = t.resolveJettonMasterAddress(ctx, jettonWallet)
		return []types.Transaction{base}
	}

	if hasOpcode && opcode == tonJettonTransferNotificationOpcode {
		amount, sender, ok := parseJettonTransferBody(intMsg)
		if !ok || amount == "" || amount == "0" {
			return nil
		}

		if sender != nil {
			if normalizedSender := normalizeTONAddressFromObj(sender); normalizedSender != "" {
				base.FromAddress = normalizedSender
			}
		}

		jettonWallet := normalizeTONAddressFromObj(intMsg.SrcAddr)
		base.Type = constant.TxTypeTokenTransfer
		base.Amount = amount
		base.AssetAddress = t.resolveJettonMasterAddress(ctx, jettonWallet)
		return []types.Transaction{base}
	}

	if hasOpcode && opcode == tonJettonInternalTransferOpcode {
		amount, sender, ok := parseJettonTransferBody(intMsg)
		if !ok || amount == "" || amount == "0" {
			return nil
		}

		jettonWallet := normalizeTONAddressFromObj(intMsg.DstAddr)
		if jettonWallet == "" {
			return nil
		}

		if sender != nil {
			if normalizedSender := normalizeTONAddressFromObj(sender); normalizedSender != "" {
				base.FromAddress = normalizedSender
			}
		}

		if owner := t.resolveJettonOwnerAddress(ctx, jettonWallet); owner != "" {
			base.ToAddress = owner
		}

		base.Type = constant.TxTypeTokenTransfer
		base.Amount = amount
		base.AssetAddress = t.resolveJettonMasterAddress(ctx, jettonWallet)
		return []types.Transaction{base}
	}

	if !isSimpleTransferMessage(intMsg) {
		return nil
	}

	nativeAmount := intMsg.Amount.Nano().String()
	if nativeAmount == "" || nativeAmount == "0" {
		return nil
	}

	base.Type = constant.TxTypeNativeTransfer
	base.Amount = nativeAmount
	return []types.Transaction{base}
}

func (t *TonIndexer) resolveJettonOwnerAddress(ctx context.Context, walletAddress string) string {
	if walletAddress == "" {
		return ""
	}

	t.jettonMasterMu.RLock()
	owner, ok := t.jettonOwners[walletAddress]
	t.jettonMasterMu.RUnlock()
	if ok && owner != "" {
		return owner
	}

	resolvedOwner, resolvedMaster, err := t.client.ResolveJettonWalletData(ctx, walletAddress)
	if err != nil {
		return ""
	}

	normalizedOwner := normalizeTONAddressRaw(resolvedOwner)
	if normalizedOwner == "" {
		normalizedOwner = resolvedOwner
	}

	normalizedMaster := normalizeTONAddressRaw(resolvedMaster)
	if normalizedMaster == "" {
		normalizedMaster = resolvedMaster
	}

	t.jettonMasterMu.Lock()
	if normalizedOwner != "" {
		t.jettonOwners[walletAddress] = normalizedOwner
	}
	if normalizedMaster != "" {
		t.jettonMasters[walletAddress] = normalizedMaster
	}
	t.jettonMasterMu.Unlock()

	return normalizedOwner
}

func (t *TonIndexer) resolveJettonMasterAddress(ctx context.Context, walletAddress string) string {
	if walletAddress == "" {
		return ""
	}

	t.jettonMasterMu.RLock()
	master, ok := t.jettonMasters[walletAddress]
	t.jettonMasterMu.RUnlock()
	if ok && master != "" {
		return master
	}

	resolved, err := t.client.ResolveJettonMasterAddress(ctx, walletAddress)
	if err != nil || resolved == "" {
		return walletAddress
	}

	normalized := normalizeTONAddressRaw(resolved)
	if normalized == "" {
		normalized = resolved
	}

	t.jettonMasterMu.Lock()
	t.jettonMasters[walletAddress] = normalized
	t.jettonMasterMu.Unlock()

	return normalized
}

func (t *TonIndexer) matchMonitoredTONAddress(addr string) (bool, []string, string) {
	if t.pubkeyStore == nil {
		return false, nil, ""
	}

	candidates := tonAddressCandidates(addr)
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if t.pubkeyStore.Exist(enum.NetworkTypeTon, candidate) {
			return true, candidates, candidate
		}
	}

	return false, candidates, ""
}

func toBlockError(err error) *Error {
	if err == nil {
		return nil
	}

	msg := err.Error()
	if strings.Contains(strings.ToLower(msg), "not found") {
		return &Error{
			ErrorType: ErrorTypeBlockNotFound,
			Message:   msg,
		}
	}

	return &Error{
		ErrorType: ErrorTypeUnknown,
		Message:   msg,
	}
}

func getShardID(shard *tonlib.BlockIDExt) string {
	return fmt.Sprintf("%d|%d", shard.Workchain, shard.Shard)
}

func shardBlockScanKey(shard *tonlib.BlockIDExt) string {
	if shard == nil {
		return ""
	}
	return fmt.Sprintf("%d|%d|%d", shard.Workchain, shard.Shard, shard.SeqNo)
}

func dedupShardBlocks(in []*tonlib.BlockIDExt) []*tonlib.BlockIDExt {
	if len(in) <= 1 {
		return in
	}

	seen := make(map[string]struct{}, len(in))
	out := make([]*tonlib.BlockIDExt, 0, len(in))
	for _, shard := range in {
		if shard == nil {
			continue
		}
		key := shardBlockScanKey(shard)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, shard)
	}
	return out
}

func (t *TonIndexer) networkID() string {
	if id := strings.TrimSpace(t.cfg.NetworkId); id != "" {
		return id
	}
	return t.cfg.InternalCode
}

func masterBlockHash(master *tonlib.BlockIDExt) string {
	if master == nil {
		return ""
	}
	return fmt.Sprintf("%x:%x", master.RootHash, master.FileHash)
}

func masterParentHash(master *tonlib.BlockIDExt) string {
	if master == nil || master.SeqNo == 0 {
		return ""
	}
	return fmt.Sprintf("master:%d", master.SeqNo-1)
}

func encodeTONTxHash(hash []byte) string {
	return hex.EncodeToString(hash)
}

func normalizeTONAddressFromObj(addr *address.Address) string {
	if addr == nil {
		return ""
	}
	return normalizeTONAddressRaw(addr.StringRaw())
}

func normalizeTONAddressRaw(addr string) string {
	parsed, err := parseTONAddress(addr)
	if err != nil {
		return ""
	}
	return parsed.StringRaw()
}

func parseTONAddress(addr string) (*address.Address, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("empty address")
	}

	if parsed, err := address.ParseAddr(addr); err == nil {
		return parsed, nil
	}
	if parsed, err := address.ParseRawAddr(addr); err == nil {
		return parsed, nil
	}

	parts := strings.SplitN(addr, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid address format")
	}

	rawHex := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(parts[1])), "0x")
	if rawHex == "" {
		return nil, fmt.Errorf("empty address payload")
	}

	if len(rawHex)%2 == 1 {
		rawHex = "0" + rawHex
	}
	if len(rawHex) > 64 {
		rawHex = rawHex[len(rawHex)-64:]
	} else if len(rawHex) < 64 {
		rawHex = strings.Repeat("0", 64-len(rawHex)) + rawHex
	}

	return address.ParseRawAddr(parts[0] + ":" + rawHex)
}

func messageOpcode(msg *tlb.InternalMessage) (uint64, bool) {
	if msg == nil || msg.Body == nil {
		return 0, false
	}

	bodySlice := msg.Body.BeginParse()
	if bodySlice.BitsLeft() < 32 {
		return 0, false
	}

	opcode, err := bodySlice.LoadUInt(32)
	if err != nil {
		return 0, false
	}
	return opcode, true
}

func isRetryableTONGetTxError(err error) bool {
	return isTONResolveBlockErr(err) || isTONTimeoutErr(err)
}

func isSkippableTONGetTxError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return isTONResolveBlockErr(err) ||
		isTONTimeoutErr(err) ||
		strings.Contains(msg, "lt not in db") ||
		strings.Contains(msg, "cannot compute block with specified transaction")
}

func isTONTimeoutErr(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "timeout")
}

func isTONResolveBlockErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "failed to resolve block") ||
		strings.Contains(msg, "lite server error, code 500")
}

func isSimpleTransferMessage(msg *tlb.InternalMessage) bool {
	if msg == nil || msg.Body == nil {
		return true
	}

	opcode, ok := messageOpcode(msg)
	if !ok {
		return true
	}

	// Simple TON transfer with optional text comment.
	return opcode == 0
}

func parseJettonTransferBody(msg *tlb.InternalMessage) (string, *address.Address, bool) {
	if msg == nil || msg.Body == nil {
		return "", nil, false
	}

	bodySlice := msg.Body.BeginParse()
	if _, err := bodySlice.LoadUInt(32); err != nil { // opcode
		return "", nil, false
	}
	if _, err := bodySlice.LoadUInt(64); err != nil { // query_id
		return "", nil, false
	}

	jettonAmount, err := bodySlice.LoadVarUInt(16)
	if err != nil {
		return "", nil, false
	}

	sender, err := bodySlice.LoadAddr()
	if err != nil {
		return "", nil, false
	}

	return jettonAmount.String(), sender, true
}

func tonAddressCandidates(input string) []string {
	normalizedRaw := normalizeTONAddressRaw(input)
	if normalizedRaw == "" {
		return []string{strings.TrimSpace(input)}
	}

	addr, err := parseTONAddress(normalizedRaw)
	if err != nil || addr == nil {
		return []string{normalizedRaw}
	}

	seen := make(map[string]struct{}, 5)
	out := make([]string, 0, 5)
	add := func(v string) {
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}

	add(normalizedRaw)
	add(addr.Copy().Bounce(true).Testnet(false).String())
	add(addr.Copy().Bounce(false).Testnet(false).String())
	add(addr.Copy().Bounce(true).Testnet(true).String())
	add(addr.Copy().Bounce(false).Testnet(true).String())

	return out
}
