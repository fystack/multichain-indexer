package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/btcsuite/btcutil/base58"
)

// TronToHexAddress keeps T... addresses, converts 0x... to T...
func TronToHexAddress(addr string) string {
	cleaned := strings.TrimSpace(addr)
	if len(cleaned) >= 34 && (cleaned[0] == 'T' || cleaned[0] == 't') {
		return cleaned
	}
	if strings.HasPrefix(cleaned, "0x") {
		return EVMToTronAddress(cleaned)
	}
	if len(cleaned) == 40 {
		return EVMToTronAddress("0x" + cleaned)
	}
	return addr
}

// EVMToTronAddress converts 0x41... to T...
func EVMToTronAddress(evmAddr string) string {
	hexAddr := strings.TrimPrefix(strings.ToLower(evmAddr), "0x")
	raw, err := hex.DecodeString(hexAddr[len(hexAddr)-40:])
	if err != nil {
		return evmAddr
	}
	payload := append([]byte{0x41}, raw...)
	h1 := sha256.Sum256(payload)
	h2 := sha256.Sum256(h1[:])
	checksum := h2[:4]
	full := append(payload, checksum...)
	return base58.Encode(full)
}
