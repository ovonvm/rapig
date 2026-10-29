package httpx

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	appuploads "rapig/internal/app/uploads"
	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	"rapig/internal/platform/apperr"
)

type initUploadRequest struct {
	Parent int64  `json:"parent"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type initUploadResponse struct {
	UploadID      string    `json:"upload_id"`
	PartSize      int64     `json:"part_size"`
	PartsTotal    int64     `json:"parts_total"`
	ExpiresAt     time.Time `json:"expires_at"`
	DedupedNodeID *int64    `json:"deduped_node_id,omitempty"`
}

// handleInitUpload starts a resumable upload, or completes it instantly when
// the client supplied a sha256 that is already stored (and charges quota).
func (s *Server) handleInitUpload(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req initUploadRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	parent, err := s.files.Parent(r.Context(), p, req.Parent)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionUpload, resourceFor(parent)); err != nil {
		s.writeError(w, r, err)
		return
	}
	res, err := s.uploads.Init(r.Context(), p, parent, appuploads.InitRequest{
		ParentRef: parent.ID,
		Name:      req.Name,
		Size:      req.Size,
		SHA256:    req.SHA256,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	out := initUploadResponse{
		UploadID:   res.UploadID,
		PartSize:   res.PartSize,
		PartsTotal: res.PartsTotal,
		ExpiresAt:  res.ExpiresAt,
	}
	if res.DedupedNodeID != nil {
		out.DedupedNodeID = res.DedupedNodeID
		s.audit(r, p, "upload.instant", "node", *res.DedupedNodeID,
			mustJSON(map[string]any{"name": req.Name, "size": req.Size}))
	}
	s.writeJSON(w, http.StatusCreated, out)
}

// handlePutPart streams one part. The body is raw bytes; the part number comes
// from the path, so a retry simply overwrites the same part.
func (s *Server) handlePutPart(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionUpload, access.Resource{Kind: access.ResourceSystem}); err != nil {
		s.writeError(w, r, err)
		return
	}
	uploadID := r.PathValue("id")
	partNo, err := strconv.ParseInt(r.PathValue("part"), 10, 64)
	if err != nil || partNo < 0 {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonOutOfRange))
		return
	}
	// Clients may send Content-Range (bytes start-end/total); validate it when
	// present so a confused client fails loudly instead of writing to the
	// wrong offset.
	if cr := r.Header.Get("Content-Range"); cr != "" {
		if _, _, _, perr := parseContentRange(cr); perr != nil {
			s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonOutOfRange))
			return
		}
	}
	body := http.MaxBytesReader(w, r.Body, domainfiles.MaxPartSize+1)
	if err := s.uploads.PutPart(r.Context(), p, uploadID, partNo, body); err != nil {
		s.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCompleteUpload merges the parts and creates the file node.
func (s *Server) handleCompleteUpload(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionUpload, access.Resource{Kind: access.ResourceSystem}); err != nil {
		s.writeError(w, r, err)
		return
	}
	var req struct {
		Parts []struct {
			PartNo int64  `json:"part_no"`
			SHA256 string `json:"sha256"`
		} `json:"parts"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	hashes := make(map[int64]string, len(req.Parts))
	for _, part := range req.Parts {
		if part.PartNo >= 0 && part.SHA256 != "" {
			hashes[part.PartNo] = part.SHA256
		}
	}
	node, err := s.uploads.Complete(r.Context(), p, r.PathValue("id"), hashes)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	parent, err := s.files.Parent(r.Context(), p, parentRefOf(node))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "upload.complete", "node", node.ID,
		mustJSON(map[string]any{"name": node.Name, "size": node.Size}))
	s.writeJSON(w, http.StatusOK, nodeInParent(node, parent))
}

// handleCancelUpload aborts an open upload and releases its quota.
func (s *Server) handleCancelUpload(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionUpload, access.Resource{Kind: access.ResourceSystem}); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.uploads.Cancel(r.Context(), p, r.PathValue("id")); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "upload.cancel", "upload", 0, "")
	w.WriteHeader(http.StatusNoContent)
}

// handleDownload streams a file with Range and ETag support. The node is
// resolved inside the caller's scope first, so another user's file is a plain
// 404 (no existence leak). An admin holding a valid breakglass grant may pass
// X-Breakglass: <grant id> to read exactly one foreign file, which is audited.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
		return
	}

	node, sha, nodeMime, store, err := s.files.Content(r.Context(), p, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionRead, resourceFor(node)); err != nil {
		s.writeError(w, r, err)
		return
	}
	f, err := store.OpenFile(sha)
	if err != nil {
		s.log.Warn("download: object missing", "node_id", node.ID, "sha256", sha, "error", err)
		s.writeError(w, r, apperr.ErrNotFound.WithReason(apperr.ReasonMissingAsset))
		return
	}
	defer f.Close()
	if node.Size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(node.Size, 10))
	}
	ct := nodeMime
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
	// ServeContent handles Range/If-None-Match; the ETag set above is what it
	// compares against.
	http.ServeContent(w, r, node.Name, node.UpdatedAt, f)
}

// parentRefOf returns the API parent reference of a node: 0 when its parent is
// the root row (or unknown).
func parentRefOf(n domainfiles.Node) int64 {
	if n.ParentID == nil {
		return 0
	}
	return *n.ParentID
}

// resourceFor converts a node into the Authorizer resource shape. GroupID is
// carried so a node inside a group space is checked against the caller's group
// role instead of node ownership.
func resourceFor(n domainfiles.Node) access.Resource {
	return access.Resource{Kind: access.ResourceNode, ID: n.ID, OwnerID: n.OwnerID, GroupID: n.GroupID}
}

func contentDisposition(name string) string {
	v := mime.FormatMediaType("attachment", map[string]string{"filename": name})
	if v == "" {
		return "attachment"
	}
	return v
}

// parseContentRange parses "bytes start-end/total".
func parseContentRange(v string) (start, end, total int64, err error) {
	v = strings.TrimSpace(v)
	if !strings.HasPrefix(v, "bytes ") {
		return 0, 0, 0, errors.New("content-range: missing bytes unit")
	}
	v = strings.TrimPrefix(v, "bytes ")
	rangePart, totalPart, ok := strings.Cut(v, "/")
	if !ok {
		return 0, 0, 0, errors.New("content-range: missing total")
	}
	s, e, ok := strings.Cut(rangePart, "-")
	if !ok {
		return 0, 0, 0, errors.New("content-range: missing range")
	}
	start, err = strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("content-range: start: %w", err)
	}
	end, err = strconv.ParseInt(strings.TrimSpace(e), 10, 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("content-range: end: %w", err)
	}
	if totalPart != "*" {
		total, err = strconv.ParseInt(strings.TrimSpace(totalPart), 10, 64)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("content-range: total: %w", err)
		}
	}
	if start < 0 || end < start {
		return 0, 0, 0, errors.New("content-range: bad bounds")
	}
	return start, end, total, nil
}
