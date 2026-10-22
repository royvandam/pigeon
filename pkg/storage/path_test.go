package storage

import (
	"errors"
	"strings"
	"testing"
)

func TestValidatePath(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"file.txt", "folder/file.txt", "with spaces/naïve 文件.txt", ".hidden", "upload-x", "a/.uploads/b",
		strings.Repeat("x", 255), strings.Repeat("x/", 2047) + "x",
	} {
		if err := ValidatePath(name); err != nil {
			t.Errorf("ValidatePath(%q) = %v", name, err)
		}
	}
	for _, name := range []string{
		"", ".", "..", "../x", "x/..", "x/./y", "/x", "x/", "x//y", `x\y`, "C:x", "c:/x", "x\x00y", "x\ny",
		strings.Repeat("x", 256), strings.Repeat("x/", 2048) + "x", ".upload-1", "x/.upload-1/y",
	} {
		if err := ValidatePath(name); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("ValidatePath(%q) = %v; want ErrInvalidPath", name, err)
		}
	}
}
