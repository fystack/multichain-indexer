package evm

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"golang.org/x/crypto/sha3"
)

// DecodeERC20TransferInput parses ERC20 transfer(address,uint256)
func DecodeERC20TransferInput(input string) (string, *big.Int, error) {
	input = strings.TrimPrefix(input, "0x")
	if len(input) < 8+64+64 {
		return "", nil, errors.New("invalid ERC20 transfer input length")
	}

	// remove method selector (8 hex chars = 4 bytes)
	data := input[8:]

	// first 32 bytes = address (right padded)
	addrData, err := hex.DecodeString(data[:64])
	if err != nil {
		return "", nil, err
	}
	// last 20 bytes are the address
	toAddr := "0x" + hex.EncodeToString(addrData[12:])
	toAddr = ToChecksumAddress(toAddr)

	// next 32 bytes = amount
	amountData, err := hex.DecodeString(data[64:128])
	if err != nil {
		return "", nil, err
	}
	amount := new(big.Int).SetBytes(amountData)

	return toAddr, amount, nil
}

// DecodeERC20TransferFromInput parses transferFrom(address,address,uint256)
func DecodeERC20TransferFromInput(input string) (string, string, *big.Int, error) {
	input = strings.TrimPrefix(input, "0x")
	if len(input) < 8+64+64+64 {
		return "", "", nil, errors.New("invalid ERC20 transferFrom input length")
	}

	data := input[8:]

	// from
	fromData, err := hex.DecodeString(data[:64])
	if err != nil {
		return "", "", nil, err
	}
	fromAddr := "0x" + hex.EncodeToString(fromData[12:])
	fromAddr = ToChecksumAddress(fromAddr)

	// to
	toData, err := hex.DecodeString(data[64:128])
	if err != nil {
		return "", "", nil, err
	}
	toAddr := "0x" + hex.EncodeToString(toData[12:])
	toAddr = ToChecksumAddress(toAddr)

	// amount
	amtData, err := hex.DecodeString(data[128:192])
	if err != nil {
		return "", "", nil, err
	}
	amount := new(big.Int).SetBytes(amtData)

	return fromAddr, toAddr, amount, nil
}

// GnosisSafeExecParams holds decoded parameters from execTransaction input.
type GnosisSafeExecParams struct {
	To        string   // recipient address
	Value     *big.Int // ETH value in wei
	Data      []byte   // inner calldata (empty = pure ETH transfer)
	Operation uint8    // 0 = Call, 1 = DelegateCall
}

// DecodeGnosisSafeExecTransaction decodes execTransaction(address,uint256,bytes,uint8,...) input.
// The input must start with method selector 0x6a761202.
// Layout after selector (each 32 bytes):
//
//	offset 0x00: to (address, left-padded to 32 bytes)
//	offset 0x20: value (uint256)
//	offset 0x40: data offset (pointer to dynamic bytes)
//	offset 0x60: operation (uint8, left-padded to 32 bytes)
//	... (remaining params: safeTxGas, baseGas, gasPrice, gasToken, refundReceiver, signatures)
func DecodeGnosisSafeExecTransaction(input string) (*GnosisSafeExecParams, error) {
	input = strings.TrimPrefix(input, "0x")

	// Minimum: 8 (selector) + 4*64 (to, value, data offset, operation) = 264 hex chars
	if len(input) < 8+4*64 {
		return nil, errors.New("input too short for execTransaction")
	}

	data := input[8:] // skip method selector

	// Param 0: to (address) — last 20 bytes of 32-byte word
	toBytes, err := hex.DecodeString(data[:64])
	if err != nil {
		return nil, fmt.Errorf("decode 'to': %w", err)
	}
	to := "0x" + hex.EncodeToString(toBytes[12:])
	to = ToChecksumAddress(to)

	// Param 1: value (uint256)
	valueBytes, err := hex.DecodeString(data[64:128])
	if err != nil {
		return nil, fmt.Errorf("decode 'value': %w", err)
	}
	value := new(big.Int).SetBytes(valueBytes)

	// Param 2: data offset (uint256) — pointer to dynamic bytes
	dataOffsetBytes, err := hex.DecodeString(data[128:192])
	if err != nil {
		return nil, fmt.Errorf("decode 'data offset': %w", err)
	}
	dataOffset := new(big.Int).SetBytes(dataOffsetBytes).Uint64()

	// Param 3: operation (uint8)
	opBytes, err := hex.DecodeString(data[192:256])
	if err != nil {
		return nil, fmt.Errorf("decode 'operation': %w", err)
	}
	operation := opBytes[31] // last byte of 32-byte word

	// Decode dynamic 'data' bytes
	// dataOffset is in bytes from start of params (after selector)
	// At dataOffset: first 32 bytes = length, then the actual bytes
	var innerData []byte
	dataHexLen := uint64(len(data))

	// Guard: dataOffset * 2 could overflow uint64 if dataOffset > max/2.
	// Also reject if dataOffset points beyond the input.
	if dataOffset <= dataHexLen/2 {
		hexOffset := dataOffset * 2
		if dataHexLen >= hexOffset+64 {
			lengthBytes, err := hex.DecodeString(data[hexOffset : hexOffset+64])
			if err == nil {
				dataLen := new(big.Int).SetBytes(lengthBytes).Uint64()
				// Guard: dataLen * 2 could overflow uint64
				if dataLen > 0 && dataLen <= dataHexLen/2 {
					hexStart := hexOffset + 64
					hexEnd := hexStart + dataLen*2
					if dataHexLen >= hexEnd {
						innerData, _ = hex.DecodeString(data[hexStart:hexEnd])
					}
				}
			}
		}
	}

	return &GnosisSafeExecParams{
		To:        to,
		Value:     value,
		Data:      innerData,
		Operation: operation,
	}, nil
}

// ToChecksumAddress converts an Ethereum address to EIP-55 checksummed format
func ToChecksumAddress(addr string) string {
	// Remove 0x prefix if present
	addr = strings.TrimPrefix(strings.ToLower(addr), "0x")

	// Handle empty or invalid addresses
	if len(addr) != 40 {
		return "0x" + addr
	}

	// Compute keccak256 hash of the lowercase address
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(addr))
	hashBytes := hash.Sum(nil)

	// Build checksummed address
	result := make([]byte, 42)
	result[0] = '0'
	result[1] = 'x'

	for i := 0; i < 40; i++ {
		c := addr[i]
		// Get the corresponding nibble from hash
		hashByte := hashBytes[i/2]
		var nibble byte
		if i%2 == 0 {
			nibble = hashByte >> 4
		} else {
			nibble = hashByte & 0x0f
		}

		// If hash nibble >= 8, capitalize the character (if it's a letter)
		if nibble >= 8 && c >= 'a' && c <= 'f' {
			result[2+i] = c - 32 // Convert to uppercase
		} else {
			result[2+i] = c
		}
	}

	return string(result)
}
