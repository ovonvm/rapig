// Package sqlstore is rapig's SQLite persistence layer. It is the only
// package that knows SQL; services above it receive domain types.
package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps *sql.DB with rapig specific helpers.
type DB struct {
	*sql.DB
	// Path is the database file path, or ":memory:" in tests.
	Path string
	// writeMu serializes write transactions inside this process. SQLite
	// allows a single writer, and a deferred BEGIN that upgrades to a write
	// can fail with SQLITE_BUSY_SNAPSHOT (517) which busy_timeout does not
	// retry. Under the 512 MB / 1 vCPU profile (8 upload streams plus the UI
	// polling) that surfaced as HTTP 500s, so writes queue here instead:
	// write transactions are short and the queue is bounded by latency, not
	// memory. See docs/07 F-17.
	writeMu sync.Mutex
}

// busyRetryAttempts bounds how often a transaction is retried when SQLite
// still reports BUSY (for example while another process holds the writer
// lock). In-process writers never hit this because writeMu serializes them.
const busyRetryAttempts = 4

// Options tunes the connection pool and SQLite page cache. PRAGMA
// cache_size is per connection, so the worst case is MaxConns * CacheKiB;
// this is the biggest memory lever on a 512 MB machine (docs/09 §3).
type Options struct {
	MaxConns int
	CacheKiB int
}

// DefaultOptions keeps the M0.5 behaviour for tests and callers that do not
// care about the memory profile.
func DefaultOptions() Options { return Options{MaxConns: 4, CacheKiB: 32000} }

// Open opens (creating when needed) the SQLite database at path with the
// default pool settings.
func Open(ctx context.Context, path string) (*DB, error) {
	return OpenWithOptions(ctx, path, DefaultOptions())
}

// OpenWithOptions opens the database and applies the M0 pragmas: WAL
// journaling, a 5s busy timeout, foreign keys, a small page cache and no mmap
// (the memory baseline matters more than mmap).
func OpenWithOptions(ctx context.Context, path string, opts Options) (*DB, error) {
	if opts.MaxConns <= 0 {
		opts.MaxConns = DefaultOptions().MaxConns
	}
	if opts.CacheKiB <= 0 {
		opts.CacheKiB = DefaultOptions().CacheKiB
	}
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("sqlstore: create db dir: %w", err)
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)" +
		fmt.Sprintf("&_pragma=cache_size(-%d)", opts.CacheKiB) +
		"&_pragma=mmap_size(0)" +
		"&_pragma=journal_mode(WAL)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: open %s: %w", path, err)
	}
	sqlDB.SetMaxOpenConns(opts.MaxConns)
	sqlDB.SetMaxIdleConns(opts.MaxConns)
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("sqlstore: ping %s: %w", path, err)
	}
	return &DB{DB: sqlDB, Path: path}, nil
}

// WithTx runs fn inside a write transaction, rolling back on error and
// committing otherwise. Transactions are serialized in-process (see writeMu),
// which removes the BUSY_SNAPSHOT class of failures entirely; the retry loop
// only covers a BUSY returned while opening the transaction, because the
// transaction body may have side effects and must not be replayed.
func (d *DB) WithTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	tx, err := d.beginTx(ctx)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
			return fmt.Errorf("sqlstore: rollback after %v: %w", err, rbErr)
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlstore: commit tx: %w", err)
	}
	return nil
}

func (d *DB) beginTx(ctx context.Context) (*sql.Tx, error) {
	var lastErr error
	for attempt := 0; attempt < busyRetryAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt*attempt) * 20 * time.Millisecond
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		tx, err := d.BeginTx(ctx, nil)
		if err == nil {
			return tx, nil
		}
		lastErr = err
		if !isBusyError(err) || ctx.Err() != nil {
			return nil, fmt.Errorf("sqlstore: begin tx: %w", err)
		}
	}
	return nil, fmt.Errorf("sqlstore: begin tx still busy after %d attempts: %w", busyRetryAttempts, lastErr)
}

// isBusyError reports whether err is SQLite's "database is locked" family
// (SQLITE_BUSY / SQLITE_BUSY_SNAPSHOT), which is worth retrying.
func isBusyError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "SQLITE_BUSY")
}

// Ping verifies the database is reachable.
func (d *DB) Ping(ctx context.Context) error { return d.PingContext(ctx) }
