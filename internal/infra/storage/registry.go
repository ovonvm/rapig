// Package storage maps storage policy rows to concrete object stores.
//
// M5.3 lets one deployment serve several local roots. The policy row is the
// source of truth (root path, enabled flag); this registry is the runtime view
// of it: it opens a local.Store per policy on first use, probes it, and caches
// the result until an admin changes the root (Invalidate). Keeping the mapping
// here means the application services never touch storage_policies/root_path
// directly, and a policy that is disabled is still readable — only new writes
// have to go through Enabled checks in the caller.
package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"rapig/internal/infra/sql"
	"rapig/internal/infra/storage/local"
	"rapig/internal/platform/apperr"
)

// ErrPolicyNotFound is returned when a policy id does not exist. It is a
// sentinel so callers can map it to a 404/500 without importing sqlstore error
// codes.
var ErrPolicyNotFound = errors.New("storage policy not found")

// ErrPolicyDisabled is returned by Writable for a policy that still serves its
// content but must not receive new writes.
var ErrPolicyDisabled = errors.New("storage policy disabled")

// PolicyRoot is the registry's view of one policy, used by gc/fsck/backup which
// need every root (including disabled ones).
type PolicyRoot struct {
	ID       int64
	RootPath string
	Enabled  bool
}

// Registry opens and caches object stores per storage policy.
type Registry struct {
	db     *sqlstore.DB
	log    *slog.Logger
	mu     sync.RWMutex
	stores map[int64]*local.Store
}

// NewRegistry builds a registry over db. Stores are opened lazily.
func NewRegistry(db *sqlstore.DB, log *slog.Logger) *Registry {
	return &Registry{db: db, log: log, stores: make(map[int64]*local.Store)}
}

// Get returns the store for one policy, opening (and probing) it on first use.
// A missing policy is ErrPolicyNotFound. Disabled policies are returned too:
// their content stays downloadable, only new writes are refused by callers.
func (r *Registry) Get(ctx context.Context, policyID int64) (*local.Store, error) {
	if policyID <= 0 {
		return nil, fmt.Errorf("%w: id %d", ErrPolicyNotFound, policyID)
	}
	r.mu.RLock()
	store, ok := r.stores[policyID]
	r.mu.RUnlock()
	if ok {
		return store, nil
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if store, ok := r.stores[policyID]; ok {
		return store, nil
	}
	policy, err := r.db.StoragePolicyByID(ctx, policyID)
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return nil, fmt.Errorf("%w: id %d", ErrPolicyNotFound, policyID)
		}
		return nil, err
	}
	store, err = openStore(ctx, policy.RootPath)
	if err != nil {
		return nil, err
	}
	r.stores[policyID] = store
	if r.log != nil {
		r.log.Info("storage policy opened", "policy_id", policy.ID, "root", policy.RootPath, "enabled", policy.Enabled)
	}
	return store, nil
}

// Policy returns the policy row itself. Application services use it to check
// enabled/root before they accept a write; the row is read through, not cached,
// so an admin change is visible immediately.
func (r *Registry) Policy(ctx context.Context, policyID int64) (sqlstore.StoragePolicy, error) {
	policy, err := r.db.StoragePolicyByID(ctx, policyID)
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return sqlstore.StoragePolicy{}, fmt.Errorf("%w: id %d", ErrPolicyNotFound, policyID)
		}
		return sqlstore.StoragePolicy{}, err
	}
	return policy, nil
}

// Writable returns the store of a policy that may receive new content. A
// missing policy is ErrPolicyNotFound, a disabled one ErrPolicyDisabled; both
// are typed sentinels so callers map them to the right HTTP status (400/507 for
// uploads).
func (r *Registry) Writable(ctx context.Context, policyID int64) (*local.Store, error) {
	policy, err := r.Policy(ctx, policyID)
	if err != nil {
		return nil, err
	}
	if !policy.Enabled {
		return nil, fmt.Errorf("%w: id %d (%s)", ErrPolicyDisabled, policy.ID, policy.Name)
	}
	return r.Get(ctx, policyID)
}

// Default returns the is_default policy id and its store.
func (r *Registry) Default(ctx context.Context) (int64, *local.Store, error) {
	policy, err := r.db.DefaultStoragePolicy(ctx)
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return 0, nil, fmt.Errorf("%w: no default policy", ErrPolicyNotFound)
		}
		return 0, nil, err
	}
	store, err := r.Get(ctx, policy.ID)
	if err != nil {
		return 0, nil, err
	}
	return policy.ID, store, nil
}

// Invalidate drops the cached store of one policy so the next Get re-reads the
// row. Admin handlers call it after changing root_path.
func (r *Registry) Invalidate(policyID int64) {
	r.mu.Lock()
	delete(r.stores, policyID)
	r.mu.Unlock()
}

// All lists every policy root, disabled ones included. gc, fsck, backup and the
// startup probe iterate this.
func (r *Registry) All(ctx context.Context) ([]PolicyRoot, error) {
	policies, err := r.db.ListStoragePolicies(ctx, true)
	if err != nil {
		return nil, err
	}
	out := make([]PolicyRoot, 0, len(policies))
	for _, p := range policies {
		out = append(out, PolicyRoot{ID: p.ID, RootPath: p.RootPath, Enabled: p.Enabled})
	}
	return out, nil
}

// ProbeEnabled opens and probes every enabled policy. It runs at startup so a
// broken root fails fast (and loudly) instead of failing the first upload.
func (r *Registry) ProbeEnabled(ctx context.Context) error {
	policies, err := r.db.EnabledStoragePolicies(ctx)
	if err != nil {
		return err
	}
	for _, p := range policies {
		if _, err := r.Get(ctx, p.ID); err != nil {
			return fmt.Errorf("storage policy %d (%s): %w", p.ID, p.RootPath, err)
		}
	}
	return nil
}

func openStore(ctx context.Context, root string) (*local.Store, error) {
	store, err := local.New(root)
	if err != nil {
		return nil, fmt.Errorf("open storage root %s: %w", root, err)
	}
	if err := store.Probe(ctx); err != nil {
		return nil, fmt.Errorf("storage root %s unusable: %w", root, err)
	}
	return store, nil
}
