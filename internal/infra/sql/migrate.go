package sqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"
)

// Migrate applies every unapplied *.sql migration from fsys in lexical order.
// It returns the versions it applied. Migrations are forward only.
//
// Every applied version records the sha256 of its file. Re-running with a
// changed file is a hard error instead of silently skipping it: the live
// schema would otherwise drift from the code with no way to notice. This is a
// development guard, not a migration framework: to change a shipped migration
// you must add a new one.
func (d *DB) Migrate(ctx context.Context, fsys fs.FS) ([]string, error) {
	const createTable = `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		checksum   TEXT NOT NULL DEFAULT '',
		applied_at INTEGER NOT NULL
	)`
	if _, err := d.ExecContext(ctx, createTable); err != nil {
		return nil, fmt.Errorf("sqlstore: create schema_migrations: %w", err)
	}
	if _, err := d.ensureChecksumColumn(ctx); err != nil {
		return nil, err
	}
	entries, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("sqlstore: list migrations: %w", err)
	}
	sort.Strings(entries)

	applied := make([]string, 0, len(entries))
	for _, name := range entries {
		body, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("sqlstore: read migration %s: %w", name, err)
		}
		sum := checksum(body)

		var stored string
		err = d.QueryRowContext(ctx,
			`SELECT checksum FROM schema_migrations WHERE version = ?`, name).Scan(&stored)
		switch {
		case err == nil:
			if stored != "" && stored != sum {
				return applied, fmt.Errorf(
					"sqlstore: migration %s changed after it was applied (recorded %s, file %s); add a new migration instead",
					name, shortSum(stored), shortSum(sum))
			}
			if stored == "" {
				// Row written by an older build: record the baseline checksum.
				if _, err := d.ExecContext(ctx,
					`UPDATE schema_migrations SET checksum = ? WHERE version = ?`,
					sum, name); err != nil {
					return applied, fmt.Errorf("sqlstore: backfill checksum for %s: %w", name, err)
				}
			}
			continue
		case err != sql.ErrNoRows:
			return nil, fmt.Errorf("sqlstore: check migration %s: %w", name, err)
		}

		if err := d.applyMigration(ctx, name, body, sum); err != nil {
			return applied, err
		}
		applied = append(applied, name)
	}
	return applied, nil
}

