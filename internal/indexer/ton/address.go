package ton

import (
	"fmt"
	"strings"

	"github.com/xssnick/tonutils-go/address"
)

func resolvePollAddress(addrStr string) (*address.Address, string, error) {
	addr, err := parseTONAddress(addrStr)
	if err != nil {
		return nil, "", fmt.Errorf("invalid TON address %s: %w", addrStr, err)
	}
	return addr, addr.StringRaw(), nil
}

// NormalizeTONAddressRaw returns canonical raw format (workchain:hex) or empty if invalid.
func NormalizeTONAddressRaw(addr string) string {
	parsed, err := parseTONAddress(addr)
	if err != nil {
		return ""
	}
	return parsed.StringRaw()
}

// NormalizeTONAddressList canonicalizes to raw format, trims invalid inputs, and de-duplicates while preserving order.
func NormalizeTONAddressList(addresses []string) []string {
	if len(addresses) == 0 {
		return nil
	}

	dedup := make(map[string]struct{}, len(addresses))
	result := make([]string, 0, len(addresses))
	for _, addr := range addresses {
		normalized := NormalizeTONAddressRaw(addr)
		if normalized == "" {
			continue
		}
		if _, exists := dedup[normalized]; exists {
			continue
		}
		dedup[normalized] = struct{}{}
		result = append(result, normalized)
	}

	return result
}

func parseTONAddress(addrStr string) (*address.Address, error) {
	addrStr = strings.TrimSpace(addrStr)

	// User-friendly format (base64url with checksum), e.g. EQ...
	if addr, err := address.ParseAddr(addrStr); err == nil {
		return addr, nil
	}
	// Raw format, e.g. 0:abcdef...
	if addr, err := address.ParseRawAddr(addrStr); err == nil {
		return addr, nil
	}

	// Defensive normalization for malformed historical values.
	parts := strings.SplitN(addrStr, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid address format")
	}

	rawHex := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(parts[1])), "0x")
	if rawHex == "" {
		return nil, fmt.Errorf("empty address payload")
	}

	if len(rawHex)%2 == 1 {
		rawHex = "0" + rawHex
	}
	if len(rawHex) > 64 {
		rawHex = rawHex[len(rawHex)-64:]
	} else if len(rawHex) < 64 {
		rawHex = strings.Repeat("0", 64-len(rawHex)) + rawHex
	}

	return address.ParseRawAddr(parts[0] + ":" + rawHex)
}
