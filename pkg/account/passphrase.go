package account

import (
	"crypto/rand"
	_ "embed"
	"fmt"
	"math/big"
	"strings"
)

// EFF's 7,776-word list is embedded so account creation works offline.
// Attribution and license: wordlists/README.md.
//
//go:embed wordlists/eff_large.txt
var passphraseWordList string

// Skip compound entries so hyphens unambiguously separate the four words.
var passphraseWords = func() []string {
	var words []string
	for _, word := range strings.Fields(passphraseWordList) {
		if !strings.Contains(word, "-") {
			words = append(words, word)
		}
	}
	return words
}()

// GeneratePassphrase returns four random words from the EFF long word list,
// separated by hyphens: about 51 bits of entropy.
func GeneratePassphrase() (string, error) {
	var words [4]string
	limit := big.NewInt(int64(len(passphraseWords)))
	for i := range words {
		index, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate passphrase: %w", err)
		}
		words[i] = passphraseWords[index.Int64()]
	}
	return strings.Join(words[:], "-"), nil
}
