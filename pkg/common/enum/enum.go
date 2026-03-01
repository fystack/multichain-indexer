package enum

// each wallet could have multiple keys
type WalletType string
type KeyType string
type AddressStandard string
type NetworkType string
type BFType string
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
	NetworkTypeEVM    NetworkType = "evm"
	NetworkTypeTron   NetworkType = "tron"
	NetworkTypeBtc    NetworkType = "btc"
	NetworkTypeSol    NetworkType = "sol"
	NetworkTypeApt    NetworkType = "apt"
	NetworkTypeSui    NetworkType = "sui"
	NetworkTypeTon    NetworkType = "ton"
	NetworkTypeCosmos NetworkType = "cosmos"
)

const (
	BFBackendRedis    BFType = "redis"
	BFBackendInMemory BFType = "in_memory"
)

const (
	KVStoreTypeBadger KVStoreType = "badger"
	KVStoreTypeConsul KVStoreType = "consul"
)
