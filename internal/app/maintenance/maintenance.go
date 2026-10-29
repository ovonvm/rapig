// Package maintenance implements the offline storage maintenance commands:
// blob garbage collection and a filesystem/database consistency check. Since
// M5.3 both walk every storage policy root (enabled or not) and report a
// per-policy summary next to the deployment-wide totals.
package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/infra/storage/local"
)

// GCGrace is how long an unreferenced blob must exist before gc deletes it.
// The grace period keeps a blob that is being uploaded but whose node row does
// not exist yet out of reach.
const GCGrace = 24 * time.Hour

// GCPolicy is the per-storage-policy part of a gc pass.
type GCPolicy struct {
	PolicyID   int64
	RootPath   string
	Removed    int
	BytesFreed int64
}

// GCResult summarises one garbage-collection pass. Removed/BytesFreed are the
// totals across policies; Policies carries the per-root breakdown.
type GCResult struct {
	Removed    int
	BytesFreed int64
	DryRun     bool
	Policies   []GCPolicy
}

// GC deletes unreferenced blobs older than GCGrace, first from disk and then
// from the database, walking every policy root. Deleting the file first is
// safe: a crash in between leaves a zero-reference row that the next run
// removes. An orphan in one root never makes gc touch another root's files.
func GC(ctx context.Context, db *sqlstore.DB, registry *storagereg.Registry, dryRun bool, now time.Time, log *slog.Logger) (GCResult, error) {
	roots, err := registry.All(ctx)
	if err != nil {
		return GCResult{}, err
	}
	// A blob whose refcount drifted to 0 but that a node still points at must
	// not be deleted; one query covers every policy.
	nodeCounts, err := db.BlobNodeCounts(ctx)
	if err != nil {
		return GCResult{}, err
	}
	cutoff := now.Add(-GCGrace)
	res := GCResult{DryRun: dryRun, Policies: make([]GCPolicy, 0, len(roots))}
	for _, root := range roots {
		store, err := registry.Get(ctx, root.ID)
		if err != nil {
			return res, fmt.Errorf("gc policy %d (%s): %w", root.ID, root.RootPath, err)
		}
		orphans, err := db.OrphanBlobsByPolicy(ctx, root.ID, cutoff)
		if err != nil {
			return res, err
		}
		p := GCPolicy{PolicyID: root.ID, RootPath: root.RootPath}
		for _, b := range orphans {
			if nodeCounts[b.ID] > 0 {
				if log != nil {
					log.Warn("gc: blob still referenced by a node, skipping",
						"policy_id", b.StoragePolicyID, "blob_id", b.ID, "sha256", b.SHA256)
				}
				continue
			}
			if dryRun {
				p.Removed++
				p.BytesFreed += b.Size
				continue
			}
			if err := store.Remove(b.SHA256); err != nil {
				res.Policies = append(res.Policies, p)
				return res, fmt.Errorf("remove blob file %s in policy %d: %w", b.SHA256, root.ID, err)
			}
			if err := db.DeleteBlobRow(ctx, b.ID); err != nil {
				res.Policies = append(res.Policies, p)
				return res, err
			}
			p.Removed++
			p.BytesFreed += b.Size
		}
		res.Removed += p.Removed
		res.BytesFreed += p.BytesFreed
		res.Policies = append(res.Policies, p)
		if log != nil {
			log.Info("gc policy finished",
				"policy_id", root.ID, "root", root.RootPath, "enabled", root.Enabled,
				"blobs", p.Removed, "bytes", p.BytesFreed)
		}
	}
	if log != nil {
		log.Info("gc finished", "dry_run", dryRun, "blobs", res.Removed, "bytes", res.BytesFreed,
			"policies", len(res.Policies))
	}
	return res, nil
}

// PolicyFsck is the per-storage-policy part of an fsck pass.
type PolicyFsck struct {
	PolicyID         int64
	RootPath         string
	Blobs            int
	MissingFiles     []string
	SizeMismatches   []string
	BadContent       []string
	RefcountRepaired int
}

// FsckReport lists every consistency problem found by Fsck. The top-level lists
// stay the deployment-wide totals (the existing JSON shape); Policies adds the
// per-root breakdown.
type FsckReport struct {
	Blobs            int
	MissingFiles     []string
	SizeMismatches   []string
	BadContent       []string
	BrokenNodeBlobs  []int64
	ForeignNodeBlobs []int64
	RefcountRepaired int
	Policies         []PolicyFsck
}

