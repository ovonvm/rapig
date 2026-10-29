package httpx

import (
	"net/http"
	"strconv"
	"time"

	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	"rapig/internal/platform/apperr"
)

// trashItemJSON is one recycle bin entry. It extends the normal node shape
// with where it came from and when it will be purged.
type trashItemJSON struct {
	nodeJSON
	OriginalParentID int64  `json:"original_parent_id"`
	TrashedAt        string `json:"trashed_at"`
	ExpiresAt        string `json:"expires_at"`
	RemainingDays    int    `json:"remaining_days"`
}

type trashListResponse struct {
	Items  []trashItemJSON `json:"items"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

type updateNodeRequest struct {
	Name     *string `json:"name,omitempty"`
	Parent   *int64  `json:"parent,omitempty"`
	Revision string  `json:"revision"`
}

// nodeIDFromPath parses the {id} path segment.
func nodeIDFromPath(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID)
	}
	return id, nil
}

// loadNode resolves a node inside the caller's scope. Foreign or missing nodes
// surface as 404; the caller runs the policy check itself.
func (s *Server) loadNode(r *http.Request, p access.Principal) (domainfiles.Node, error) {
	id, err := nodeIDFromPath(r)
	if err != nil {
		return domainfiles.Node{}, err
	}
	return s.files.Node(r.Context(), p, id)
}

// handleDeleteNode soft deletes a node into the recycle bin; ?purge=1 deletes
// it permanently after the client confirmed.
func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	n, err := s.loadNode(r, p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionDelete, access.Resource{Kind: access.ResourceNode, ID: n.ID, OwnerID: n.OwnerID, GroupID: n.GroupID}); err != nil {
		s.writeError(w, r, err)
		return
	}
	// 组共享空间没有回收站：组内删除即永久删除（需要 delete 权限），子树、
	// blob 引用计数与各上传者的 used_bytes 一起在事务里清算。个人空间保持
	// 软删除语义，只有显式 ?purge=1 才永久删除。
	if n.GroupID != nil {
		if err := s.files.PurgeGroup(r.Context(), p, n); err != nil {
			s.writeError(w, r, err)
			return
		}
		s.audit(r, p, "node.purge", "node", n.ID, mustJSON(map[string]any{
			"name": n.Name, "group_id": *n.GroupID,
		}))
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Query().Get("purge") == "1" {
		if err := s.files.Purge(r.Context(), p, n); err != nil {
			s.writeError(w, r, err)
			return
		}
		s.audit(r, p, "node.purge", "node", n.ID, "name="+n.Name)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	out, err := s.files.Trash(r.Context(), p, n)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "node.trash", "node", n.ID, "name="+n.Name)
	s.writeJSON(w, http.StatusOK, s.nodeForAPI(r, p, out))
}

// handleUpdateNode renames and/or moves a node. revision guards against lost
// updates: a stale token yields 409 stale_write and the client refreshes.
func (s *Server) handleUpdateNode(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	n, err := s.loadNode(r, p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionWrite, access.Resource{Kind: access.ResourceNode, ID: n.ID, OwnerID: n.OwnerID, GroupID: n.GroupID}); err != nil {
		s.writeError(w, r, err)
		return
	}
	var req updateNodeRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Name == nil && req.Parent == nil {
		s.writeError(w, r, apperr.ErrBadRequest)
		return
	}
	out, err := s.files.Move(r.Context(), p, n, req.Parent, req.Name, req.Revision)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	action := "node.move"
	if req.Parent == nil {
		action = "node.rename"
	}
	s.audit(r, p, action, "node", n.ID, "name="+out.Name)
	s.writeJSON(w, http.StatusOK, s.nodeForAPI(r, p, out))
}

// handleListTrash lists top-level recycle bin entries, newest first.
func (s *Server) handleListTrash(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionRead, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1000000)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	entries, total, err := s.files.TrashList(r.Context(), p, limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	root, rootErr := s.files.Parent(r.Context(), p, 0)
	items := make([]trashItemJSON, 0, len(entries))
	for _, e := range entries {
		n := e.Node
		orig := int64(0)
		if n.TrashedFrom != nil && (rootErr != nil || *n.TrashedFrom != root.ID) {
			orig = *n.TrashedFrom
		}
		item := trashItemJSON{
			nodeJSON:         toNodeJSON(n),
			OriginalParentID: orig,
			RemainingDays:    0,
		}
		if n.TrashedAt != nil {
			item.TrashedAt = n.TrashedAt.UTC().Format(time.RFC3339)
			item.ExpiresAt = e.ExpiresAt.UTC().Format(time.RFC3339)
			if days := int(time.Until(e.ExpiresAt).Hours() / 24); days > 0 {
				item.RemainingDays = days
			}
		}
		items = append(items, item)
	}
	s.writeJSON(w, http.StatusOK, trashListResponse{Items: items, Total: total, Limit: limit, Offset: offset})
}

// handleRestoreNode brings a trashed node back (renaming on conflict).
func (s *Server) handleRestoreNode(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	n, err := s.loadNode(r, p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionWrite, access.Resource{Kind: access.ResourceNode, ID: n.ID, OwnerID: n.OwnerID, GroupID: n.GroupID}); err != nil {
		s.writeError(w, r, err)
		return
	}
	out, err := s.files.Restore(r.Context(), p, n)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "node.restore", "node", n.ID, "name="+out.Name)
	s.writeJSON(w, http.StatusOK, s.nodeForAPI(r, p, out))
}

// handleEmptyTrash purges every top-level trash entry of the caller.
func (s *Server) handleEmptyTrash(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionDelete, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	n, err := s.files.EmptyTrash(r.Context(), p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "trash.empty", "trash", 0, "purged="+strconv.Itoa(n))
	s.writeJSON(w, http.StatusOK, map[string]int{"purged": n})
}

// nodeForAPI renders a node using the API convention that the root of the
// node's own space (personal or group) is reported as parent 0; internal root
// row ids are never exposed.
func (s *Server) nodeForAPI(r *http.Request, p access.Principal, n domainfiles.Node) nodeJSON {
	out := toNodeJSON(n)
	if n.IsRoot() {
		return out
	}
	if root, err := s.files.RootOf(r.Context(), p, n); err == nil && n.ParentID != nil && *n.ParentID == root.ID {
		out.ParentID = 0
	}
	return out
}
