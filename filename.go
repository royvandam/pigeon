package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var filenameBad = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	if name == "." || name == ".." {
		return ""
	}
	name = filenameBad.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._-")
	if len(name) > 180 {
		name = name[:180]
	}
	return name
}

func shortRandHex(nbytes int) string {
	buf := make([]byte, nbytes)
	if _, err := rand.Read(buf); err != nil {
		// fallback; not security-critical for filenames
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
