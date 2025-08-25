package stringutils

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/tyler-smith/go-bip39"
)

var (
	cachedWordList []string
)

func init() {
	cachedWordList = bip39.GetWordList() // Only load once
}

const (
	DefaultRandomCharSet     = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	NumericCharSet           = "0123456789"
	HighEntropyRandomCharSet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*()_+{}|:<>?~`"
)

func CreateSlug(input string) string {
	// Remove special characters
	reg, err := regexp.Compile("[^a-zA-Z0-9]+")
	if err != nil {
		slog.Error("Error creating slug", "error", err)
		return ""
	}
	processedString := reg.ReplaceAllString(input, " ")
	// Remove leading and trailing spaces
	processedString = strings.TrimSpace(processedString)
	// Replace spaces with dashes
	slug := strings.ReplaceAll(processedString, " ", "-")
	// Convert to lowercase
	slug = strings.ToLower(slug)
	return slug
}

func IsAlphanumeric(s string) bool {
	for _, char := range s {
		if !unicode.IsLetter(char) && !unicode.IsNumber(char) {
			return false
		}
	}
	return true
}

// GenerateRandomString generates a cryptographically secure random string of a given length
func GenerateRandomString(length int, charSet string) string {
	result := make([]byte, length)
	for i := range result {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(charSet))))
		if err != nil {
			slog.Error("Failed to generate secure random index", "error", err)
			return ""
		}
		result[i] = charSet[idx.Int64()]
	}

	return string(result)
}

func ExtractFunctionName(signature string) string {
	re := regexp.MustCompile(`^(?P<functionName>[a-zA-Z_][a-zA-Z0-9_]*)\(`)
	matches := re.FindStringSubmatch(signature)
	if len(matches) > 1 {
		return matches[re.SubexpIndex("functionName")]
	}
	return ""
}

func ExpandTildePath(s string) string {
	home, _ := os.UserHomeDir()
	return strings.Replace(s, "~", home, 1)
}

func HexToBytes(hexStr string) ([]byte, error) {
	// Remove 0x prefix if it exists
	if strings.HasPrefix(hexStr, "0x") {
		hexStr = hexStr[2:]
	}
	return hex.DecodeString(hexStr)
}

// GenerateBIP39ReadableCode generates a mnemonic-like readable code using the BIP-39 word list
func GenerateBIP39ReadableCode(wordCount int) string {
	parts := make([]string, wordCount)

	for i := 0; i < wordCount; i++ {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(cachedWordList))))
		if err != nil {
			slog.Error("Failed to generate secure index for BIP39 words", "error", err)
			return ""
		}
		parts[i] = cachedWordList[idx.Int64()]
	}

	return strings.Join(parts, "_")
}

// GenerateBIP39CodeWithHashSuffix generates a mnemonic-like code with a secure numeric suffix
func GenerateBIP39CodeWithHashSuffix(wordCount int) string {
	code := GenerateBIP39ReadableCode(wordCount)
	suffix, err := rand.Int(rand.Reader, big.NewInt(10000)) // 4-digit random number
	if err != nil {
		slog.Error("Failed to generate secure suffix", "error", err)
		return code // Return without suffix if there's an error
	}
	return fmt.Sprintf("%s_%04d", code, suffix.Int64())
}

// If the input format is invalid, it returns a fallback masked string ("****").
// MaskCodePreview returns a masked version of the code by showing the first 4 and last 4 characters.
// If the code is shorter than 8 characters, it returns "****".
func MaskCodePreview(code string) string {
	if len(code) < 8 {
		return "****"
	}

	start := code[:4]
	end := code[len(code)-4:]
	return start + "...." + end
}

var matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
var matchAllCap = regexp.MustCompile("([a-z0-9])([A-Z])")

// toSnakeCase converts a given CamelCase string to snake_case.
func ToSnakeCase(str string) string {
	snake := matchFirstCap.ReplaceAllString(str, "${1}_${2}")
	snake = matchAllCap.ReplaceAllString(snake, "${1}_${2}")
	return strings.ToLower(snake)
}
