package indexer

import (
	"crypto/sha256"
	"errors"
	"fmt"

	"filippo.io/edwards25519"
	"github.com/mr-tron/base58"
)

const solanaAssociatedTokenProgramID = "ATokenGPvbdGVxr1b2hvZbsiqW5xWH25efTNsLJA8knL"

// SolanaTokenProgramIDs are the two SPL token programs a token account can belong to.
var SolanaTokenProgramIDs = []string{solanaTokenProgramID, solanaToken2022ProgramID}

// SolanaAssociatedTokenAddress derives owner's associated token account for mint under tokenProgram: the account a
// wallet receives that token in by default. It exists before any deposit is made, so it can be watched from the start.
func SolanaAssociatedTokenAddress(owner, mint, tokenProgram string) (string, error) {
	seeds := make([][]byte, 0, 3)
	for _, key := range []string{owner, tokenProgram, mint} {
		b, err := solanaPubkeyBytes(key)
		if err != nil {
			return "", err
		}
		seeds = append(seeds, b)
	}
	program, err := solanaPubkeyBytes(solanaAssociatedTokenProgramID)
	if err != nil {
		return "", err
	}
	addr, err := solanaFindProgramAddress(seeds, program)
	if err != nil {
		return "", err
	}
	return base58.Encode(addr), nil
}

func solanaPubkeyBytes(key string) ([]byte, error) {
	b, err := base58.Decode(key)
	if err != nil {
		return nil, fmt.Errorf("invalid solana pubkey %q: %w", key, err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("invalid solana pubkey %q: %d bytes", key, len(b))
	}
	return b, nil
}

// solanaFindProgramAddress is Solana's find_program_address: the first bump seed, from 255 down, whose hash is not a
// point on the ed25519 curve (so no private key can sign for it).
func solanaFindProgramAddress(seeds [][]byte, programID []byte) ([]byte, error) {
	for bump := 255; bump >= 0; bump-- {
		h := sha256.New()
		for _, s := range seeds {
			h.Write(s)
		}
		h.Write([]byte{byte(bump)})
		h.Write(programID)
		h.Write([]byte("ProgramDerivedAddress"))
		sum := h.Sum(nil)
		if _, err := new(edwards25519.Point).SetBytes(sum); err != nil {
			return sum, nil
		}
	}
	return nil, errors.New("no viable bump seed for program address")
}
