package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/fystack/multichain-indexer/internal/indexer"
	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/fystack/multichain-indexer/pkg/common/enum"
	"github.com/fystack/multichain-indexer/pkg/common/logger"
	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/events"
	"github.com/fystack/multichain-indexer/pkg/infra"
	"github.com/fystack/multichain-indexer/pkg/retry"
	"github.com/fystack/multichain-indexer/pkg/store/blockstore"
	"github.com/fystack/multichain-indexer/pkg/store/pubkeystore"
)

// WorkerMode defines the operating mode for a worker.
type WorkerMode string

const (
	ModeRegular WorkerMode = "regular"
	ModeCatchup WorkerMode = "catchup"
	ModeManual  WorkerMode = "manual"
	ModeMempool WorkerMode = "mempool"
)

// Worker is the interface implemented by all worker types.
type Worker interface {
	Start()
	Stop()
}

type WalletReloadSource string

const (
	WalletReloadSourceKV WalletReloadSource = "kv"
	WalletReloadSourceDB WalletReloadSource = "db"
)

var (
	ErrNoTonWorkerConfigured = errors.New("no ton worker configured")
	ErrTonWorkerNotFound     = errors.New("ton worker not found")
)

func (s WalletReloadSource) Normalize() WalletReloadSource {
	if s == WalletReloadSourceDB {
		return WalletReloadSourceDB
	}
	return WalletReloadSourceKV
}

func (s WalletReloadSource) IsValid() bool {
	return s == WalletReloadSourceKV || s == WalletReloadSourceDB
}

type TonWalletReloadRequest struct {
	Source      WalletReloadSource
	ChainFilter string
}

type TonWalletReloadResult struct {
	Chain           string `json:"chain"`
	ReloadedWallets int    `json:"reloaded_wallets"`
	Error           string `json:"error,omitempty"`
}

type TonJettonReloadRequest struct {
	ChainFilter string
}

type TonJettonReloadResult struct {
	Chain           string `json:"chain"`
	ReloadedJettons int    `json:"reloaded_jettons"`
	Error           string `json:"error,omitempty"`
}

// TonWalletReloader is implemented by TON workers that support runtime wallet cache reload.
type TonWalletReloader interface {
	Worker
	GetName() string
	GetNetworkType() enum.NetworkType
	ReloadWalletsFromKV(ctx context.Context) (int, error)
	ReloadWalletsFromDB(ctx context.Context) (int, error)
}

// TonJettonReloader is implemented by TON workers that support runtime jetton registry reload.
type TonJettonReloader interface {
	Worker
	GetName() string
	GetNetworkType() enum.NetworkType
	ReloadJettons(ctx context.Context) (int, error)
}

// WorkerDeps groups shared dependencies used by block-based workers.
type WorkerDeps struct {
	Chain        indexer.Indexer
	Config       config.ChainConfig
	BlockStore   blockstore.Store
	PubkeyStore  pubkeystore.Store
	Emitter      events.Emitter
	FailureQueue FailureQueue
	Redis        infra.RedisClient
}

// FailureQueue captures blocks that failed processing for deferred recovery.
type FailureQueue interface {
	EnqueueFailedBlock(ctx context.Context, chainCode string, blockNumber uint64) error
}

type redisFailureQueue struct {
	client infra.RedisClient
}

func NewRedisFailureQueue(client infra.RedisClient) FailureQueue {
	if client == nil {
		return nil
	}
	return &redisFailureQueue{client: client}
}

func (q *redisFailureQueue) EnqueueFailedBlock(ctx context.Context, chainCode string, blockNumber uint64) error {
	key := fmt.Sprintf("failed_blocks:%s", chainCode)
	return q.client.GetClient().LPush(ctx, key, blockNumber).Err()
}

const (
	defaultCatchupRangeSize   = 20
	defaultCatchupWorkerCount = 3
	catchupProgressSaveEvery  = 1
)

func formatRange(start, end uint64) string {
	return fmt.Sprintf("%d-%d", start, end)
}

// splitCatchupRange splits a large catchup range into smaller chunks.
func splitCatchupRange(r blockstore.CatchupRange, maxSize uint64) []blockstore.CatchupRange {
	if r.End <= r.Start {
		return []blockstore.CatchupRange{r}
	}

	totalBlocks := r.End - r.Start + 1
	if totalBlocks <= maxSize {
		return []blockstore.CatchupRange{r}
	}

	var ranges []blockstore.CatchupRange
	current := r.Start
	for current <= r.End {
		end := min(current+maxSize-1, r.End)

		newCurrent := r.Current
		if newCurrent < current {
			newCurrent = current - 1
		} else if newCurrent > end {
			newCurrent = end
		}

		ranges = append(ranges, blockstore.CatchupRange{Start: current, End: end, Current: newCurrent})
		current = end + 1
	}
	return ranges
}

// BaseWorker holds the common state and logic shared by all worker types.
type BaseWorker struct {
	ctx    context.Context
	cancel context.CancelFunc
	mode   WorkerMode
	logger *slog.Logger

	deps WorkerDeps
}

