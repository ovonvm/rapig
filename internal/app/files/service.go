// Package files implements the file tree application service. It resolves
// parents inside the caller's scope, writes through the scoped repository and
// maps node storage policies to object stores through the storage registry
// (M5.3); policy checks stay in the transport layer.
package files

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/infra/storage/local"
	"rapig/internal/platform/apperr"
)

// Service is the file tree application service.
type Service struct {
	db       *sqlstore.DB
	registry *storagereg.Registry
	log      *slog.Logger
}

// NewService builds the file service over the storage policy registry. The
// default policy is resolved per request, so changing is_default takes effect
// without a restart.
func NewService(db *sqlstore.DB, registry *storagereg.Registry, log *slog.Logger) *Service {
	return &Service{db: db, registry: registry, log: log}
}

// Parent resolves a parent reference inside the caller's scope. ref == 0
// means the caller's root ("my files"). Any foreign or missing node maps to
// apperr.ErrParentNotFound, which the transport renders as 404.
func (s *Service) Parent(ctx context.Context, p access.Principal, ref int64) (domainfiles.Node, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return domainfiles.Node{}, err
	}
	if ref == 0 {
		n, err := s.db.RootNode(ctx, sc)
		if err == nil {
			return n, nil
		}
		if e, ok := apperr.From(err); !ok || e.Code != apperr.CodeNotFound {
			return domainfiles.Node{}, err
		}
		policyID, err := s.DefaultPolicyID(ctx)
		if err != nil {
			return domainfiles.Node{}, err
		}
		return s.db.EnsureRoot(ctx, p.UserID, policyID)
	}
	n, err := s.db.NodeByID(ctx, sc, ref)
	if err != nil {
		if e, ok := apperr.From(err); ok && e.Code == apperr.CodeNotFound {
			return domainfiles.Node{}, apperr.ErrParentNotFound
		}
		return domainfiles.Node{}, err
	}
	if n.State != domainfiles.StateActive || n.Kind != domainfiles.KindDir {
		return domainfiles.Node{}, apperr.ErrParentNotFound
	}
	return n, nil
}

// DefaultPolicyID returns the policy new root-level entries land in.
func (s *Service) DefaultPolicyID(ctx context.Context) (int64, error) {
	id, _, err := s.registry.Default(ctx)
	if err != nil {
		if s.log != nil {
			s.log.Error("default storage policy unavailable", "err", err)
		}
		return 0, apperr.ErrInternal.WithCause(err)
	}
	return id, nil
}

// PolicyFor resolves the storage policy a child of parent belongs to: the
// parent's own policy (the tree inherits it down to the leaves), falling back to
// the default policy for a root that predates M5.3.
func (s *Service) PolicyFor(ctx context.Context, parent domainfiles.Node) (int64, error) {
	if parent.StoragePolicyID != nil {
		return *parent.StoragePolicyID, nil
	}
	return s.DefaultPolicyID(ctx)
}

// WritablePolicy checks that a policy exists and accepts new content, mapping a
// disabled policy to 507 insufficient_storage and a missing one to 400
// invalid_target. Callers use it before accepting an explicit policy id.
func (s *Service) WritablePolicy(ctx context.Context, policyID int64) error {
	if _, err := s.registry.Writable(ctx, policyID); err != nil {
		return s.policyWriteError(err)
	}
	return nil
}

// policyWriteError maps registry policy errors onto the API contract.
func (s *Service) policyWriteError(err error) error {
	switch {
	case errors.Is(err, storagereg.ErrPolicyNotFound):
		return apperr.ErrInvalidTarget.WithReason(apperr.ReasonInvalidValue).WithCause(err)
	case errors.Is(err, storagereg.ErrPolicyDisabled):
		return apperr.ErrInsufficientStorage.WithReason(apperr.ReasonStoragePolicyDisabled).WithCause(err)
	default:
		if s.log != nil {
			s.log.Error("storage policy write check failed", "err", err)
		}
		return apperr.ErrInternal.WithCause(err)
	}
}

// Store resolves the object store of a node's storage policy. Reads stay
// allowed for a disabled policy: disabling only stops new writes. A policy that
// no longer exists is a server error (the tree now points at a lost root).
func (s *Service) Store(ctx context.Context, n domainfiles.Node) (*local.Store, error) {
	var policyID int64
	if n.StoragePolicyID != nil {
		policyID = *n.StoragePolicyID
	}
	store, err := s.registry.Get(ctx, policyID)
	if err != nil {
		if s.log != nil {
			s.log.Error("node storage policy unavailable",
				"node_id", n.ID, "storage_policy_id", policyID, "err", err)
		}
		return nil, apperr.ErrInternal.WithCause(err)
	}
	return store, nil
}

// Content loads a file node's content metadata together with the store its
// bytes live in, so a download never reads the wrong root.
func (s *Service) Content(ctx context.Context, p access.Principal, id int64) (domainfiles.Node, string, string, *local.Store, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return domainfiles.Node{}, "", "", nil, err
	}
	n, sha, mime, err := s.db.FileContent(ctx, sc, id)
	if err != nil {
		return domainfiles.Node{}, "", "", nil, err
	}
	store, err := s.Store(ctx, n)
	if err != nil {
		return domainfiles.Node{}, "", "", nil, err
	}
	return n, sha, mime, store, nil
}

