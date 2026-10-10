package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Directory uploads use slash-separated relative paths. Validate the original
// path before converting it to a native path, preserving spaces and Unicode.
func validateUploadPath(name string) error {
	if name == "." || !fs.ValidPath(name) || len(name) > 4095 ||
		strings.ContainsRune(name, '\\') || strings.IndexFunc(name, unicode.IsControl) >= 0 ||
		!filepath.IsLocal(filepath.FromSlash(name)) ||
		(len(name) >= 2 && name[1] == ':' && ((name[0] >= 'A' && name[0] <= 'Z') || (name[0] >= 'a' && name[0] <= 'z'))) {
		return fmt.Errorf("invalid relative file path")
	}
	for _, component := range strings.Split(name, "/") {
		if len(component) > 255 {
			return fmt.Errorf("invalid relative file path")
		}
	}
	return nil
}

func shortRandHex(nbytes int) string {
	buf := make([]byte, nbytes)
	if _, err := rand.Read(buf); err != nil {
		// fallback; not security-critical for filenames
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
