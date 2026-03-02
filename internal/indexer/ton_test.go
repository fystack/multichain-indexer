package indexer

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeTONAddressRaw(t *testing.T) {
	addr := "0:fc58a2bb35b051810bef84fce18747ac2c2cfcbe0ce3d3167193d9b2538ef33e"

	got := normalizeTONAddressRaw(
		" 0:FC58A2BB35B051810BEF84FCE18747AC2C2CFCBE0CE3D3167193D9B2538EF33E ",
	)
	assert.Equal(t, addr, got)

	got = normalizeTONAddressRaw("0:0xfc58a2bb35b051810bef84fce18747ac2c2cfcbe0ce3d3167193d9b2538ef33e")
	assert.Equal(t, addr, got)

	assert.Equal(t, "", normalizeTONAddressRaw("not-a-ton-address"))
}

func TestEncodeTONTxHash(t *testing.T) {
	hash := []byte{0xfb, 0xef, 0xff, 0xfa, 0x00, 0x01, 0x02, 0x7f, 0x80, 0x90, 0xaa}
	encoded := encodeTONTxHash(hash)
	assert.Equal(t, "fbeffffa0001027f8090aa", encoded)

	decoded, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	assert.Equal(t, hash, decoded)
}
