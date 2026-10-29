// Storage policies (M5.3): the admin side of the multi-root storage feature.
//
// A policy is one local root directory that files can live in. Everything the
// tree needs at runtime comes from the registry (internal/infra/storage); what
// is validated here is only what an operator can get wrong: the name, the root
// path, and the invariants that keep exactly one default policy and at least one
// policy alive.
package admin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	sqlstore "rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
)

const (
	// MinStorageNameLen and MaxStorageNameLen bound the operator-visible name.
	MinStorageNameLen = 1
	MaxStorageNameLen = 64
	// storageDirPerm is what a newly created root gets; the storage layer only
	// ever needs to read and write inside it.
	storageDirPerm = 0o750
)

// StoragePolicyRequest is the POST /api/v1/admin/storage payload. The optional
// flags default to enabled=true, is_default=false.
type StoragePolicyRequest struct {
	Name      string
	RootPath  string
	Enabled   *bool
	IsDefault *bool
}

// StoragePolicyPatch is the PATCH /api/v1/admin/storage/{id} payload; nil
// fields keep their current value.
type StoragePolicyPatch struct {
	Name      *string
	RootPath  *string
	Enabled   *bool
	IsDefault *bool
}

// ListStoragePolicies returns every policy with its node/file counts. The admin
// console shows disabled ones too.
func (s *Service) ListStoragePolicies(ctx context.Context) ([]sqlstore.StoragePolicy, error) {
	return s.db.ListStoragePolicies(ctx, true)
}

// EnabledStoragePolicies lists the policies users may upload into.
func (s *Service) EnabledStoragePolicies(ctx context.Context) ([]sqlstore.StoragePolicy, error) {
	return s.db.EnabledStoragePolicies(ctx)
}