// Children lists a directory's active children.
func (s *Service) Children(ctx context.Context, p access.Principal, parent domainfiles.Node, limit, offset int) ([]domainfiles.Node, int, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	return s.db.ListChildren(ctx, sc, parent.ID, limit, offset)
}

// Search finds the caller's active nodes by filename. kind is "" (all),
// domainfiles.KindFile or domainfiles.KindDir; recent orders by modification
// time instead of relevance.
func (s *Service) Search(ctx context.Context, p access.Principal, query, kind string, recent bool, limit, offset int) ([]domainfiles.Node, int, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	return s.db.SearchNodes(ctx, sc, query, kind, recent, limit, offset)
}

// SearchGroup finds active nodes inside one group space. The transport layer
// checks read permission on the group first; the repository additionally
// confines the scan to groups inside the caller's scope, so a bug cannot widen
// it. Personal nodes never appear in the result.
func (s *Service) SearchGroup(ctx context.Context, p access.Principal, groupID int64, query, kind string, recent bool, limit, offset int) ([]domainfiles.Node, int, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	return s.db.SearchGroupNodes(ctx, sc, groupID, query, kind, recent, limit, offset)
}

// RootOf returns the root of the space n lives in: the caller's personal root,
// or the root of the group space n belongs to. The API reports "the root" as
// parent 0, which therefore has to mean the root of the node's own space.
func (s *Service) RootOf(ctx context.Context, p access.Principal, n domainfiles.Node) (domainfiles.Node, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return domainfiles.Node{}, err
	}
	return s.db.RootForNode(ctx, sc, n)
}

// GroupRoot returns the root node of one group space. Membership is enforced by
// the repository's access predicate; non-members get apperr.ErrNotFound.
func (s *Service) GroupRoot(ctx context.Context, p access.Principal, groupID int64) (domainfiles.Node, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return domainfiles.Node{}, err
	}
	if groupID <= 0 {
		return domainfiles.Node{}, apperr.ErrNotFound
	}
	return s.db.GroupRootNode(ctx, sc, groupID)
}

// CreateDir creates a directory under parent. The new node inherits the
// parent's storage policy. policyID may only be set for a directory created
// directly under the owner's root ("my files"); it must exist and be enabled.
func (s *Service) CreateDir(ctx context.Context, p access.Principal, parent domainfiles.Node, name string, policyID *int64) (domainfiles.Node, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return domainfiles.Node{}, err
	}
	if err := domainfiles.ValidateName(name); err != nil {
		return domainfiles.Node{}, err
	}
	if parent.Kind != domainfiles.KindDir {
		return domainfiles.Node{}, apperr.ErrParentNotFound
	}
	target := parent
	if policyID != nil {
		if !parent.IsRoot() {
			return domainfiles.Node{}, apperr.ErrInvalidTarget.WithReason(apperr.ReasonInvalidValue).WithCause(
				errors.New("storage_policy_id is only allowed directly under the root"))
		}
		policy, err := s.registry.Policy(ctx, *policyID)
		if err != nil {
			return domainfiles.Node{}, apperr.ErrInvalidTarget.WithReason(apperr.ReasonInvalidValue).WithCause(err)
		}
		if !policy.Enabled {
			return domainfiles.Node{}, apperr.ErrInvalidTarget.WithReason(apperr.ReasonStoragePolicyDisabled)
		}
		// A directory created in another policy hangs off the root of the
		// caller's tree but its descendants stay in that policy. The parent id
		// is still the root node: the tree shape does not change, only the
		// policy the subtree stores into.
		chosen := *policyID
		target.StoragePolicyID = &chosen
	}
	nodePolicy, err := s.PolicyFor(ctx, target)
	if err != nil {
		return domainfiles.Node{}, err
	}
	if _, err := s.registry.Writable(ctx, nodePolicy); err != nil {
		return domainfiles.Node{}, s.policyWriteError(err)
	}
	rel := domainfiles.ChildPath(parent.Path, name)
	var n domainfiles.Node
	if policyID != nil {
		n, err = s.db.CreateDirInPolicy(ctx, sc, parent.ID, name, rel, nodePolicy)
	} else {
		n, err = s.db.CreateDir(ctx, sc, parent.ID, name, rel)
	}
	if err != nil {
		if errors.Is(err, apperr.ErrNameConflict) {
			return domainfiles.Node{}, apperr.ErrNameConflict
		}
		return domainfiles.Node{}, err
	}
	return n, nil
}

// Node loads one node inside the caller's scope. Trashed nodes are returned so
// the recycle-bin handlers can restore or purge them; foreign and purged nodes
// map to apperr.ErrNotFound.
func (s *Service) Node(ctx context.Context, p access.Principal, id int64) (domainfiles.Node, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return domainfiles.Node{}, err
	}
	return s.db.NodeByID(ctx, sc, id)
}

// Copy duplicates a file or directory subtree under parentRef (0 = root) and
// returns the new copy root. Blob contents are shared; quota is charged by the
// repository inside the same transaction. A copy across storage policies is
// refused by the repository with 409 cross_policy_operation.
func (s *Service) Copy(ctx context.Context, p access.Principal, id int64, parentRef *int64, newName *string) (domainfiles.Node, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return domainfiles.Node{}, err
	}
	return s.db.CopyNode(ctx, sc, id, parentRef, newName, time.Now().UTC())
}
