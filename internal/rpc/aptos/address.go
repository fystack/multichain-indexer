package aptos

import (
	"fmt"
	"strings"
)

func NormalizeAddress(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))

	if !strings.HasPrefix(addr, "0x") {
		addr = "0x" + addr
	}

	addr = strings.TrimPrefix(addr, "0x")

	if len(addr) < 64 {
		addr = strings.Repeat("0", 64-len(addr)) + addr
	}

	return "0x" + addr
}

func ValidateAddress(addr string) error {
	normalized := NormalizeAddress(addr)

	if len(normalized) != 66 {
		return fmt.Errorf("invalid address length: expected 66 characters (0x + 64 hex), got %d", len(normalized))
	}

	for i, c := range normalized[2:] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return fmt.Errorf("invalid character at position %d: %c", i+2, c)
		}
	}

	return nil
}

func ShortAddress(addr string) string {
	normalized := NormalizeAddress(addr)
	trimmed := strings.TrimLeft(normalized[2:], "0")
	if trimmed == "" {
		return "0x0"
	}
	return "0x" + trimmed
}
