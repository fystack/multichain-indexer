package enum

// each wallet could have multiple keys
type WalletType string
type KeyType string
type AddressType string
type AddressStandard string
type ChainType string
type BFBackend string
type KVStoreType string

const (
	WalletTypeStandard WalletType = "standard"
	WalletTypeMPC      WalletType = "mpc"
)

const (
	KeyTypeSecp256k1 KeyType = "secp256k1"
	KeyTypeEd25519   KeyType = "ed25519"
)

const (
	AddressTypeEvm    AddressType = "evm"
	AddressTypeBtc    AddressType = "btc"
	AddressTypeSolana AddressType = "sol"
	AddressTypeAptos  AddressType = "aptos"
	AddressTypeTron   AddressType = "tron"
)

const (
	ChainTypeEVM  ChainType = "evm"
	ChainTypeTron ChainType = "tron"
	ChainTypeBtc  ChainType = "btc"
	ChainTypeSol  ChainType = "sol"
	ChainTypeApt  ChainType = "apt"
)

func (c ChainType) String() string {
	return string(c)
}

const (
	BFBackendRedis    BFBackend = "redis"
	BFBackendInMemory BFBackend = "in_memory"
)

const (
	KVStoreTypeBadger KVStoreType = "badger"
	KVStoreTypeConsul KVStoreType = "consul"
)
