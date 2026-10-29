package httpx

import (
	"net/http"
	"strconv"
	"time"

	sharesvc "rapig/internal/app/shares"
	"rapig/internal/domain/access"
	"rapig/internal/domain/share"
	"rapig/internal/platform/apperr"
)

const shareCookieName = "rapig_share"

// shareJSON is the owner-facing share representation.
type shareJSON struct {
	ID            string     `json:"id"`
	Token         string     `json:"token"`
	URL           string     `json:"url"`
	NodeID        int64      `json:"node_id"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	ItemCount     int        `json:"item_count,omitempty"`
	HasPassword   bool       `json:"has_password"`
	ExpiresAt     *time.Time `json:"expires_at"`
	MaxDownloads  int64      `json:"max_downloads"`
	DownloadCount int64      `json:"download_count"`
	VisitsCount   int64      `json:"visits_count"`
	LastAccessAt  *time.Time `json:"last_access_at"`
	CreatedAt     time.Time  `json:"created_at"`
	Active        bool       `json:"active"`
}

func toShareJSON(sh share.Share, name string, now time.Time) shareJSON {
	out := shareJSON{
		ID:            sh.ID,
		Token:         sh.Token,
		URL:           "/s/" + sh.Token,
		NodeID:        sh.NodeID,
		Name:          name,
		Kind:          string(sh.Kind),
		HasPassword:   sh.HasPassword(),
		ExpiresAt:     sh.ExpiresAt,
		MaxDownloads:  sh.MaxDownloads,
		DownloadCount: sh.DownloadCount,
		VisitsCount:   sh.VisitsCount,
		LastAccessAt:  sh.LastAccessAt,
		CreatedAt:     sh.CreatedAt,
		Active:        sh.Active(now),
	}
	if sh.Multi() {
		out.ItemCount = sh.ItemCount()
	}
	return out
}

// handleCreateShare creates a share for one or more of the caller's nodes.
func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req struct {
		NodeID       int64   `json:"node_id"`
		NodeIDs      []int64 `json:"node_ids"`
		Password     string  `json:"password"`
		ExpiresIn    *int64  `json:"expires_in"`
		MaxDownloads int64   `json:"max_downloads"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.NodeID <= 0 && len(req.NodeIDs) == 0 {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
		return
	}
	var name string
	for _, id := range req.NodeIDs {
		node, err := s.files.Node(r.Context(), p, id)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if err := s.authz.Require(p, access.ActionShare, resourceFor(node)); err != nil {
			s.writeError(w, r, err)
			return
		}
		if name == "" {
			name = node.Name
		}
	}
	if req.NodeID > 0 {
		node, err := s.files.Node(r.Context(), p, req.NodeID)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if err := s.authz.Require(p, access.ActionShare, resourceFor(node)); err != nil {
			s.writeError(w, r, err)
			return
		}
		if name == "" {
			name = node.Name
		}
	}
	sh, err := s.shares.Create(r.Context(), p, sharesvc.CreateRequest{
		NodeID:       req.NodeID,
		NodeIDs:      req.NodeIDs,
		Password:     req.Password,
		ExpiresIn:    req.ExpiresIn,
		MaxDownloads: req.MaxDownloads,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusCreated, map[string]any{"share": toShareJSON(sh, name, time.Now())})
}

// handleListShares pages through the caller's own shares.
func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
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
	offset, err := queryInt(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	list, total, err := s.shares.List(r.Context(), p, r.URL.Query().Get("status"), limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	now := time.Now()
	items := make([]shareJSON, 0, len(list))
	for _, sh := range list {
		name := ""
		if n, err := s.files.Node(r.Context(), p, sh.NodeID); err == nil {
			name = n.Name
		}
		items = append(items, toShareJSON(sh, name, now))
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"items": items, "total": total, "limit": limit, "offset": offset,
	})
}

// handleRevokeShare disables one share; revoking twice is not an error.
func (s *Server) handleRevokeShare(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionShare, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
		return
	}
	if r.URL.Query().Get("purge") == "1" {
		if err := s.shares.Purge(r.Context(), p, id); err != nil {
			s.writeError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := s.shares.Revoke(r.Context(), p, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRestoreShare re-enables a revoked share (same token and counters).
func (s *Server) handleRestoreShare(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionShare, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
		return
	}
	sh, err := s.shares.Restore(r.Context(), p, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	name := ""
	if n, err := s.files.Node(r.Context(), p, sh.NodeID); err == nil {
		name = n.Name
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"share": toShareJSON(sh, name, time.Now())})
}

// publicShareJSON is the anonymous view of a share. Locked responses only
// reveal enough to render the password prompt.
type publicShareJSON struct {
	Locked        bool       `json:"locked,omitempty"`
	NodeID        int64      `json:"node_id"`
	Name          string     `json:"name,omitempty"`
	Kind          string     `json:"kind"`
	ItemCount     int        `json:"item_count,omitempty"`
	Size          int64      `json:"size,omitempty"`
	ExpiresAt     *time.Time `json:"expires_at"`
	HasPassword   bool       `json:"has_password"`
	DownloadsLeft *int64     `json:"downloads_left,omitempty"`
}

// shareTicket reads the per-share ticket cookie. The cookie path is scoped to
// the token, so the browser only sends it to that share.
func shareTicket(r *http.Request) string {
	c, err := r.Cookie(shareCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

func (s *Server) setShareTicket(w http.ResponseWriter, r *http.Request, token, ticket string, exp time.Time) {
	secs := int(time.Until(exp).Seconds())
	if secs < 1 {
		secs = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     shareCookieName,
		Value:    ticket,
		Path:     "/api/v1/public/shares/" + token,
		MaxAge:   secs,
		Expires:  exp,
		HttpOnly: true,
		Secure:   r.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

// handlePublicShareMeta returns the public metadata of a share.
func (s *Server) handlePublicShareMeta(w http.ResponseWriter, r *http.Request) {
	token, err := shareToken(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	meta, err := s.shares.Meta(r.Context(), token, shareTicket(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := publicShareJSON{
		Locked:      meta.Locked,
		NodeID:      meta.Share.NodeID,
		Kind:        string(meta.Share.Kind),
		ExpiresAt:   meta.Share.ExpiresAt,
		HasPassword: meta.Share.HasPassword(),
	}
	if meta.Share.Multi() {
		out.ItemCount = meta.ItemCount
	}
	if !meta.Locked {
		out.Name = meta.Node.Name
		if !meta.Share.Multi() {
			out.Size = meta.Node.Size
		}
		if left, limited := meta.Share.DownloadsLeft(); limited {
			out.DownloadsLeft = &left
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"share": out})
}

// handlePublicShareVerify checks the optional password and issues a ticket.
func (s *Server) handlePublicShareVerify(w http.ResponseWriter, r *http.Request) {
	token, err := shareToken(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	res, err := s.shares.Verify(r.Context(), token, req.Password, clientIP(r))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.setShareTicket(w, r, token, res.Ticket, res.ExpiresAt)
	w.WriteHeader(http.StatusNoContent)
}

// handlePublicShareNodes lists one directory level of a shared folder.
func (s *Server) handlePublicShareNodes(w http.ResponseWriter, r *http.Request) {
	token, err := shareToken(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	parent, err := queryInt64(r, "parent", 0)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if parent < 0 {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
		return
	}
	limit, err := queryInt(r, "limit", 50, 1, 200)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	offset, err := queryInt(r, "offset", 0, 0, 1_000_000)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	_, current, items, total, err := s.shares.Nodes(r.Context(), token, shareTicket(r), parent, limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	type item struct {
		ID        int64     `json:"id"`
		Name      string    `json:"name"`
		Kind      string    `json:"kind"`
		Size      int64     `json:"size"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	out := make([]item, 0, len(items))
	for _, n := range items {
		out = append(out, item{ID: n.ID, Name: n.Name, Kind: string(n.Kind), Size: n.Size, UpdatedAt: n.UpdatedAt})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"items": out, "total": total, "limit": limit, "offset": offset,
		"parent": map[string]any{"id": current.ID, "name": current.Name},
	})
}

// handlePublicShareContent streams a shared file. Each successful GET consumes
// one download from the budget; HEAD only returns headers.
func (s *Server) handlePublicShareContent(w http.ResponseWriter, r *http.Request) {
	token, err := shareToken(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
		return
	}
	sh, node, sha, mime, err := s.shares.Content(r.Context(), token, shareTicket(r), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	store, err := s.files.Store(r.Context(), node)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	f, err := store.OpenFile(sha)
	if err != nil {
		s.log.Warn("share download: object missing", "node_id", node.ID, "sha256", sha, "error", err)
		s.writeError(w, r, apperr.ErrNotFound.WithReason(apperr.ReasonMissingAsset))
		return
	}
	defer f.Close()
	if r.Method != http.MethodHead {
		if err := s.shares.Consume(r.Context(), sh); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	if node.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(node.Size, 10))
	}
	ct := mime
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", contentDisposition(node.Name))
	w.Header().Set("ETag", `"`+sha+`"`)
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Set("Accept-Ranges", "bytes")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	http.ServeContent(w, r, node.Name, node.UpdatedAt, f)
}

func shareToken(r *http.Request) (string, error) {
	token := r.PathValue("token")
	if token == "" || len(token) > 128 {
		return "", apperr.ErrNotFound
	}
	return token, nil
}
