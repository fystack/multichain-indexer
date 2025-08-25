package enum

// each wallet could have multiple keys
type WalletType string
type KeyType string
type AddressType string
type AddressStandard string
type WalletRole string
type WalletPurpose string

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
	AddressStandardBtcP2PKH  = "btc_p2pkh"
	AddressStandardBtcP2SH   = "btc_p2sh"
	AddressStandardBtcBech32 = "btc_bech32"
)

var (
	WalletRoleAdmin  WalletRole = "wallet_admin"
	WalletRoleSigner WalletRole = "wallet_signer"
	WalletRoleViewer WalletRole = "wallet_viewer"
)

var (
	WalletPurposeGeneral    WalletPurpose = "general"
	WalletPurposeGasTank    WalletPurpose = "gas_tank"
	WalletPurposeDeployment WalletPurpose = "deployment"
	WalletPurposeCustody    WalletPurpose = "custody"
	WalletPurposeUser       WalletPurpose = "user"
	WalletPurposePayment    WalletPurpose = "payment"
)
