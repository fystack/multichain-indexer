package ton

import (
	"encoding/hex"
	"testing"

	"github.com/fystack/multichain-indexer/pkg/common/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTonAccountIndexerTransactionNetworkID(t *testing.T) {
	t.Run("prefer_network_id", func(t *testing.T) {
		indexer := &TonAccountIndexer{
			config: config.ChainConfig{
				NetworkId:    "ton_testnet",
				InternalCode: "TON_TESTNET",
			},
		}

		assert.Equal(t, "ton_testnet", indexer.transactionNetworkID())
	})

	t.Run("empty_when_network_id_missing", func(t *testing.T) {
		indexer := &TonAccountIndexer{
			config: config.ChainConfig{
				NetworkId:    "  ",
				InternalCode: "TON_TESTNET",
			},
		}

		assert.Equal(t, "", indexer.transactionNetworkID())
	})
}

func TestNormalizeTONAddressList_CanonicalizesAndDedups(t *testing.T) {
	addr1 := "0:fc58a2bb35b051810bef84fce18747ac2c2cfcbe0ce3d3167193d9b2538ef33e"
	addr2 := "0:2942e40f94b5a2f111ea2ff98beb5f634f3a971f99f7fedafff5164c4bfa1bef"

	got := NormalizeTONAddressList([]string{
		" 0:FC58A2BB35B051810BEF84FCE18747AC2C2CFCBE0CE3D3167193D9B2538EF33E ",
		"0:0xfc58a2bb35b051810bef84fce18747ac2c2cfcbe0ce3d3167193d9b2538ef33e",
		addr2,
		"not-an-address",
		addr1,
		"",
	})

	assert.Equal(t, []string{addr1, addr2}, got)
}

func TestEncodeTONTxHash_UsesHex(t *testing.T) {
	hash := []byte{0xfb, 0xef, 0xff, 0xfa, 0x00, 0x01, 0x02, 0x7f, 0x80, 0x90, 0xaa}

	encoded := encodeTONTxHash(hash)

	assert.Equal(t, "fbeffffa0001027f8090aa", encoded)

	decoded, err := hex.DecodeString(encoded)
	require.NoError(t, err)
	assert.Equal(t, hash, decoded)
}
