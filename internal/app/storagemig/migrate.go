// Package storagemig moves every blob referenced by one local storage policy
// into another while Rapig is offline (M6.2 / F-42).
//
// The admin API deliberately refuses to change a storage root that still holds
// nodes or blobs (409 storage_in_use). Operators who grow out of a root run this
// command with the server stopped: it copies each object into the target root,
// repoints the nodes (and therefore the blobs row) at the target policy, drops
// the source blob row, and finally unlinks the source file. Every blob is an
// independent, idempotent unit of work, so an interrupted run can simply be
// started again.
package storagemig

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	sqlstore "rapig/internal/infra/sql"
	"rapig/internal/infra/storage"
	"rapig/internal/infra/storage/local"
)

var (
	// ErrSamePolicy rejects migrating a policy onto itself.
	ErrSamePolicy = errors.New("source and target policy are the same")
	// ErrSourceEnabled requires the source policy to be disabled first: while it
	// is enabled the server would keep writing new blobs underneath us.
	ErrSourceEnabled = errors.New("source policy must be disabled before migrating")
	// ErrTargetDisabled requires the target policy to be enabled and writable.
	ErrTargetDisabled = errors.New("target policy must be enabled")
	// ErrPolicyBusy rejects runs while unfinished uploads or transfers still
	// target this policy (the server must be stopped anyway; this catches the
	// case where it was not).
	ErrPolicyBusy = errors.New("source policy still has unfinished uploads or transfers")
	// ErrRefcountDrift refuses to move blobs whose refcount no longer matches the
	// number of nodes pointing at them; run `rapig fsck -repair` first.
	ErrRefcountDrift = errors.New("blob refcount drift on source policy")
)

// Options configures one migration run.
type Options struct {
	From   int64
	To     int64
	DryRun bool
}

// Report summarises what a run did (or would do with DryRun).
type Report struct {
	FromPolicy   int64
	ToPolicy     int64
	Blobs        int64
	Nodes        int64
	Bytes        int64
	Copied       int64
	Reused       int64
	RemovedFiles int64
	DryRun       bool
}

// ResolvePolicy maps a CLI reference (name or #id) to a policy id.
func ResolvePolicy(ctx context.Context, db *sqlstore.DB, ref string) (int64, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return 0, fmt.Errorf("storage policy reference is empty")
	}
	if strings.HasPrefix(ref, "#") {
		id, err := strconv.ParseInt(strings.TrimPrefix(ref, "#"), 10, 64)
		if err != nil || id <= 0 {
			return 0, fmt.Errorf("invalid storage policy id %q", ref)
		}
		return id, nil
	}
	policies, err := db.ListStoragePolicies(ctx, true)
	if err != nil {
		return 0, err
	}
	for _, p := range policies {
		if p.Name == ref {
			return p.ID, nil
		}
	}
	return 0, fmt.Errorf("storage policy %q not found", ref)
}

// Migrate moves every blob of the source policy to the target policy.
func Migrate(ctx context.Context, db *sqlstore.DB, reg *storage.Registry, log *slog.Logger, opts Options) (Report, error) {
	report := Report{DryRun: opts.DryRun}
	from, err := db.StoragePolicyByID(ctx, opts.From)
	if err != nil {
		return report, fmt.Errorf("load source policy: %w", err)
	}
	to, err := db.StoragePolicyByID(ctx, opts.To)
	if err != nil {
		return report, fmt.Errorf("load target policy: %w", err)
	}
	report.FromPolicy, report.ToPolicy = from.ID, to.ID
	if from.ID == to.ID {
		return report, ErrSamePolicy
	}
	if from.Enabled {
		return report, fmt.Errorf("%w (%q)", ErrSourceEnabled, from.Name)
	}
	if !to.Enabled {
		return report, fmt.Errorf("%w (%q)", ErrTargetDisabled, to.Name)
	}
	if err := checkBusy(ctx, db, from.ID); err != nil {
		return report, err
	}

	// reg.Get opens a disabled policy read-only on purpose (that is what makes
	// its content downloadable); Writable refuses disabled targets.
	src, err := reg.Get(ctx, from.ID)
	if err != nil {
		return report, fmt.Errorf("open source root: %w", err)
	}
	dst, err := reg.Writable(ctx, to.ID)
	if err != nil {
		return report, fmt.Errorf("open target root: %w", err)
	}

	blobs, err := listPolicyBlobs(ctx, db, from.ID)
	if err != nil {
		return report, err
	}
	for _, b := range blobs {
		nodes, err := countBlobNodes(ctx, db, from.ID, b.ID)
		if err != nil {
			return report, err
		}
		if nodes != int64(b.Refcount) {
			return report, fmt.Errorf("%w: blob %d %s refcount=%d nodes=%d", ErrRefcountDrift, b.ID, b.SHA256, b.Refcount, nodes)
		}
		report.Blobs++
		report.Nodes += nodes
		report.Bytes += b.Size * nodes
		if opts.DryRun {
			continue
		}
		dstPath, err := dst.Path(b.SHA256)
		if err != nil {
			return report, fmt.Errorf("resolve target object %s: %w", b.SHA256, err)
		}
		if _, err := os.Stat(dstPath); err == nil {
			report.Reused++
		} else if !errors.Is(err, fs.ErrNotExist) {
			return report, fmt.Errorf("stat target object %s: %w", b.SHA256, err)
		} else {
			if err := copyBlob(ctx, src, dst, b.SHA256); err != nil {
				return report, fmt.Errorf("copy blob %s: %w", b.SHA256, err)
			}
			report.Copied++
		}
		if err := moveBlobRows(ctx, db, from.ID, to.ID, b, nodes); err != nil {
			return report, fmt.Errorf("move blob %s rows: %w", b.SHA256, err)
		}
		if err := src.Remove(b.SHA256); err != nil && !errors.Is(err, fs.ErrNotExist) {
			// The rows are gone already; leaving the file behind is safe (gc
			// reclaims it later) and must not fail a mostly successful run.
			log.Warn("cannot unlink source object after migration", "sha256", b.SHA256, "err", err)
		} else {
			report.RemovedFiles++
		}
		log.Info("migrated blob", "sha256", b.SHA256, "nodes", nodes, "size", b.Size)
	}
	return report, nil
}

