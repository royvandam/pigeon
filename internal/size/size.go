// Package size reads and formats storage sizes.
package size

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

var sizeRe = regexp.MustCompile(`(?i)^\s*(\d+)\s*(b|kb|k|mb|m|gb|g|tb|t)\s*$`)

// Parse reads a size such as 10MB, 1GB, or 500K. Units are binary: 1KB is
// 1024 bytes.
func Parse(s string) (int64, error) {
	m := sizeRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, fmt.Errorf("expected like 10MB, 1GB, 500K, got %q", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, err
	}
	unit := strings.ToLower(m[2])

	mult := int64(1)
	switch unit {
	case "b":
		mult = 1
	case "k", "kb":
		mult = 1024
	case "m", "mb":
		mult = 1024 * 1024
	case "g", "gb":
		mult = 1024 * 1024 * 1024
	case "t", "tb":
		mult = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown unit %q", unit)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("size %q exceeds the maximum supported size", s)
	}
	return n * mult, nil
}

// Format describes b bytes for people, such as 1.50 MB.
func Format(b int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)
	switch {
	case b >= TB:
		return fmt.Sprintf("%.2f TB", float64(b)/float64(TB))
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
