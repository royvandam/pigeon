package files

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/royvandam/pigeon/pkg/storage"
)

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
		return fmt.Errorf("%w: names cannot contain invisible formatting characters, such as right-to-left overrides", storage.ErrInvalidPath)
	}
	return nil
}

// validateEntryName checks the name of a new folder or a renamed item.
func validateEntryName(name string) error {
	if strings.ContainsRune(name, '/') {
		return fmt.Errorf("%w: enter a name without folder separators", storage.ErrInvalidPath)
	}
	if err := storage.ValidatePath(name); err != nil {
		return err
	}
	return validateNewName(name)
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
