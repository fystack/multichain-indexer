package events

import (
	"encoding/json"
	"fmt"

	"github.com/fystack/transaction-indexer/pkg/common/types"
	"github.com/nats-io/nats.go"
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
	EmitError(chain string, err error) error
	Emit(event IndexerEvent) error
	Close()
}

type emitter struct {
	conn          *nats.Conn
	subjectPrefix string
}

func NewEmitter(natsURL, subjectPrefix string) (Emitter, error) {
	conn, err := nats.Connect(natsURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to NATS: %w", err)
	}

	return &emitter{
		conn:          conn,
		subjectPrefix: subjectPrefix,
	}, nil
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
	return e.conn.Publish(TransferEventTopic, txBytes)
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
	return e.conn.Publish(e.subjectPrefix, data)
}

func (e *emitter) Close() {
	if e.conn != nil {
		e.conn.Close()
	}
}
