package evm

import (
	"encoding/hex"
	"errors"
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
