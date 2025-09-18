package tron

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/btcsuite/btcutil/base58"
)

// HexToTronAddress converts hex addresses (41...) to TRON base58 format (T...)
func HexToTronAddress(addr string) string {
	cleaned := strings.TrimSpace(addr)

	// Already in TRON base58 format
	if len(cleaned) >= 34 && (cleaned[0] == 'T' || cleaned[0] == 't') {
		return cleaned
	}

	// Handle hex addresses starting with 41 (TRON hex format)
	if len(cleaned) == 42 && strings.HasPrefix(cleaned, "41") {
		raw, err := hex.DecodeString(cleaned)
		if err != nil {
			return addr
		}
		h1 := sha256.Sum256(raw)
		h2 := sha256.Sum256(h1[:])
		checksum := h2[:4]
		full := append(raw, checksum...)
		return base58.Encode(full)
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
