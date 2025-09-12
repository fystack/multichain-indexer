package utils

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

func ParseHexUint64(h string) (uint64, error) {
	h = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "0x")
	if h == "" {
		return 0, fmt.Errorf("empty hex")
	}
	return strconv.ParseUint(h, 16, 64)
}

func ParseHexBigInt(h string) (*big.Int, error) {
	h = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "0x")
	if h == "" {
		return big.NewInt(0), nil
	}
	bi := new(big.Int)
	if _, ok := bi.SetString(h, 16); !ok {
		return nil, fmt.Errorf("invalid hex: %s", h)
	}
	return bi, nil
}

// ChunkBySize splits slice into chunks with maximum size 'chunkSize'
func ChunkBySize[T any](slice []T, chunkSize int) [][]T {
	if chunkSize <= 0 || len(slice) == 0 {
		return [][]T{slice}
	}

	chunks := make([][]T, 0, (len(slice)+chunkSize-1)/chunkSize)
	for i := 0; i < len(slice); i += chunkSize {
		end := i + chunkSize
		if end > len(slice) {
			end = len(slice)
		}
		chunks = append(chunks, slice[i:end])
	}
	return chunks
}
