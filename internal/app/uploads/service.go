// Package uploads implements resumable, content addressed uploads on top of
// the sqlstore repositories and the local object store. It owns the ordering
// of the three side effects (quota reservation, part bytes, node creation) so
// a crash or an error can never leave the counters inconsistent.
package uploads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	filessvc "rapig/internal/app/files"
	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/infra/storage/local"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/idgen"
)

// Service coordinates upload sessions.
type Service struct {
	db       *sqlstore.DB
	registry *storagereg.Registry
	files    *filessvc.Service
	log      *slog.Logger
	sem      chan struct{}
	now      func() time.Time
	partSz   int64
	bufSz    int
}

// New creates the upload service. concurrency bounds simultaneous part writes
// and merges (the low profile uses 2, the high profile 8).
func New(db *sqlstore.DB, registry *storagereg.Registry, files *filessvc.Service, log *slog.Logger, concurrency int, bufferBytes int) *Service {
	if concurrency < 1 {
		concurrency = 1
	}
	if bufferBytes < 8<<10 {
		bufferBytes = 64 << 10
	}
	return &Service{
		db:       db,
		registry: registry,
		files:    files,
		log:      log,
		sem:      make(chan struct{}, concurrency),
		now:      func() time.Time { return time.Now().UTC() },
		partSz:   domainfiles.PartSizeDefault,
		bufSz:    bufferBytes,
	}
}

// InitRequest is the body of POST /uploads.
type InitRequest struct {
	ParentRef int64 // 0 means the user's root
	Name      string
	Size      int64
	SHA256    string // optional, enables instant upload
}

// InitResult is returned for both a new session and an instant upload.
type InitResult struct {
	UploadID      string
	PartSize      int64
	PartsTotal    int64
	ExpiresAt     time.Time
	DedupedNodeID *int64
}

// Init validates the request, reserves quota and creates the session. The
// parent is resolved and authorized by the handler. When the hash already
// exists and its object is on disk the file node is created immediately
// (instant upload), consuming quota instead of reserving it.
func (s *Service) Init(ctx context.Context, p access.Principal, parent domainfiles.Node, req InitRequest) (InitResult, error) {
	if err := domainfiles.ValidateName(req.Name); err != nil {
		return InitResult{}, err
	}
	if req.Size < 0 || req.Size > domainfiles.MaxFileSize {
		return InitResult{}, apperr.ErrPayloadTooLarge.WithReason(apperr.ReasonOutOfRange)
	}
	if req.SHA256 != "" && !validSHA256(req.SHA256) {
		return InitResult{}, apperr.ErrBadRequest
	}
	if parent.Kind != domainfiles.KindDir {
		return InitResult{}, apperr.ErrParentNotFound
	}

	partsTotal := int64(0)
	if req.Size > 0 {
		partsTotal = (req.Size + s.partSz - 1) / s.partSz
	}
	if partsTotal > domainfiles.MaxParts {
		return InitResult{}, apperr.ErrPayloadTooLarge.WithReason(apperr.ReasonOutOfRange)
	}

	sc := access.ScopeFor(p)
	// The session belongs to the storage policy of its parent directory (or the
	// default policy when uploading into the root): the bytes and the node must
	// land in the same root, so the policy is resolved once, here, and stored on
	// the upload row.
	policyID, err := s.files.PolicyFor(ctx, parent)
	if err != nil {
		return InitResult{}, err
	}
	store, err := s.writeStore(ctx, policyID)
	if err != nil {
		return InitResult{}, err
	}
	if req.SHA256 != "" {
		node, hit, err := s.tryInstant(ctx, sc, parent, store, policyID, req)
		if err != nil {
			return InitResult{}, err
		}
		if hit {
			id := node.ID
			return InitResult{
				UploadID:      "",
				PartSize:      s.partSz,
				PartsTotal:    0,
				DedupedNodeID: &id,
			}, nil
		}
	}

	now := s.now()
	uploadID, err := idgen.Token(24)
	if err != nil {
		return InitResult{}, apperr.ErrInternal.WithCause(err)
	}
	up := domainfiles.Upload{
		ID:              uploadID,
		OwnerID:         p.UserID,
		ParentID:        parent.ID,
		StoragePolicyID: policyID,
		Name:            req.Name,
		NameFolded:      domainfiles.FoldName(req.Name),
		Size:            req.Size,
		SHA256:          strings.ToLower(req.SHA256),
		PartSize:        s.partSz,
		PartsTotal:      partsTotal,
		State:           domainfiles.UploadOpen,
		CreatedAt:       now,
		ExpiresAt:       now.Add(domainfiles.UploadTTL),
	}
	if err := s.db.ReserveBytes(ctx, p.UserID, req.Size); err != nil {
		return InitResult{}, err
	}
	// The node path is derived from the parent path; the upload row stores the
	// parent id only, and completion recomputes the path under the same parent.
	if err := s.db.CreateUpload(ctx, sc, up); err != nil {
		_ = s.db.ReleaseBytes(ctx, p.UserID, req.Size)
		return InitResult{}, err
	}
	return InitResult{
		UploadID:   up.ID,
		PartSize:   up.PartSize,
		PartsTotal: up.PartsTotal,
		ExpiresAt:  up.ExpiresAt,
	}, nil
}

