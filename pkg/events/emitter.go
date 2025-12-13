package events

import (
	"encoding/json"

	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/infra"
)

const (
	TransferEventTopic         = "transfer:event"
	MultiAssetTransferEventTopic = "transfer:multi_asset_event"
)

type IndexerEvent struct {
	Type      string `json:"type"`
	Chain     string `json:"chain"`
	Data      any    `json:"data"`
	Timestamp int64  `json:"timestamp"`
}

type Emitter interface {
	EmitBlock(chain string, block *types.Block) error
	EmitTransaction(chain string, tx *types.Transaction) error
	EmitMultiAssetTransaction(event MultiAssetTransactionEvent) error
	EmitError(chain string, err error) error
	Emit(event IndexerEvent) error
	Close()
}

type emitter struct {
	queue         infra.MessageQueue
	subjectPrefix string
}

func NewEmitter(queue infra.MessageQueue, subjectPrefix string) Emitter {
	return &emitter{
		queue:         queue,
		subjectPrefix: subjectPrefix,
	}
}

func (e *emitter) EmitBlock(chain string, block *types.Block) error {
	// TODO: implement
	return nil
}

func (e *emitter) EmitTransaction(chain string, tx *types.Transaction) error {
	txBytes, err := tx.MarshalBinary()
	if err != nil {
		return err
	}
	return e.queue.Enqueue(infra.TransferEventTopicQueue, txBytes, &infra.EnqueueOptions{
		IdempotententKey: tx.Hash(),
	})
}

func (e *emitter) EmitMultiAssetTransaction(event MultiAssetTransactionEvent) error {
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return e.queue.Enqueue(infra.MultiAssetTransferEventTopicQueue, eventBytes, &infra.EnqueueOptions{
		IdempotententKey: event.TxHash, // Use TxHash for idempotency
	})
}

func (e *emitter) EmitError(chain string, err error) error {
	// TODO: implement
	return nil
}

func (e *emitter) Emit(event IndexerEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	// All chains emit to the same subject
	return e.queue.Enqueue(e.subjectPrefix, data, nil)
}

func (e *emitter) Close() {
	if e.queue != nil {
		e.queue.Close()
	}
}
