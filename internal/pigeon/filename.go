package pigeon

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

const (
	zeroWidthNonJoiner = 0x200C
	zeroWidthJoiner    = 0x200D
)

// hiddenFormatCharacter reports whether r is an invisible formatting character
// (Unicode category Cf) that new names may not contain. Bidirectional controls
// can make a file named "invoice", U+202E, "fdp.exe" display as invoiceexe.pdf.
// Zero-width joiners and non-joiners remain allowed, because Persian, Indic
// scripts, and emoji sequences need them.
func hiddenFormatCharacter(r rune) bool {
	return unicode.In(r, unicode.Cf) && r != zeroWidthNonJoiner && r != zeroWidthJoiner
}

// validateNewName checks a name being created by an upload, new folder, or
// rename. Paths to existing items skip this check, so files stored before it
// existed stay accessible.
func validateNewName(name string) error {
	if strings.IndexFunc(name, hiddenFormatCharacter) >= 0 {
		return fmt.Errorf("%w: names cannot contain invisible formatting characters, such as right-to-left overrides", errStoragePath)
	}
	return nil
}

// downloadName removes hidden formatting characters from the filename a
// download suggests, so older files with such names cannot disguise their type.
func downloadName(name string) string {
	cleaned := strings.Map(func(r rune) rune {
		if hiddenFormatCharacter(r) {
			return -1
		}
		return r
	}, name)
	if cleaned == "" {
		return "download"
	}
	return cleaned
}

func shortRandHex(nbytes int) string {
	buf := make([]byte, nbytes)
	if _, err := rand.Read(buf); err != nil {
		// fallback; not security-critical for filenames
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
