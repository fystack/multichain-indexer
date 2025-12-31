package bitcoin

import (
	"crypto/sha256"
	"fmt"
	"strings"

	"github.com/btcsuite/btcutil/base58"
	"github.com/btcsuite/btcutil/bech32"
)

// NormalizeBTCAddress validates and normalizes a Bitcoin address
// Supports both mainnet and testnet addresses in various formats
func NormalizeBTCAddress(addr string) (string, error) {
	addr = strings.TrimSpace(addr)

	if addr == "" {
		return "", fmt.Errorf("empty address")
	}

	// Check for Bech32 addresses (SegWit)
	if strings.HasPrefix(addr, "bc1") || strings.HasPrefix(addr, "tb1") {
		// Bech32 validation
		_, _, err := bech32.Decode(addr)
		if err != nil {
			return "", fmt.Errorf("invalid bech32 address: %w", err)
		}
		// Return lowercase normalized form
		return strings.ToLower(addr), nil
	}

	// Base58Check validation for legacy addresses
	decoded := base58.Decode(addr)
	if len(decoded) != 25 {
		return "", fmt.Errorf("invalid address length: expected 25, got %d", len(decoded))
	}

	// Verify checksum (double SHA256)
	payload := decoded[:21]
	checksum := decoded[21:]
	hash := sha256.Sum256(payload)
	hash = sha256.Sum256(hash[:])

	if hash[0] != checksum[0] || hash[1] != checksum[1] || hash[2] != checksum[2] || hash[3] != checksum[3] {
		return "", fmt.Errorf("invalid address checksum")
	}

	// Validate version byte
	versionByte := decoded[0]
	validVersions := map[byte]string{
		0x00: "p2pkh_mainnet",
		0x05: "p2sh_mainnet",
		0x6f: "p2pkh_testnet",
		0xc4: "p2sh_testnet",
	}

	if _, ok := validVersions[versionByte]; !ok {
		return "", fmt.Errorf("invalid version byte: 0x%02x", versionByte)
	}

	return addr, nil
}

// GetAddressType determines the type of Bitcoin address
func GetAddressType(addr string) string {
	addr = strings.TrimSpace(addr)

	switch {
	// Mainnet addresses
	case strings.HasPrefix(addr, "1"):
		return "p2pkh_mainnet"
	case strings.HasPrefix(addr, "3"):
		return "p2sh_mainnet"
	case strings.HasPrefix(addr, "bc1q"):
		return "p2wpkh_mainnet"
	case strings.HasPrefix(addr, "bc1p"):
		return "p2tr_mainnet"

	// Testnet addresses
	case strings.HasPrefix(addr, "m") || strings.HasPrefix(addr, "n"):
		return "p2pkh_testnet"
	case strings.HasPrefix(addr, "2"):
		return "p2sh_testnet"
	case strings.HasPrefix(addr, "tb1q"):
		return "p2wpkh_testnet"
	case strings.HasPrefix(addr, "tb1p"):
		return "p2tr_testnet"

	default:
		return "unknown"
	}
}

// IsTestnetAddress checks if an address is for testnet
func IsTestnetAddress(addr string) bool {
	addrType := GetAddressType(addr)
	return strings.Contains(addrType, "testnet")
}

// IsMainnetAddress checks if an address is for mainnet
func IsMainnetAddress(addr string) bool {
	addrType := GetAddressType(addr)
	return strings.Contains(addrType, "mainnet")
}

// ValidateAddressFormat validates that an address has a valid format
func ValidateAddressFormat(addr string) error {
	_, err := NormalizeBTCAddress(addr)
	return err
}
