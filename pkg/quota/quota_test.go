package quota

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/royvandam/pigeon/pkg/storage/local"
)

func TestValidateLeavesHeadroom(t *testing.T) {
	t.Parallel()
	for _, quota := range []int64{0, -1, math.MaxInt64, math.MaxInt64 - Headroom + 1} {
		if err := Validate(quota); err == nil {
			t.Errorf("accepted quota %d", quota)
		}
	}
	for _, quota := range []int64{1, DefaultQuota, math.MaxInt64 - Headroom} {
		if err := Validate(quota); err != nil {
			t.Errorf("rejected quota %d: %v", quota, err)
		}
	}
	if Remaining(10, 4) != 6 || Remaining(10, 10) != 0 || Remaining(10, 12) != 0 {
		t.Fatal("remaining quota is not clamped at zero")
	}
}

func TestChargeRoundsUpToTheAllocationUnit(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct{ size, want int64 }{
		{0, 4096}, {1, 4096}, {4096, 4096}, {4097, 8192}, {1 << 20, 1 << 20},
		{math.MaxInt64, math.MaxInt64},
	} {
		if got := Charge(tt.size, DefaultAllocationUnit); got != tt.want {
			t.Errorf("Charge(%d) = %d, want %d", tt.size, got, tt.want)
		}
	}
}

func TestMeasureChargesEveryItemButTheTopFolder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, err := local.NewProvider(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, size := range map[string]int{"a.txt": 0, "folder/b.txt": 4097, "folder/empty/.keep": 1} {
		filename := filepath.Join(p.Directory(), "alice", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, make([]byte, size), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	tree, err := p.Open(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	// Two folders and three files; b.txt needs two units.
	if used, err := Measure(ctx, tree, "", 4096); err != nil || used != 6*4096 {
		t.Fatalf("Measure(top) = %d, %v", used, err)
	}
	// A folder being measured, such as before deleting it, is charged itself.
	if used, err := Measure(ctx, tree, "folder", 1); err != nil || used != 4100 {
		t.Fatalf("Measure(folder) = %d, %v", used, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Measure(canceled, tree, "", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled measurement: %v", err)
	}
}

func TestCacheMeasuresOnceUntilEntriesExpire(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clock := time.Now()
	c := NewCache(func() time.Time { return clock })
	measurements := 0
	measure := func(context.Context) (int64, error) {
		measurements++
		return 100, nil
	}
	c.Add(1, 5) // An account that was never measured stays unmeasured.
	if _, _, ok := c.Cached(1); ok {
		t.Fatal("adding to an unmeasured account cached a value")
	}
	for range 2 {
		if used, err := c.Usage(ctx, 1, measure); err != nil || used != 100 {
			t.Fatalf("Usage = %d, %v", used, err)
		}
	}
	c.Add(1, 20)
	c.Add(1, -500)
	if used, measured, ok := c.Cached(1); !ok || used != 0 || !measured.Equal(clock) || measurements != 1 {
		t.Fatalf("cached %d measured at %v, %v, after %d measurements", used, measured, ok, measurements)
	}
	clock = clock.Add(RemeasureAfter)
	if used, err := c.Usage(ctx, 1, measure); err != nil || used != 100 || measurements != 2 {
		t.Fatalf("expired entry: %d, %v, after %d measurements", used, err, measurements)
	}
	c.Forget(1)
	if _, _, ok := c.Cached(1); ok {
		t.Fatal("a forgotten entry is still cached")
	}
	failure := errors.New("unreadable")
	if _, err := c.Usage(ctx, 1, func(context.Context) (int64, error) { return 0, failure }); !errors.Is(err, failure) {
		t.Fatalf("failed measurement: %v", err)
	}
	if _, _, ok := c.Cached(1); ok {
		t.Fatal("a failed measurement was cached")
	}
}
