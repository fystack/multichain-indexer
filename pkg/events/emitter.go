package events

import (
	"encoding/json"

	"github.com/fystack/multichain-indexer/pkg/common/types"
	"github.com/fystack/multichain-indexer/pkg/infra"
)

const (
	TransferEventTopic = "transfer:event"
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
	EmitTransactionWithKey(chain string, tx *types.Transaction, idempotentKey string) error
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
	return e.EmitTransactionWithKey(chain, tx, tx.Hash())
}

func (e *emitter) EmitTransactionWithKey(chain string, tx *types.Transaction, idempotentKey string) error {
	txBytes, err := tx.MarshalBinary()
	if err != nil {
		return err
	}
	return e.queue.Enqueue(infra.TransferEventTopicQueue, txBytes, &infra.EnqueueOptions{
		IdempotententKey: idempotentKey,
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
