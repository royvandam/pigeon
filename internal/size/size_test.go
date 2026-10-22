package size

import (
	"math"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()
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
			got, err := Parse(tt.input)
			if err != nil || got != tt.want {
				t.Fatalf("Parse(%q) = %d, %v; want %d", tt.input, got, err, tt.want)
			}
		})
	}
}

func TestParseRejectsInvalidAndOverflowingSizes(t *testing.T) {
	t.Parallel()
	for _, input := range []string{
		"", "10", "-1MB", "1.5GB", "10XB",
		"9223372036854775808B", "8388608TB", "16777217TB",
	} {
		t.Run(input, func(t *testing.T) {
			if got, err := Parse(input); err == nil {
				t.Fatalf("Parse(%q) = %d; want an error", input, got)
			}
		})
	}
}