// ensureChecksumColumn upgrades a schema_migrations table created before
// checksums existed. It reports whether the column had to be added.
func (d *DB) ensureChecksumColumn(ctx context.Context) (bool, error) {
	rows, err := d.QueryContext(ctx, `PRAGMA table_info(schema_migrations)`)
	if err != nil {
		return false, fmt.Errorf("sqlstore: inspect schema_migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("sqlstore: inspect schema_migrations: %w", err)
		}
		if name == "checksum" {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("sqlstore: inspect schema_migrations: %w", err)
	}
	rows.Close()
	if _, err := d.ExecContext(ctx,
		`ALTER TABLE schema_migrations ADD COLUMN checksum TEXT NOT NULL DEFAULT ''`); err != nil {
		return false, fmt.Errorf("sqlstore: add schema_migrations.checksum: %w", err)
	}
	return true, nil
}

// migrationFKOffMarker opts a migration into SQLite's documented table-rebuild
// procedure. The marker is a comment near the top of the file:
//
//	-- migrator: foreign-keys-off
//
// A rebuild has to drop and recreate a table that other tables still reference
// (0013 rebuilds blobs while nodes.blob_id points into it). "PRAGMA
// defer_foreign_keys" is not enough: SQLite counts the rows invalidated by the
// DROP's implicit DELETE and does not clear the count when the replacement
// table has the same ids, so COMMIT fails. PRAGMA foreign_keys only takes
// effect outside a transaction, hence the dedicated connection below. As a
// guard, PRAGMA foreign_key_check must report no violations before the commit.
const migrationFKOffMarker = "migrator: foreign-keys-off"

// applyMigration runs one migration file in a transaction and records its
// checksum. Every migration is forward only and atomic.
func (d *DB) applyMigration(ctx context.Context, name string, body []byte, sum string) error {
	run := func(tx *sql.Tx) error {
		for _, stmt := range splitStatements(string(body)) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("sqlstore: migration %s: %w", name, err)
			}
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, checksum, applied_at) VALUES (?, ?, ?)`,
			name, sum, time.Now().Unix())
		return err
	}
	if !strings.Contains(string(body), migrationFKOffMarker) {
		return d.WithTx(ctx, run)
	}
	return d.withForeignKeysOff(ctx, run)
}

// withForeignKeysOff runs fn against a transaction on a dedicated connection
// with foreign key enforcement switched off, then restores the connection
// state and hands it back to the pool. The caller must verify the data with
// PRAGMA foreign_key_check before committing (applyMigration's rebuild
// migrations do).
func (d *DB) withForeignKeysOff(ctx context.Context, fn func(tx *sql.Tx) error) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("sqlstore: migration connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("sqlstore: disable foreign keys: %w", err)
	}
	// Restore the connection before it returns to the pool: PRAGMA
	// foreign_keys is per connection and the next user of this one must get
	// enforcement back.
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `PRAGMA foreign_keys = ON`)
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: begin migration tx: %w", err)
	}
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil && rbErr != sql.ErrTxDone {
			return fmt.Errorf("sqlstore: rollback after %v: %w", err, rbErr)
		}
		return err
	}
	var violations int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&violations); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("sqlstore: foreign key check: %w", err)
	}
	if violations > 0 {
		_ = tx.Rollback()
		return fmt.Errorf("sqlstore: migration left %d foreign key violation(s)", violations)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("sqlstore: commit tx: %w", err)
	}
	return nil
}

func checksum(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func shortSum(sum string) string {
	if len(sum) > 12 {
		return sum[:12]
	}
	return sum
}

// splitStatements splits a migration file on semicolons that are not inside
// strings, comments, or CREATE TRIGGER BEGIN..END bodies. rapig migrations are
// simple DDL/DML, so this is enough and keeps the driver from depending on
// multi-statement support.
//
// Trigger bodies only contain BEGIN/END keywords in our migrations; a CASE
// expression inside a trigger body would confuse the depth counter and is not
// used.
func splitStatements(src string) []string {
	var out []string
	var b strings.Builder
	const (
		normal = iota
		inSingle
		inDouble
		inLineComment
		inBlockComment
	)
	state := normal
	inTrigger := false
	beginDepth := 0
	var word strings.Builder
	flushWord := func() {
		if word.Len() == 0 {
			return
		}
		w := strings.ToLower(word.String())
		word.Reset()
		if inTrigger {
			switch w {
			case "begin":
				beginDepth++
			case "end":
				if beginDepth > 0 {
					beginDepth--
				}
			}
			return
		}
		if w == "trigger" {
			if prefix := strings.ToLower(strings.TrimSpace(b.String())); strings.HasPrefix(prefix, "create") {
				inTrigger = true
			}
		}
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch state {
		case inLineComment:
			b.WriteByte(c)
			if c == '\n' {
				state = normal
			}
		case inBlockComment:
			b.WriteByte(c)
			if c == '*' && i+1 < len(src) && src[i+1] == '/' {
				b.WriteByte('/')
				i++
				state = normal
			}
		case inSingle:
			b.WriteByte(c)
			if c == '\'' {
				if i+1 < len(src) && src[i+1] == '\'' {
					b.WriteByte('\'')
					i++
				} else {
					state = normal
				}
			}
		case inDouble:
			b.WriteByte(c)
			if c == '"' {
				state = normal
			}
		default:
			switch {
			case c == '\'':
				state = inSingle
				flushWord()
				b.WriteByte(c)
			case c == '"':
				state = inDouble
				flushWord()
				b.WriteByte(c)
			case c == '-' && i+1 < len(src) && src[i+1] == '-':
				state = inLineComment
				flushWord()
				b.WriteString("--")
				i++
			case c == '/' && i+1 < len(src) && src[i+1] == '*':
				state = inBlockComment
				flushWord()
				b.WriteString("/*")
				i++
			case c == ';':
				flushWord()
				if inTrigger && beginDepth > 0 {
					b.WriteByte(c)
					continue
				}
				if stmt := strings.TrimSpace(b.String()); stmt != "" {
					out = append(out, stmt)
				}
				b.Reset()
				inTrigger = false
				beginDepth = 0
			case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_':
				word.WriteByte(c)
				b.WriteByte(c)
			default:
				flushWord()
				b.WriteByte(c)
			}
		}
	}
	flushWord()
	if stmt := strings.TrimSpace(b.String()); stmt != "" {
		out = append(out, stmt)
	}
	return out
}
