package files

import (
	"context"
	"strconv"
	"time"

	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	"rapig/internal/infra/sql"
)

// TrashEntry is one top-level item in the recycle bin.
type TrashEntry struct {
	Node      domainfiles.Node
	ExpiresAt time.Time
}

// TrashRetentionDays reads settings.trash_retention_days with the M2 default.
func (s *Service) TrashRetentionDays(ctx context.Context) int {
	fallback := sqlstore.DefaultTrashRetentionDays
	raw, ok, err := s.db.Setting(ctx, sqlstore.SettingTrashRetentionDays)
	if err != nil || !ok || raw == "" {
		return fallback
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > 3650 {
		return fallback
	}
	return days
}

// Trash moves a node (and, for directories, its subtree by parentage) to the
// recycle bin. Quota stays reserved until purge.
func (s *Service) Trash(ctx context.Context, p access.Principal, node domainfiles.Node) (domainfiles.Node, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return domainfiles.Node{}, err
	}
	return s.db.TrashNode(ctx, sc, node.ID, time.Now())
}

// TrashList returns top-level recycle bin entries plus the total count.
func (s *Service) TrashList(ctx context.Context, p access.Principal, limit, offset int) ([]TrashEntry, int, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	nodes, total, err := s.db.ListTrash(ctx, sc, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	days := s.TrashRetentionDays(ctx)
	out := make([]TrashEntry, 0, len(nodes))
	for _, n := range nodes {
		e := TrashEntry{Node: n}
		if n.TrashedAt != nil {
			e.ExpiresAt = n.TrashedAt.Add(time.Duration(days) * 24 * time.Hour)
		}
		out = append(out, e)
	}
	return out, total, nil
}

// Restore puts a trashed node back, renaming on conflict.
func (s *Service) Restore(ctx context.Context, p access.Principal, node domainfiles.Node) (domainfiles.Node, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return domainfiles.Node{}, err
	}
	return s.db.RestoreNode(ctx, sc, node.ID, time.Now())
}

// PurgeGroup permanently deletes a node inside a group space. 组共享空间没有
// 回收站：DELETE 语义就是永久删除，需要 delete 权限（由 transport 层判定）。
// 个人空间的 Purge 走回收站清理，两者共用底层子树删除与配额清算。
func (s *Service) PurgeGroup(ctx context.Context, p access.Principal, node domainfiles.Node) error {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return err
	}
	orphans, err := s.db.PurgeGroupNode(ctx, sc, node.ID, time.Now())
	if err != nil {
		return err
	}
	s.removeObjects(orphans)
	return nil
}

// Purge permanently deletes a node and its subtree, releasing quota and
// deleting objects that lost their last reference.
func (s *Service) Purge(ctx context.Context, p access.Principal, node domainfiles.Node) error {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return err
	}
	orphans, err := s.db.PurgeNode(ctx, sc, node.ID, time.Now())
	if err != nil {
		return err
	}
	s.removeObjects(orphans)
	return nil
}

// EmptyTrash purges every top-level trashed entry of the caller.
func (s *Service) EmptyTrash(ctx context.Context, p access.Principal) (int, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return 0, err
	}
	n, orphans, err := s.db.EmptyTrash(ctx, sc, time.Now())
	if err != nil {
		return 0, err
	}
	s.removeObjects(orphans)
	return n, nil
}

// PurgeExpiredTrash removes trash entries older than the configured retention.
// It is maintenance code and runs across all owners.
func (s *Service) PurgeExpiredTrash(ctx context.Context, limit int) (int, error) {
	days := s.TrashRetentionDays(ctx)
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	n, orphans, err := s.db.PurgeExpiredTrash(ctx, cutoff, limit)
	if err != nil {
		return 0, err
	}
	s.removeObjects(orphans)
	return n, nil
}

// Move renames and/or moves a node, guarded by the revision the client read.
func (s *Service) Move(ctx context.Context, p access.Principal, node domainfiles.Node, parentRef *int64, newName *string, revision string) (domainfiles.Node, error) {
	sc := access.ScopeFor(p)
	if err := sc.Validate(); err != nil {
		return domainfiles.Node{}, err
	}
	return s.db.MoveNode(ctx, sc, node.ID, parentRef, newName, revision, time.Now())
}

// removeObjects deletes objects whose last blob reference disappeared. Each
// orphan names the policy it belongs to, so the file is removed from its own
// root. Missing files are fine (idempotent); the error is logged and left to
// `rapig gc`.
func (s *Service) removeObjects(orphans []sqlstore.OrphanObject) {
	for _, o := range orphans {
		store, err := s.registry.Get(context.Background(), o.StoragePolicyID)
		if err != nil {
			if s.log != nil {
				s.log.Warn("resolve orphan storage policy failed",
					"policy_id", o.StoragePolicyID, "sha256", o.SHA256, "err", err)
			}
			continue
		}
		if err := store.Remove(o.SHA256); err != nil {
			if s.log != nil {
				s.log.Warn("remove orphan object failed",
					"policy_id", o.StoragePolicyID, "sha256", o.SHA256, "err", err)
			}
		}
	}
}