// tryInstant creates a node for an already stored object inside the same
// policy. hit=false means the caller should fall back to a normal upload
// session (dedup never crosses storage roots: the bytes must exist in this
// policy's root).
func (s *Service) tryInstant(ctx context.Context, sc access.Scope, parent domainfiles.Node, store *local.Store, policyID int64, req InitRequest) (domainfiles.Node, bool, error) {
	blob, err := s.db.BlobBySHAInPolicy(ctx, policyID, req.SHA256)
	if err != nil {
		if apperr.CodeOf(err) == apperr.CodeNotFound {
			return domainfiles.Node{}, false, nil
		}
		return domainfiles.Node{}, false, err
	}
	path, err := store.Path(blob.SHA256)
	if err != nil {
		return domainfiles.Node{}, false, nil
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() != req.Size {
		// Blob row exists but the object is gone; treat as a normal upload.
		return domainfiles.Node{}, false, nil
	}
	node, err := s.db.CommitCompletedUpload(ctx, sc, sqlstore.CompletedUploadInput{
		ParentID:        parent.ID,
		StoragePolicyID: policyID,
		Name:            req.Name,
		Path:            domainfiles.ChildPath(parent.Path, req.Name),
		Mime:            mimeFor(req.Name),
		SHA256:          blob.SHA256,
		Size:            req.Size,
		BlobID:          blob.ID,
		IncrementRef:    true,
		CreatedAt:       s.now(),
	})
	if err != nil {
		return domainfiles.Node{}, false, err
	}
	s.audit(ctx, sc, "upload.instant", node.ID, map[string]any{
		"owner_id": sc.UserID, "name": node.Name, "size": node.Size,
	})
	return node, true, nil
}

// PutPart streams one part into the temp area and records its metadata. A
// repeated part overwrites the previous bytes (retry after a dropped
// connection).
func (s *Service) PutPart(ctx context.Context, p access.Principal, uploadID string, partNo int64, body io.Reader) error {
	if err := s.acquire(ctx); err != nil {
		return err
	}
	defer s.release()

	sc := access.ScopeFor(p)
	up, err := s.db.UploadByID(ctx, sc, uploadID)
	if err != nil {
		return err
	}
	if !up.IsOpen() {
		return apperr.ErrInvalidState
	}
	if s.now().After(up.ExpiresAt) {
		return apperr.ErrInvalidState.WithReason(apperr.ReasonUploadExpired)
	}
	expected, ok := up.ExpectedPartSize(partNo)
	if !ok {
		return apperr.ErrBadRequest.WithReason(apperr.ReasonOutOfRange)
	}
	store, err := s.writeStore(ctx, up.StoragePolicyID)
	if err != nil {
		return err
	}
	offset := partNo * up.PartSize

	tmpPath := s.partPath(store, up.ID, partNo)
	sum, written, err := s.writeTemp(ctx, tmpPath, body, expected)
	if err != nil {
		return err
	}
	if written != expected {
		_ = os.Remove(tmpPath)
		return apperr.ErrBadRequest.WithReason(apperr.ReasonOutOfRange)
	}
	if err := s.db.UpsertPart(ctx, sc, up.ID, partNo, offset, expected, sum); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

// Complete merges all parts, verifies the optional whole file hash, stores the
// object and creates the file node. partHashes is client supplied and checked
// against the recorded part hashes when present.
func (s *Service) Complete(ctx context.Context, p access.Principal, uploadID string, partHashes map[int64]string) (domainfiles.Node, error) {
	if err := s.acquire(ctx); err != nil {
		return domainfiles.Node{}, err
	}
	defer s.release()

	sc := access.ScopeFor(p)
	up, err := s.db.UploadByID(ctx, sc, uploadID)
	if err != nil {
		return domainfiles.Node{}, err
	}
	if up.State == domainfiles.UploadCompleted {
		if up.NodeID != nil {
			return s.db.NodeByID(ctx, sc, *up.NodeID)
		}
		return domainfiles.Node{}, apperr.ErrInvalidState
	}
	if up.State != domainfiles.UploadOpen {
		return domainfiles.Node{}, apperr.ErrInvalidState
	}
	if s.now().After(up.ExpiresAt) {
		return domainfiles.Node{}, apperr.ErrInvalidState.WithReason(apperr.ReasonUploadExpired)
	}

	parts, err := s.db.UploadParts(ctx, sc, up.ID)
	if err != nil {
		return domainfiles.Node{}, err
	}
	if int64(len(parts)) != up.PartsTotal {
		return domainfiles.Node{}, apperr.ErrBadRequest.WithReason(apperr.ReasonPartsIncomplete)
	}
	for _, part := range parts {
		expected, ok := up.ExpectedPartSize(part.PartNo)
		if !ok || part.Size != expected || part.Offset != part.PartNo*up.PartSize {
			return domainfiles.Node{}, apperr.ErrBadRequest.WithReason(apperr.ReasonPartsIncomplete)
		}
		if got, want := strings.ToLower(partHashes[part.PartNo]), strings.ToLower(part.SHA256); want != "" && got != "" && got != want {
			return domainfiles.Node{}, apperr.ErrChecksumMismatch.WithReason(apperr.ReasonChecksumMismatch)
		}
	}

	parent, err := s.files.Parent(ctx, p, up.ParentID)
	if err != nil {
		return domainfiles.Node{}, err
	}
	store, err := s.writeStore(ctx, up.StoragePolicyID)
	if err != nil {
		return domainfiles.Node{}, err
	}
	mergePath := filepath.Join(store.TempDir(), "merge-"+up.ID)
	sum, size, err := s.mergeParts(ctx, store, up, parts, mergePath)
	if err != nil {
		return domainfiles.Node{}, err
	}
	if up.SHA256 != "" && sum != up.SHA256 {
		_ = os.Remove(mergePath)
		return domainfiles.Node{}, apperr.ErrChecksumMismatch.WithReason(apperr.ReasonChecksumMismatch)
	}
	if size != up.Size {
		_ = os.Remove(mergePath)
		return domainfiles.Node{}, apperr.ErrBadRequest.WithReason(apperr.ReasonOutOfRange)
	}

	blob, created, err := s.db.InsertBlobInPolicy(ctx, up.StoragePolicyID, sum, size)
	if err != nil {
		_ = os.Remove(mergePath)
		return domainfiles.Node{}, err
	}
	if created {
		if err := store.PublishPath(mergePath, sum); err != nil {
			_ = os.Remove(mergePath)
			_, _ = s.db.DecRef(ctx, blob.ID)
			return domainfiles.Node{}, apperr.ErrInternal.WithCause(err)
		}
	} else {
		_ = os.Remove(mergePath)
	}

	node, err := s.db.CommitCompletedUpload(ctx, sc, sqlstore.CompletedUploadInput{
		UploadID:        up.ID,
		ParentID:        parent.ID,
		StoragePolicyID: up.StoragePolicyID,
		Name:            up.Name,
		Path:            domainfiles.ChildPath(parent.Path, up.Name),
		Mime:            mimeFor(up.Name),
		SHA256:          sum,
		Size:            up.Size,
		BlobID:          blob.ID,
		IncrementRef:    !created,
		CreatedAt:       s.now(),
	})
	if err != nil {
		// The node was not created: drop the reference and, when this upload
		// created the object and nothing else points at it, the bytes too.
		if remaining, derr := s.db.DecRef(ctx, blob.ID); derr == nil && remaining == 0 {
			_ = store.Remove(sum)
		}
		return domainfiles.Node{}, err
	}
	for _, part := range parts {
		_ = os.Remove(s.partPath(store, up.ID, part.PartNo))
	}
	s.audit(ctx, sc, "upload.complete", node.ID, map[string]any{
		"owner_id": sc.UserID, "name": node.Name, "size": node.Size, "mime": mimeFor(up.Name),
	})
	return node, nil
}

// Cancel aborts an open upload and releases its reservation. Canceling a
// canceled upload is a no-op; a completed upload is a conflict.
func (s *Service) Cancel(ctx context.Context, p access.Principal, uploadID string) error {
	sc := access.ScopeFor(p)
	up, err := s.db.UploadByID(ctx, sc, uploadID)
	if err != nil {
		return err
	}
	switch up.State {
	case domainfiles.UploadCanceled:
		return nil
	case domainfiles.UploadCompleted:
		return apperr.ErrInvalidState
	}
	if err := s.db.MarkUploadCanceled(ctx, sc, up.ID); err != nil {
		return err
	}
	if err := s.db.ReleaseBytes(ctx, sc.UserID, up.Size); err != nil {
		return err
	}
	s.removePartsFor(ctx, up)
	s.audit(ctx, sc, "upload.cancel", 0, map[string]any{"owner_id": sc.UserID, "name": up.Name})
	return nil
}

// CleanupExpired cancels every open session past its TTL and releases quota.
// The returned count is logged by the maintenance loop.
func (s *Service) CleanupExpired(ctx context.Context, limit int) (int, error) {
	expired, err := s.db.ExpiredUploads(ctx, s.now(), limit)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, up := range expired {
		if err := s.db.MarkUploadCanceled(ctx, access.UserScope(up.OwnerID, false), up.ID); err != nil {
			s.log.Warn("upload cleanup: mark canceled", "upload_id", up.ID, "error", err)
			continue
		}
		if err := s.db.ReleaseBytes(ctx, up.OwnerID, up.Size); err != nil {
			s.log.Warn("upload cleanup: release quota", "upload_id", up.ID, "error", err)
			continue
		}
		s.removePartsFor(ctx, up)
		if err := s.db.DeleteExpiredUpload(ctx, up.ID); err != nil {
			s.log.Warn("upload cleanup: delete row", "upload_id", up.ID, "error", err)
			continue
		}
		done++
	}
	return done, nil
}

// mergeParts concatenates the part files in order and hashes the result.
func (s *Service) mergeParts(ctx context.Context, store *local.Store, up domainfiles.Upload, parts []domainfiles.UploadPart, dst string) (string, int64, error) {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, apperr.ErrInternal.WithCause(fmt.Errorf("merge: create %s: %w", dst, err))
	}
	defer out.Close()
	h := sha256.New()
	buf := make([]byte, s.bufSz)
	var total int64
	for _, part := range parts {
		in, err := os.Open(s.partPath(store, up.ID, part.PartNo))
		if err != nil {
			return "", 0, apperr.ErrBadRequest.WithReason(apperr.ReasonPartsIncomplete)
		}
		n, err := io.CopyBuffer(io.MultiWriter(out, h), in, buf)
		in.Close()
		if err != nil {
			return "", 0, apperr.ErrInternal.WithCause(fmt.Errorf("merge: copy part %d: %w", part.PartNo, err))
		}
		if n != part.Size {
			return "", 0, apperr.ErrBadRequest.WithReason(apperr.ReasonPartsIncomplete)
		}
		total += n
	}
	if err := out.Sync(); err != nil {
		return "", 0, apperr.ErrInternal.WithCause(fmt.Errorf("merge: sync: %w", err))
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), total, nil
}

// writeTemp copies body into path, hashing the bytes, and rejects oversized
// bodies early.
func (s *Service) writeTemp(ctx context.Context, path string, body io.Reader, limit int64) (string, int64, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, apperr.ErrInternal.WithCause(fmt.Errorf("upload: create temp: %w", err))
	}
	h := sha256.New()
	buf := make([]byte, s.bufSz)
	n, err := io.CopyBuffer(io.MultiWriter(f, h), io.LimitReader(body, limit+1), buf)
	if err != nil {
		f.Close()
		_ = os.Remove(path)
		return "", 0, apperr.ErrInternal.WithCause(fmt.Errorf("upload: write part: %w", err))
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return "", 0, apperr.ErrInternal.WithCause(fmt.Errorf("upload: sync part: %w", err))
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", 0, apperr.ErrInternal.WithCause(fmt.Errorf("upload: close part: %w", err))
	}
	if err := ctx.Err(); err != nil {
		_ = os.Remove(path)
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (s *Service) partPath(store *local.Store, uploadID string, partNo int64) string {
	return filepath.Join(store.TempDir(), "up-"+uploadID+"-"+strconv.FormatInt(partNo, 10))
}

func (s *Service) removeParts(store *local.Store, uploadID string) {
	matches, _ := filepath.Glob(filepath.Join(store.TempDir(), "up-"+uploadID+"-*"))
	for _, path := range matches {
		_ = os.Remove(path)
	}
}

// removePartsFor drops the part files of an upload using the store of its own
// policy; an unresolvable policy leaves stale temp files to the startup
// CleanTemp sweep instead of failing the cancellation.
func (s *Service) removePartsFor(ctx context.Context, up domainfiles.Upload) {
	store, err := s.registry.Get(ctx, up.StoragePolicyID)
	if err != nil {
		if s.log != nil {
			s.log.Warn("upload cleanup: resolve storage policy", "upload_id", up.ID,
				"policy_id", up.StoragePolicyID, "error", err)
		}
		return
	}
	s.removeParts(store, up.ID)
}

// writeStore returns the store of an upload policy and refuses disabled or
// missing policies with 507 insufficient_storage: parts and the final object
// must be written into a root that accepts new content.
func (s *Service) writeStore(ctx context.Context, policyID int64) (*local.Store, error) {
	store, err := s.registry.Writable(ctx, policyID)
	if err != nil {
		switch {
		case errors.Is(err, storagereg.ErrPolicyDisabled):
			return nil, apperr.ErrInsufficientStorage.WithReason(apperr.ReasonStoragePolicyDisabled).WithCause(err)
		case errors.Is(err, storagereg.ErrPolicyNotFound):
			return nil, apperr.ErrInsufficientStorage.WithCause(err)
		default:
			if s.log != nil {
				s.log.Error("upload storage policy unavailable", "policy_id", policyID, "err", err)
			}
			return nil, apperr.ErrInternal.WithCause(err)
		}
	}
	return store, nil
}

func (s *Service) acquire(ctx context.Context) error {
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) release() { <-s.sem }

func (s *Service) audit(ctx context.Context, sc access.Scope, action string, targetID int64, detail map[string]any) {
	_ = s.db.InsertAudit(ctx, sqlstore.AuditEvent{
		At:         s.now(),
		ActorID:    sc.UserID,
		Action:     action,
		TargetType: "node",
		TargetID:   targetID,
		Result:     "allow",
		Detail:     encodeDetail(detail),
	})
}

func encodeDetail(detail map[string]any) string {
	if len(detail) == 0 {
		return ""
	}
	var b strings.Builder
	first := true
	b.WriteByte('{')
	for k, v := range detail {
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteString(strconv.Quote(k))
		b.WriteByte(':')
		switch val := v.(type) {
		case string:
			b.WriteString(strconv.Quote(val))
		case int64:
			b.WriteString(strconv.FormatInt(val, 10))
		case int:
			b.WriteString(strconv.Itoa(val))
		default:
			b.WriteString("null")
		}
	}
	b.WriteByte('}')
	return b.String()
}

// mimeFor guesses a Content-Type from the file name. It never returns a value
// with parameters (charset) so the download header stays simple.
func mimeFor(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return "application/octet-stream"
	}
	t := mime.TypeByExtension(ext)
	if i := strings.IndexByte(t, ';'); i >= 0 {
		t = t[:i]
	}
	t = strings.TrimSpace(t)
	if t == "" || strings.ContainsAny(t, "\r\n") {
		return "application/octet-stream"
	}
	return t
}

func validSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
