package httpx

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/transfer"
	"rapig/internal/platform/apperr"
)

type transferJSON struct {
	ID            string `json:"id"`
	ParentID      int64  `json:"parent_id"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	SourceURL     string `json:"source_url"`
	Size          int64  `json:"size"`
	ReceivedBytes int64  `json:"received_bytes"`
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
	NodeID        int64  `json:"node_id,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

func toTransferJSON(t transfer.Transfer) transferJSON {
	out := transferJSON{
		ID:            t.ID,
		ParentID:      t.ParentID,
		Name:          t.Name,
		Kind:          string(t.KindValue()),
		SourceURL:     t.SourceURL,
		Size:          t.Size,
		ReceivedBytes: t.ReceivedBytes,
		State:         string(t.State),
		Error:         t.Error,
		CreatedAt:     t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     t.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if t.NodeID != nil {
		out.NodeID = *t.NodeID
	}
	return out
}

type createTransferRequest struct {
	Parent *int64 `json:"parent"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	// Kind selects the backend: "" (http), "http" or "qb".
	Kind string `json:"kind"`
	// Magnet is the magnet link for kind=qb.
	Magnet string `json:"magnet"`
}

type listTransfersResponse struct {
	Items  []transferJSON `json:"items"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// handleCreateTransfer queues an offline HTTP(S) or qBittorrent job.
func (s *Server) handleCreateTransfer(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	var req createTransferRequest
	if err := decodeJSON(w, r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	switch kind {
	case "", string(transfer.KindHTTP), string(transfer.KindQB):
	default:
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidKind))
		return
	}
	parentRef := int64(0)
	if req.Parent != nil {
		parentRef = *req.Parent
	}
	parent, err := s.files.Parent(r.Context(), p, parentRef)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionUpload, resourceFor(parent)); err != nil {
		s.writeError(w, r, err)
		return
	}
	var t transfer.Transfer
	if kind == string(transfer.KindQB) {
		t, err = s.transfers.CreateQB(r.Context(), p, parent, req.Name, req.Magnet)
	} else {
		t, err = s.transfers.Create(r.Context(), p, parent, req.Name, req.URL)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "transfer.create", "transfer", 0, "id="+t.ID+" name="+t.Name)
	s.writeJSON(w, http.StatusCreated, map[string]any{"transfer": toTransferJSON(t)})
}

// torrentBodyLimit is the multipart envelope cap: one byte of file over
// transfer.MaxTorrentSize plus field and part headers must still parse, so
// the per-file limit is what produces most 413s.
const torrentBodyLimit = transfer.MaxTorrentSize + (1 << 20)

// handleCreateTorrentTransfer queues a qBittorrent job from an uploaded
// .torrent file. The body is multipart/form-data with fields `parent` (int,
// 0 = root), `name` (optional) and `file` (the torrent, filename irrelevant).
func (s *Server) handleCreateTorrentTransfer(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, torrentBodyLimit)
	if err := r.ParseMultipartForm(torrentBodyLimit); err != nil {
		if isBodyTooLarge(err) {
			s.writeError(w, r, apperr.ErrPayloadTooLarge.WithReason(apperr.ReasonTorrentTooLarge))
			return
		}
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidTorrent).WithCause(err))
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}
	parentRef := int64(0)
	if raw := strings.TrimSpace(r.FormValue("parent")); raw != "" {
		v, perr := strconv.ParseInt(raw, 10, 64)
		if perr != nil || v < 0 {
			s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
			return
		}
		parentRef = v
	}
	parent, err := s.files.Parent(r.Context(), p, parentRef)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionUpload, resourceFor(parent)); err != nil {
		s.writeError(w, r, err)
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidTorrent).WithCause(err))
		return
	}
	defer func() { _ = file.Close() }()
	// Read one byte past the limit so an oversized file is rejected here even
	// when the multipart envelope itself stayed under torrentBodyLimit.
	data, err := io.ReadAll(io.LimitReader(file, transfer.MaxTorrentSize+1))
	if err != nil {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidTorrent).WithCause(err))
		return
	}
	t, err := s.transfers.CreateTorrentQB(r.Context(), p, parent, r.FormValue("name"), data)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "transfer.create", "transfer", 0, "id="+t.ID+" name="+t.Name+" hash="+t.QBHash)
	s.writeJSON(w, http.StatusCreated, map[string]any{"transfer": toTransferJSON(t)})
}

// isBodyTooLarge reports whether err came from the request size caps, either
// http.MaxBytesReader (whole body) or multipart's per-part limit.
func isBodyTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return true
	}
	return errors.Is(err, multipart.ErrMessageTooLarge) || strings.Contains(err.Error(), "request body too large")
}

// handleQBTransferStatus reports whether qBittorrent offline downloads are
// configured; the UI uses it to enable the magnet form.
func (s *Server) handleQBTransferStatus(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionRead, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]bool{"enabled": s.transfers.QBEnabled()})
}

// handleListTransfers pages the caller's jobs.
func (s *Server) handleListTransfers(w http.ResponseWriter, r *http.Request) {
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
	status := r.URL.Query().Get("status")
	list, total, err := s.transfers.List(r.Context(), p, status, limit, offset)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	items := make([]transferJSON, 0, len(list))
	for _, t := range list {
		items = append(items, toTransferJSON(t))
	}
	s.writeJSON(w, http.StatusOK, listTransfersResponse{Items: items, Total: total, Limit: limit, Offset: offset})
}

// handlePauseTransfer stops a job (running downloads abort in the worker).
func (s *Server) handlePauseTransfer(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionWrite, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.doTransferAction(w, r, p, s.transfers.Pause, "transfer.pause")
}

// handleResumeTransfer re-queues a paused/failed job.
func (s *Server) handleResumeTransfer(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionWrite, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.doTransferAction(w, r, p, s.transfers.Resume, "transfer.resume")
}

// handleDeleteTransfer cancels and removes a job row.
func (s *Server) handleDeleteTransfer(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionWrite, access.SystemResource()); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.doTransferAction(w, r, p, s.transfers.Delete, "transfer.delete")
}

func (s *Server) doTransferAction(w http.ResponseWriter, r *http.Request, p access.Principal, fn func(ctx context.Context, p access.Principal, id string) error, action string) {
	id := r.PathValue("id")
	if id == "" {
		s.writeError(w, r, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID))
		return
	}
	if err := fn(r.Context(), p, id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, action, "transfer", 0, "id="+id)
	w.WriteHeader(http.StatusNoContent)
}
