package bitcoin

import (
	"github.com/shopspring/decimal"
)

// IsCoinbase checks if a transaction is a coinbase transaction (block reward)
func (tx *Transaction) IsCoinbase() bool {
	return len(tx.Vin) == 1 && tx.Vin[0].TxID == ""
}

// CalculateFee calculates the transaction fee
// Fee = Sum(inputs) - Sum(outputs)
func (tx *Transaction) CalculateFee() decimal.Decimal {
	var totalInput, totalOutput decimal.Decimal

	// Sum all inputs
	for _, vin := range tx.Vin {
		if vin.PrevOut != nil {
			totalInput = totalInput.Add(decimal.NewFromFloat(vin.PrevOut.Value))
		}
	}

	// Sum all outputs
	for _, vout := range tx.Vout {
		totalOutput = totalOutput.Add(decimal.NewFromFloat(vout.Value))
	}

	// Calculate fee
	fee := totalInput.Sub(totalOutput)

	// Fee should never be negative in a valid transaction
	if fee.IsNegative() {
		return decimal.Zero
	}

	return fee
}

// GetOutputAddress extracts the address from an output's scriptPubKey
func GetOutputAddress(output *Output) string {
	if output == nil {
		return ""
	}

	if output.ScriptPubKey.Address != "" {
		return output.ScriptPubKey.Address
	}

	if len(output.ScriptPubKey.Addresses) > 0 {
		return output.ScriptPubKey.Addresses[0]
	}

	return ""
}

// GetOutputAddresses returns all addresses from an output's scriptPubKey.
// For standard outputs this returns a single address. For bare multisig (P2MS)
// it returns all participant addresses. Returns nil for unspendable outputs.
func GetOutputAddresses(output *Output) []string {
	if output == nil {
		return nil
	}

	if output.ScriptPubKey.Address != "" {
		return []string{output.ScriptPubKey.Address}
	}

	if len(output.ScriptPubKey.Addresses) > 0 {
		result := make([]string, len(output.ScriptPubKey.Addresses))
		copy(result, output.ScriptPubKey.Addresses)
		return result
	}

	return nil
}

// GetInputAddress extracts the address from an input's previous output
func GetInputAddress(input *Input) string {
	if input == nil || input.PrevOut == nil {
		return ""
	}

	return GetOutputAddress(input.PrevOut)
}
