package ton

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseTONAddressAny(t *testing.T) {
	t.Run("raw_address", func(t *testing.T) {
		addr, err := parseTONAddressAny("0:eaa27e0e4fbadad817ac4a106de2bae8b52106b5267c1656a3892538d59c69dc")
		require.NoError(t, err)
		require.Equal(t, "0:eaa27e0e4fbadad817ac4a106de2bae8b52106b5267c1656a3892538d59c69dc", addr.StringRaw())
	})

	t.Run("friendly_address", func(t *testing.T) {
		addr, err := parseTONAddressAny("EQAd59XMWb40TRfxqTJ0otrUTqD8ZuxLHhC8T401A5-Kr2aY")
		require.NoError(t, err)
		require.Equal(t, "0:1de7d5cc59be344d17f1a93274a2dad44ea0fc66ec4b1e10bc4f8d35039f8aaf", addr.StringRaw())
	})

	t.Run("invalid_address", func(t *testing.T) {
		_, err := parseTONAddressAny("not-an-address")
		require.Error(t, err)
	})
}
