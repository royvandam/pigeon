package main

import (
	"math"
	"testing"
)

func TestParseSize(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  int64
	}{
		{"10MB", 10 << 20},
		{" 500 k ", 500 << 10},
		{"1gB", 1 << 30},
		{"0B", 0},
		{"9223372036854775807B", math.MaxInt64},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseSize(tt.input)
			if err != nil || got != tt.want {
				t.Fatalf("parseSize(%q) = %d, %v; want %d", tt.input, got, err, tt.want)
			}
		})
	}
}

func TestParseSizeRejectsInvalidAndOverflowingSizes(t *testing.T) {
	for _, input := range []string{
		"", "10", "-1MB", "1.5GB", "10XB",
		"9223372036854775808B", "8388608TB", "16777217TB",
	} {
		t.Run(input, func(t *testing.T) {
			if got, err := parseSize(input); err == nil {
				t.Fatalf("parseSize(%q) = %d; want an error", input, got)
			}
		})
	}
}
