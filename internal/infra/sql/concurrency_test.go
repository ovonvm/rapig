package sqlstore

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// TestConcurrentWriteTransactions exercises the write serialization in
// WithTx. Before writeMu existed, a deferred BEGIN that upgraded to a write
// under concurrent writers could fail with SQLITE_BUSY_SNAPSHOT (a busy error
// the busy_timeout handler does not retry); the 512 MB stress run showed that
// as HTTP 500s on upload parts. Writes must now queue and all succeed.
func TestConcurrentWriteTransactions(t *testing.T) {
	ctx := context.Background()
	db, _ := newTestDB(t)
	// Migrations seed a few settings (e.g. default_quota_bytes); count from the
	// current baseline instead of assuming an empty table.
	base, err := db.AllSettings(ctx)
	if err != nil {
		t.Fatalf("baseline settings: %v", err)
	}
	want := len(base)

	const writers, perWriter = 8, 15
	var wg sync.WaitGroup
	errs := make(chan error, writers*perWriter)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				key := fmt.Sprintf("conc_%d_%d", w, i)
				val := fmt.Sprintf("v%d-%d", w, i)
				if err := db.SetSetting(ctx, key, val); err != nil {
					errs <- fmt.Errorf("writer %d iter %d: %w", w, i, err)
					return
				}
				if got, ok, err := db.Setting(ctx, key); err != nil || !ok || got != val {
					errs <- fmt.Errorf("writer %d iter %d: read back %q ok=%v err=%v", w, i, got, ok, err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	all, err := db.AllSettings(ctx)
	if err != nil {
		t.Fatalf("all settings: %v", err)
	}
	if len(all) != want+writers*perWriter {
		t.Fatalf("want %d settings, got %d", want+writers*perWriter, len(all))
	}
}