// Issues reports whether the check found anything that needs attention.
func (r FsckReport) Issues() int {
	return len(r.MissingFiles) + len(r.SizeMismatches) + len(r.BadContent) +
		len(r.BrokenNodeBlobs) + len(r.ForeignNodeBlobs) + r.RefcountRepaired
}

// Fsck verifies that every blob row has a matching file of the right size (and
// optionally content) and that every node's blob exists and belongs to the same
// storage policy. With repair it also rebuilds blobs.refcount from the node
// rows, which is the only derived value that can drift without being detected
// by the checks above.
func Fsck(ctx context.Context, db *sqlstore.DB, registry *storagereg.Registry, verifyContent, repair bool, log *slog.Logger) (FsckReport, error) {
	roots, err := registry.All(ctx)
	if err != nil {
		return FsckReport{}, err
	}
	rep := FsckReport{Policies: make([]PolicyFsck, 0, len(roots))}
	var all []sqlstore.Blob
	for _, root := range roots {
		store, err := registry.Get(ctx, root.ID)
		if err != nil {
			return rep, fmt.Errorf("fsck policy %d (%s): %w", root.ID, root.RootPath, err)
		}
		blobs, err := db.BlobsByPolicy(ctx, root.ID)
		if err != nil {
			return rep, err
		}
		all = append(all, blobs...)
		p := PolicyFsck{PolicyID: root.ID, RootPath: root.RootPath, Blobs: len(blobs)}
		for _, b := range blobs {
			path, err := store.Path(b.SHA256)
			if err != nil {
				// The row itself is invalid (bad sha); treat the file as missing.
				p.MissingFiles = append(p.MissingFiles, b.SHA256)
				continue
			}
			info, err := os.Stat(path)
			if err != nil {
				p.MissingFiles = append(p.MissingFiles, b.SHA256)
				continue
			}
			if info.Size() != b.Size {
				p.SizeMismatches = append(p.SizeMismatches, b.SHA256)
				continue
			}
			if verifyContent {
				ok, err := verifySHA(store, b.SHA256)
				if err != nil {
					return rep, err
				}
				if !ok {
					p.BadContent = append(p.BadContent, b.SHA256)
				}
			}
		}
		rep.Blobs += p.Blobs
		rep.MissingFiles = append(rep.MissingFiles, p.MissingFiles...)
		rep.SizeMismatches = append(rep.SizeMismatches, p.SizeMismatches...)
		rep.BadContent = append(rep.BadContent, p.BadContent...)
		rep.Policies = append(rep.Policies, p)
	}

	broken, err := db.NodesWithMissingBlob(ctx)
	if err != nil {
		return rep, err
	}
	rep.BrokenNodeBlobs = broken

	foreign, err := db.NodesWithForeignBlob(ctx)
	if err != nil {
		return rep, err
	}
	rep.ForeignNodeBlobs = foreign

	if repair {
		counts, err := db.BlobNodeCounts(ctx)
		if err != nil {
			return rep, err
		}
		for _, b := range all {
			want := counts[b.ID]
			if int64(b.Refcount) == want {
				continue
			}
			if err := db.SetBlobRefcount(ctx, b.ID, want); err != nil {
				return rep, err
			}
			rep.RefcountRepaired++
			for i := range rep.Policies {
				if rep.Policies[i].PolicyID == b.StoragePolicyID {
					rep.Policies[i].RefcountRepaired++
					break
				}
			}
		}
	}

	if log != nil {
		log.Info("fsck finished",
			"blobs", rep.Blobs,
			"missing_files", len(rep.MissingFiles),
			"size_mismatches", len(rep.SizeMismatches),
			"bad_content", len(rep.BadContent),
			"broken_node_blobs", len(rep.BrokenNodeBlobs),
			"foreign_node_blobs", len(rep.ForeignNodeBlobs),
			"refcount_repaired", rep.RefcountRepaired,
			"policies", len(rep.Policies))
	}
	return rep, nil
}

func verifySHA(store *local.Store, sha string) (bool, error) {
	f, err := store.OpenFile(sha)
	if err != nil {
		return false, nil
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, fmt.Errorf("read blob %s: %w", sha, err)
	}
	return hex.EncodeToString(h.Sum(nil)) == sha, nil
}