func checkBusy(ctx context.Context, db *sqlstore.DB, policyID int64) error {
	var uploads int64
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM uploads WHERE storage_policy_id = ? AND state = 'open'`, policyID).Scan(&uploads); err != nil {
		return fmt.Errorf("count open uploads: %w", err)
	}
	var transfers int64
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM transfers WHERE storage_policy_id = ? AND state IN ('pending','running','paused')`, policyID).Scan(&transfers); err != nil {
		return fmt.Errorf("count active transfers: %w", err)
	}
	if uploads+transfers > 0 {
		return fmt.Errorf("%w: %d uploads, %d transfers", ErrPolicyBusy, uploads, transfers)
	}
	return nil
}

func listPolicyBlobs(ctx context.Context, db *sqlstore.DB, policyID int64) ([]sqlstore.Blob, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, storage_policy_id, sha256, size, refcount, created_at
		 FROM blobs WHERE storage_policy_id = ? ORDER BY id`, policyID)
	if err != nil {
		return nil, fmt.Errorf("list policy blobs: %w", err)
	}
	defer rows.Close()
	var out []sqlstore.Blob
	for rows.Next() {
		var b sqlstore.Blob
		var created int64
		if err := rows.Scan(&b.ID, &b.StoragePolicyID, &b.SHA256, &b.Size, &b.Refcount, &created); err != nil {
			return nil, fmt.Errorf("scan policy blob: %w", err)
		}
		b.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, b)
	}
	return out, rows.Err()
}

func countBlobNodes(ctx context.Context, db *sqlstore.DB, policyID, blobID int64) (int64, error) {
	var n int64
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes WHERE storage_policy_id = ? AND blob_id = ?`, policyID, blobID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count blob nodes: %w", err)
	}
	return n, nil
}

func copyBlob(ctx context.Context, src, dst *local.Store, sha256Hex string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rc, err := src.Open(sha256Hex)
	if err != nil {
		return fmt.Errorf("open source object: %w", err)
	}
	defer rc.Close()
	tmp, err := os.CreateTemp(dst.TempDir(), "migrate-*")
	if err != nil {
		return fmt.Errorf("create target temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := io.Copy(tmp, rc); err != nil {
		tmp.Close()
		return fmt.Errorf("copy object bytes: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync target temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close target temp: %w", err)
	}
	if err := dst.PublishPath(tmpName, sha256Hex); err != nil {
		return fmt.Errorf("publish target object: %w", err)
	}
	return nil
}

// moveBlobRows repoints the nodes, creates/updates the target blob row and
// deletes the source blob row in one write transaction. It re-checks the node
// count inside the transaction so a concurrent change cannot silently lose
// references.
func moveBlobRows(ctx context.Context, db *sqlstore.DB, fromID, toID int64, b sqlstore.Blob, nodes int64) error {
	return db.WithTx(ctx, func(tx *sql.Tx) error {
		var n int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM nodes WHERE storage_policy_id = ? AND blob_id = ?`, fromID, b.ID).Scan(&n); err != nil {
			return fmt.Errorf("recount blob nodes: %w", err)
		}
		if n != nodes {
			return fmt.Errorf("%w: blob %d changed during migration", ErrRefcountDrift, b.ID)
		}
		now := time.Now().Unix()
		var destID int64
		err := tx.QueryRowContext(ctx,
			`SELECT id FROM blobs WHERE storage_policy_id = ? AND sha256 = ?`, toID, b.SHA256).Scan(&destID)
		switch {
		case err == nil:
			if _, err := tx.ExecContext(ctx,
				`UPDATE blobs SET refcount = refcount + ? WHERE id = ?`, nodes, destID); err != nil {
				return fmt.Errorf("update target blob: %w", err)
			}
		case errors.Is(err, sql.ErrNoRows):
			res, err := tx.ExecContext(ctx,
				`INSERT INTO blobs (storage_policy_id, sha256, size, refcount, created_at) VALUES (?, ?, ?, ?, ?)`,
				toID, b.SHA256, b.Size, nodes, now)
			if err != nil {
				return fmt.Errorf("insert target blob: %w", err)
			}
			destID, err = res.LastInsertId()
			if err != nil {
				return fmt.Errorf("target blob id: %w", err)
			}
		default:
			return fmt.Errorf("load target blob: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE nodes SET storage_policy_id = ?, blob_id = ? WHERE storage_policy_id = ? AND blob_id = ?`,
			toID, destID, fromID, b.ID); err != nil {
			return fmt.Errorf("repoint nodes: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM blobs WHERE id = ? AND storage_policy_id = ?`, b.ID, fromID); err != nil {
			return fmt.Errorf("delete source blob: %w", err)
		}
		return nil
	})
}