// CreateStoragePolicy validates and inserts a new root. The path is created and
// probed for writability before the row exists, so a policy never points at a
// directory the server cannot use.
func (s *Service) CreateStoragePolicy(ctx context.Context, req StoragePolicyRequest) (sqlstore.StoragePolicy, error) {
	name, err := s.validateStorageName(ctx, req.Name, 0)
	if err != nil {
		return sqlstore.StoragePolicy{}, err
	}
	root, err := s.resolveStorageRoot(ctx, req.RootPath, 0)
	if err != nil {
		return sqlstore.StoragePolicy{}, err
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	isDefault := req.IsDefault != nil && *req.IsDefault
	p, err := s.db.CreateStoragePolicy(ctx, sqlstore.NewStoragePolicy{
		Name:      name,
		RootPath:  root,
		Enabled:   enabled,
		IsDefault: isDefault,
	})
	if err != nil {
		return sqlstore.StoragePolicy{}, err
	}
	return p, nil
}

// UpdateStoragePolicy applies a partial update. Changing root_path is only
// allowed while the policy holds no node and no blob: moving a populated root
// would strand its content.
func (s *Service) UpdateStoragePolicy(ctx context.Context, id int64, patch StoragePolicyPatch) (sqlstore.StoragePolicy, error) {
	cur, err := s.db.StoragePolicyByID(ctx, id)
	if err != nil {
		return sqlstore.StoragePolicy{}, err
	}
	if err != nil {
		return sqlstore.StoragePolicy{}, err
	}
	var up sqlstore.StoragePolicyUpdate
	if patch.Name != nil {
		name, err := s.validateStorageName(ctx, *patch.Name, id)
		if err != nil {
			return sqlstore.StoragePolicy{}, err
		}
		up.Name = &name
	}
	if patch.RootPath != nil {
		root := strings.TrimSpace(*patch.RootPath)
		if filepath.Clean(root) != filepath.Clean(cur.RootPath) {
			nodes, blobs, err := s.db.CountStoragePolicyUsage(ctx, id)
			if err != nil {
				return sqlstore.StoragePolicy{}, err
			}
			if nodes > 0 || blobs > 0 {
				return sqlstore.StoragePolicy{}, apperr.ErrInvalidState.WithReason(apperr.ReasonStorageInUse)
			}
			resolved, err := s.resolveStorageRoot(ctx, root, id)
			if err != nil {
				return sqlstore.StoragePolicy{}, err
			}
			up.RootPath = &resolved
		}
	}
	if patch.Enabled != nil {
		// The default policy must stay enabled: it is where every new root-level
		// entry lands, and a disabled default would make uploads fail with 507.
		if !*patch.Enabled && cur.IsDefault {
			return sqlstore.StoragePolicy{}, apperr.ErrInvalidState.WithReason(apperr.ReasonDefaultPolicy)
		}
		up.Enabled = patch.Enabled
	}
	if patch.IsDefault != nil && *patch.IsDefault {
		up.IsDefault = patch.IsDefault
	}
	p, err := s.db.UpdateStoragePolicy(ctx, id, up)
	if err != nil {
		return sqlstore.StoragePolicy{}, err
	}
	return p, nil
}

// DeleteStoragePolicy removes an empty policy. The default policy and the last
// remaining policy are protected.
func (s *Service) DeleteStoragePolicy(ctx context.Context, id int64) error {
	cur, err := s.db.StoragePolicyByID(ctx, id)
	if err != nil {
		return err
	}
	// The default-flag and last-policy checks come first: a database must keep
	// one default root, so "in use" would be a misleading answer for it.
	if cur.IsDefault {
		return apperr.ErrInvalidState.WithReason(apperr.ReasonDefaultPolicy)
	}
	policies, err := s.db.ListStoragePolicies(ctx, true)
	if err != nil {
		return err
	}
	if len(policies) <= 1 {
		return apperr.ErrInvalidState.WithReason(apperr.ReasonLastPolicy)
	}
	nodes, blobs, err := s.db.CountStoragePolicyUsage(ctx, id)
	if err != nil {
		return err
	}
	if nodes > 0 || blobs > 0 {
		return apperr.ErrInvalidState.WithReason(apperr.ReasonStorageInUse)
	}
	if err := s.db.DeleteStoragePolicy(ctx, id); err != nil {
		return err
	}
	return nil
}

// validateStorageName trims and bounds the name and rejects a duplicate. selfID
// is the policy being updated (0 when creating).
func (s *Service) validateStorageName(ctx context.Context, raw string, selfID int64) (string, error) {
	name := strings.TrimSpace(raw)
	if n := utf8.RuneCountInString(name); n < MinStorageNameLen || n > MaxStorageNameLen {
		return "", apperr.ErrInvalidName.WithReason(apperr.ReasonInvalidValue)
	}
	policies, err := s.db.ListStoragePolicies(ctx, true)
	if err != nil {
		return "", err
	}
	for _, p := range policies {
		if p.ID != selfID && p.Name == name {
			return "", apperr.ErrNameConflict.WithReason(apperr.ReasonInvalidValue)
		}
	}
	return name, nil
}

// resolveStorageRoot canonicalises and prepares a policy root. The checks run
// in the order an operator would hit them: absolute path, creatable, real
// directory (no symlink), writable, and finally no overlap with another
// policy's root — two policies must never be able to serve the same file.
func (s *Service) resolveStorageRoot(ctx context.Context, raw string, selfID int64) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || !filepath.IsAbs(trimmed) {
		return "", apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidValue)
	}
	root := filepath.Clean(trimmed)
	if err := os.MkdirAll(root, storageDirPerm); err != nil {
		s.log.Warn("storage root not creatable", "root", root, "err", err)
		return "", apperr.ErrBadRequest.WithReason(apperr.ReasonNotWritable).WithCause(err)
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		s.log.Warn("storage root not resolvable", "root", root, "err", err)
		return "", apperr.ErrBadRequest.WithReason(apperr.ReasonNotWritable).WithCause(err)
	}
	if filepath.Clean(resolved) != root {
		return "", apperr.ErrBadRequest.WithReason(apperr.ReasonSymlinkRoot)
	}
	probe, err := os.CreateTemp(root, ".probe-*")
	if err != nil {
		s.log.Warn("storage root not writable", "root", root, "err", err)
		return "", apperr.ErrBadRequest.WithReason(apperr.ReasonNotWritable).WithCause(err)
	}
	probeName := probe.Name()
	if err := probe.Close(); err != nil {
		return "", apperr.ErrBadRequest.WithReason(apperr.ReasonNotWritable).WithCause(err)
	}
	if err := os.Remove(probeName); err != nil {
		return "", apperr.ErrBadRequest.WithReason(apperr.ReasonNotWritable).WithCause(err)
	}
	policies, err := s.db.ListStoragePolicies(ctx, true)
	if err != nil {
		return "", err
	}
	for _, other := range policies {
		if other.ID == selfID {
			continue
		}
		otherRoot := filepath.Clean(other.RootPath)
		if root == otherRoot || isWithinDir(root, otherRoot) || isWithinDir(otherRoot, root) {
			return "", apperr.ErrInvalidState.WithReason(apperr.ReasonPathOverlap)
		}
	}
	return root, nil
}

// isWithinDir reports whether child is strictly inside parent.
func isWithinDir(child, parent string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
