package indexer

import (
	"fmt"
	"time"
)

// RescannerWorker periodically re-checks recent blocks to find missed events
// This implementation scans a small window behind the latest block and re-emits matches.
// It relies on BaseWorker.run loop via Start() wiring (ModeRescanner not started yet by main).

type RescannerWorker struct {
	*BaseWorker
	window uint64 // how many blocks behind latest to rescan per tick
}

func NewRescannerWorker(bw *BaseWorker, window uint64) *RescannerWorker {
	return &RescannerWorker{
		BaseWorker: bw,
		window:     window,
	}
}

func (rw *RescannerWorker) Start() {
	rw.logger.Info("Starting rescanner worker", "chain", rw.chain.GetName(), "window", rw.window)
	go rw.run(rw.processRescan)
}

// processRescan rescans a small window behind latest
func (rw *RescannerWorker) processRescan() error {
	latest, err := rw.chain.GetLatestBlockNumber(rw.ctx)
	if err != nil {
		return fmt.Errorf("get latest block: %w", err)
	}
	if latest == 0 {
		time.Sleep(2 * time.Second)
		return nil
	}

	var start uint64
	if latest > rw.window {
		start = latest - rw.window
	} else {
		start = 1
	}
	end := latest - 1 // avoid current head to reduce reorg risk
	if end < start {
		return nil
	}

	results, err := rw.chain.GetBlocks(rw.ctx, start, end)
	if err != nil {
		return fmt.Errorf("rescanner get blocks: %w", err)
	}

	rescanned := 0
	for _, res := range results {
		if rw.handleBlockResult(res) {
			rescanned++
		}
	}
	rw.logger.Info("Rescanner pass",
		"chain", rw.chain.GetName(),
		"start", start,
		"end", end,
		"rescanned_ok", rescanned,
		"count", len(results),
	)
	return nil
}
