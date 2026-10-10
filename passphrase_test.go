package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestPassphraseWordList(t *testing.T) {
	if len(strings.Fields(passphraseWordList)) != 7776 || len(passphraseWords) != 7772 {
		t.Fatalf("passphrase dictionary has %d eligible entries; want 7,772 after excluding the four compound words", len(passphraseWords))
	}
	seen := make(map[string]bool)
	wordPattern := regexp.MustCompile(`^[a-z]+$`)
	for _, word := range passphraseWords {
		if !wordPattern.MatchString(word) || len(word) < 3 || len(word) > 9 || seen[word] {
			t.Fatalf("invalid, overlong, or duplicate passphrase entry: %q", word)
		}
		seen[word] = true
	}
}

func TestGeneratedPassphrasesAreFourWordsAndValidPasswords(t *testing.T) {
	dictionary := make(map[string]bool)
	for _, word := range passphraseWords {
		dictionary[word] = true
	}
	for range 32 {
		passphrase, err := generatePassphrase()
		if err != nil {
			t.Fatal(err)
		}
		words := strings.Split(passphrase, "-")
		if len(words) != 4 {
			t.Fatalf("passphrase has %d words; want 4", len(words))
		}
		for _, word := range words {
			if !dictionary[word] {
				t.Fatalf("generated word is absent from the dictionary: %q", word)
			}
		}
		if err := validatePassword(passphrase); err != nil {
			t.Fatalf("generated passphrase is not accepted as a password: %v", err)
		}
	}
}
