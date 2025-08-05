package events

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fystack/transaction-indexer/internal/types"

	"github.com/nats-io/nats.go"
)

const (
	EventBlockIndexed       = "block.indexed"
	EventTransactionIndexed = "transaction.indexed"
	EventIndexerError       = "indexer.error"
)

type ErrorEvent struct {
	Error string `json:"error"`
}

type Emitter struct {
	conn          *nats.Conn
	subjectPrefix string
}

func NewEmitter(natsURL, subjectPrefix string) (*Emitter, error) {
	conn, err := nats.Connect(natsURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	return &Emitter{
		conn:          conn,
		subjectPrefix: subjectPrefix,
	}, nil
}

func (e *Emitter) EmitBlock(chain string, block *types.Block) error {
	event := types.IndexerEvent{
		Type:      EventBlockIndexed,
		Chain:     chain,
		Data:      block,
		Timestamp: time.Now().Unix(),
	}
	return e.emit(event)
}

func (e *Emitter) EmitTransaction(chain string, tx *types.Transaction) error {
	event := types.IndexerEvent{
		Type:      EventTransactionIndexed,
		Chain:     chain,
		Data:      tx,
		Timestamp: time.Now().Unix(),
	}
	return e.emit(event)
}

func (e *Emitter) EmitError(chain string, err error) error {
	event := types.IndexerEvent{
		Type:      EventIndexerError,
		Chain:     chain,
		Data:      ErrorEvent{Error: err.Error()},
		Timestamp: time.Now().Unix(),
	}
	return e.emit(event)
}

func (e *Emitter) emit(event types.IndexerEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	subject := fmt.Sprintf("%s.%s.%s", e.subjectPrefix, event.Chain, event.Type)
	return e.conn.Publish(subject, data)
}

func (e *Emitter) Close() {
	if e.conn != nil {
		e.conn.Close()
	}
}
