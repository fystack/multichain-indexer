package types

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

type UTXOEvent struct {
	TxHash        string      `json:"txHash"`
	NetworkId     string      `json:"networkId"`
	BlockNumber   uint64      `json:"blockNumber"`
	BlockHash     string      `json:"blockHash"`
	Timestamp     uint64      `json:"timestamp"`
	Created       []UTXO      `json:"created"`
	Spent         []SpentUTXO `json:"spent"`
	TxFee         string      `json:"txFee"`
	Status        string      `json:"status"`
	Confirmations uint64      `json:"confirmations"`
}

type UTXO struct {
	TxHash       string `json:"txHash"`
	Vout         uint32 `json:"vout"`
	Address      string `json:"address"`
	Amount       string `json:"amount"`
	ScriptPubKey string `json:"scriptPubKey"`
}

type SpentUTXO struct {
	TxHash  string `json:"txHash"`
	Vout    uint32 `json:"vout"`
	Vin     uint32 `json:"vin"`
	Address string `json:"address"`
	Amount  string `json:"amount"`
}

func (u UTXOEvent) MarshalBinary() ([]byte, error) {
	return json.Marshal(u)
}

func (u *UTXOEvent) UnmarshalBinary(data []byte) error {
	return json.Unmarshal(data, &u)
}

func (u UTXOEvent) Hash() string {
	var builder strings.Builder
	builder.WriteString(u.NetworkId)
	builder.WriteByte('|')
	builder.WriteString(u.TxHash)
	builder.WriteByte('|')
	builder.WriteString(u.BlockHash)
	hash := sha256.Sum256([]byte(builder.String()))
	return fmt.Sprintf("%x", hash)
}

func (u UTXO) Key() string {
	return fmt.Sprintf("%s:%d", u.TxHash, u.Vout)
}

func (s SpentUTXO) Key() string {
	return fmt.Sprintf("%s:%d", s.TxHash, s.Vout)
}