// Stop stops the worker and cleans up internal resources.
func (bw *BaseWorker) Stop() {
	bw.cancel()
	bw.logger.Info("Worker stopped", "chain", bw.deps.Chain.GetName())
}

// newWorkerWithMode constructs a BaseWorker with the given mode and logger.
func newWorkerWithMode(ctx context.Context, deps WorkerDeps, mode WorkerMode) *BaseWorker {
	ctx, cancel := context.WithCancel(ctx)
	log := logger.With(
		slog.String("mode", strings.ToUpper(string(mode))),
		slog.String("chain", deps.Chain.GetName()),
	)

	return &BaseWorker{
		ctx:    ctx,
		cancel: cancel,
		mode:   mode,
		logger: log,
		deps:   deps,
	}
}

// run executes the given job repeatedly at PollInterval with error handling.
func (bw *BaseWorker) run(job func() error) {
	ticker := time.NewTicker(bw.deps.Config.PollInterval)
	defer ticker.Stop()

	const retryInterval = 2 * time.Second

	for {
		select {
		case <-bw.ctx.Done():
			bw.logger.Info("Context done, stopping worker loop")
			return
		case <-ticker.C:
			start := time.Now()

			if err := retry.Exponential(job, retry.ExponentialConfig{
				InitialInterval: retryInterval,
				MaxElapsedTime:  bw.deps.Config.PollInterval * 4,
				OnRetry: func(err error, next time.Duration) {
					bw.logger.Debug("Retrying job", "err", err, "next_retry_in", next)
				},
			}); err != nil {
				bw.logger.Error("Job error", "err", err)
				_ = bw.deps.Emitter.EmitError(bw.deps.Chain.GetName(), err)
			}

			if elapsed := time.Since(start); elapsed < bw.deps.Config.PollInterval {
				time.Sleep(bw.deps.Config.PollInterval - elapsed)
			}
		}
	}
}

// handleBlockResult processes a block result and persists/forwards errors if needed.
func (bw *BaseWorker) handleBlockResult(result indexer.BlockResult) bool {
	if result.Error != nil {
		_ = bw.deps.BlockStore.SaveFailedBlock(bw.deps.Chain.GetNetworkInternalCode(), result.Number)

		if bw.deps.FailureQueue != nil {
			if err := bw.deps.FailureQueue.EnqueueFailedBlock(
				bw.ctx,
				bw.deps.Chain.GetNetworkInternalCode(),
				result.Number,
			); err != nil {
				bw.logger.Warn("Failed to enqueue failed block",
					"chain", bw.deps.Chain.GetName(),
					"block", result.Number,
					"err", err,
				)
			}
		}

		bw.logger.Error("Failed to process block",
			"chain", bw.deps.Chain.GetName(),
			"block", result.Number,
			"err", result.Error.Message,
		)
		return false
	}

	if result.Block == nil {
		bw.logger.Error("Nil block result",
			"chain", bw.deps.Chain.GetName(),
			"block", result.Number,
		)
		return false
	}

	bw.emitBlock(result.Block)

	bw.logger.Info("Processed block successfully",
		"chain", bw.deps.Chain.GetName(),
		"block", result.Block.Number,
	)
	return true
}

// emitBlock emits relevant transactions for subscribed addresses.
// Only emits transactions where ToAddress is monitored (incoming deposits).
func (bw *BaseWorker) emitBlock(block *types.Block) {
	if block == nil || bw.deps.PubkeyStore == nil {
		return
	}

	addressType := bw.deps.Chain.GetNetworkType()
	for _, tx := range block.Transactions {
		toMonitored := tx.ToAddress != "" && bw.deps.PubkeyStore.Exist(addressType, tx.ToAddress)

		if !toMonitored {
			continue
		}

		bw.logger.Info("Emitting matched transaction",
			"direction", "incoming",
			"from", tx.FromAddress,
			"to", tx.ToAddress,
			"chain", bw.deps.Chain.GetName(),
			"addressType", addressType,
			"txhash", tx.TxHash,
			"status", tx.Status,
			"confirmations", tx.Confirmations,
		)
		_ = bw.deps.Emitter.EmitTransaction(bw.deps.Chain.GetName(), &tx)
	}
}

func (bw *BaseWorker) processBatchResults(start uint64, results []indexer.BlockResult, skipSolanaNotFound bool) uint64 {
	lastSuccess := start - 1

	for _, res := range results {
		if skipSolanaNotFound && bw.isSolanaSkippedSlot(res) {
			if res.Number > lastSuccess {
				lastSuccess = res.Number
			}
			continue
		}

		if bw.handleBlockResult(res) && res.Number > lastSuccess {
			lastSuccess = res.Number
		}
	}

	return lastSuccess
}

func (bw *BaseWorker) isSolanaSkippedSlot(res indexer.BlockResult) bool {
	return res.Error != nil &&
		res.Error.ErrorType == indexer.ErrorTypeBlockNotFound &&
		bw.deps.Chain.GetNetworkType() == enum.NetworkTypeSol
}
