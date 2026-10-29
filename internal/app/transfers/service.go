// Package transfers implements offline downloads: HTTP(S) sources (M4.2) and
// qBittorrent magnets (M6.1).
package transfers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	"rapig/internal/domain/transfer"
	"rapig/internal/infra/fetch"
	"rapig/internal/infra/qb"
	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/infra/storage/local"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/idgen"
)

// copyBuffer is the streaming chunk size (64 KiB, matching the upload service).
const copyBuffer = 64 << 10

// Service owns transfer rows and the download worker.
type Service struct {
	db       *sqlstore.DB
	registry *storagereg.Registry
	fetch    *fetch.Client
	log      *slog.Logger
	now      func() time.Time

	sem    chan struct{}
	mu     sync.Mutex
	wg     sync.WaitGroup
	ctxMap map[string]context.CancelFunc

	// qb is nil when qBittorrent offline downloads are disabled. dataDir
	// roots the per-task qBittorrent save paths (<dataDir>/tmp/qb/...).
	qb       *qb.Client
	dataDir  string
	qbMu     sync.Mutex
	qbActive map[string]struct{}
}

// Options are optional integrations. The zero value keeps HTTP-only behavior.
type Options struct {
	// QB is the qBittorrent client; nil disables qb jobs.
	QB *qb.Client
	// DataDir is the Rapig data directory; qb jobs download below
	// <DataDir>/tmp/qb. Required when QB is set.
	DataDir string
}

// New builds the service. concurrency is the number of simultaneous HTTP
// downloads. Options may be omitted entirely (HTTP-only service).
func New(db *sqlstore.DB, registry *storagereg.Registry, fetcher *fetch.Client, log *slog.Logger, concurrency int, opts ...Options) *Service {
	if concurrency < 1 {
		concurrency = 1
	}
	var o Options
	for _, opt := range opts {
		o = opt
	}
	return &Service{
		db: db, registry: registry, fetch: fetcher, log: log,
		now:      func() time.Time { return time.Now().UTC() },
		sem:      make(chan struct{}, concurrency),
		ctxMap:   make(map[string]context.CancelFunc),
		qb:       o.QB,
		dataDir:  o.DataDir,
		qbActive: make(map[string]struct{}),
	}
}

// Create validates and queues a job. The parent must already be authorized and
// loaded by the caller.
func (s *Service) Create(ctx context.Context, p access.Principal, parent domainfiles.Node, name, rawURL string) (transfer.Transfer, error) {
	u, err := fetch.ValidateURL(rawURL)
	if err != nil {
		return transfer.Transfer{}, apperr.ErrBadRequest.WithReason("invalid_url").WithCause(err)
	}
	if err := s.fetch.CheckURL(ctx, u.String()); err != nil {
		return transfer.Transfer{}, apperr.ErrBadRequest.WithReason("blocked_url").WithCause(err)
	}
	if !parent.IsRoot() && parent.Kind != domainfiles.KindDir {
		return transfer.Transfer{}, apperr.ErrParentNotFound
	}
	if name = strings.TrimSpace(name); name == "" {
		name = deriveName(u.Path)
	}
	if err := domainfiles.ValidateName(name); err != nil {
		if name == deriveName(u.Path) {
			name = "download"
		} else {
			return transfer.Transfer{}, apperr.ErrInvalidName.WithCause(err)
		}
	}
	now := s.now()
	// The job downloads into the storage policy of its target directory: the
	// policy is resolved now (so a later admin change cannot move an in-flight
	// job) and recorded on the row. A disabled policy refuses the job up front;
	// a policy disabled later fails the running job instead.
	policyID, err := s.policyFor(ctx, parent)
	if err != nil {
		return transfer.Transfer{}, err
	}
	if _, err := s.writeStore(ctx, policyID); err != nil {
		return transfer.Transfer{}, err
	}
	t := transfer.Transfer{
		ID:              idgen.MustToken(transferIDBytes),
		OwnerID:         p.UserID,
		ParentID:        parent.ID,
		StoragePolicyID: policyID,
		Name:            name,
		NameFolded:      domainfiles.FoldName(name),
		SourceURL:       u.String(),
		State:           transfer.StatePending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	out, err := s.db.CreateTransfer(ctx, access.ScopeFor(p), t)
	if err != nil {
		return transfer.Transfer{}, err
	}
	s.log.Info("transfer.create", "transfer_id", out.ID, "name", out.Name, "url", out.SourceURL)
	return out, nil
}

const transferIDBytes = 12

// List pages the caller's jobs filtered by an optional exact state.
func (s *Service) List(ctx context.Context, p access.Principal, state string, limit, offset int) ([]transfer.Transfer, int, error) {
	if state != "" && !transfer.ValidState(state) {
		return nil, 0, apperr.ErrBadRequest.WithReason("invalid_state")
	}
	return s.db.ListTransfers(ctx, access.ScopeFor(p), state, limit, offset)
}

// Pause stops a job. Running downloads are aborted by the worker; the row keeps
// its downloaded progress so the UI can show it. qb jobs also pause the
// qBittorrent task (best effort).
func (s *Service) Pause(ctx context.Context, p access.Principal, id string) error {
	sc := access.ScopeFor(p)
	t, err := s.db.TransferByID(ctx, sc, id)
	if err != nil {
		return err
	}
	ok, err := s.db.PauseTransfer(ctx, sc, id, s.now())
	if err != nil {
		return err
	}
	if !ok {
		return apperr.ErrInvalidState
	}
	s.cancel(id)
	if t.KindValue() == transfer.KindQB {
		s.pauseQBTorrent(ctx, t)
	}
	return nil
}

// Resume re-queues a paused/failed job from zero. qb jobs also resume the
// qBittorrent task; a torrent that vanished is re-added by the poller.
func (s *Service) Resume(ctx context.Context, p access.Principal, id string) error {
	sc := access.ScopeFor(p)
	t, err := s.db.TransferByID(ctx, sc, id)
	if err != nil {
		return err
	}
	ok, err := s.db.ResumeTransfer(ctx, sc, id, s.now())
	if err != nil {
		return err
	}
	if !ok {
		return apperr.ErrInvalidState
	}
	if t.KindValue() == transfer.KindQB {
		s.resumeQBTorrent(ctx, t)
	}
	return nil
}

// Delete cancels an active job and removes the row. qb jobs also delete the
// qBittorrent task (with files) and the temporary download directory.
func (s *Service) Delete(ctx context.Context, p access.Principal, id string) error {
	sc := access.ScopeFor(p)
	t, err := s.db.TransferByID(ctx, sc, id)
	if err != nil {
		return err
	}
	s.cancel(id)
	if t.KindValue() == transfer.KindQB {
		s.deleteQBTorrent(ctx, t)
	}
	if _, err := s.db.CancelTransfer(ctx, sc, id, s.now()); err != nil {
		return err
	}
	_, err = s.db.DeleteTransfer(ctx, sc, id)
	return err
}

func (s *Service) cancel(id string) {
	s.mu.Lock()
	c := s.ctxMap[id]
	s.mu.Unlock()
	if c != nil {
		c()
	}
}

// RunPending claims up to the free worker slots and starts downloads. It
// returns the number of jobs started.
func (s *Service) RunPending(ctx context.Context) int {
	avail := cap(s.sem) - len(s.sem)
	if avail <= 0 {
		return 0
	}
	list, err := s.db.PendingTransfers(ctx, avail*2)
	if err != nil {
		s.log.Warn("transfer.pending", "err", err)
		return 0
	}
	started := 0
	for _, t := range list {
		if started >= avail {
			break
		}
		ok, err := s.db.ClaimTransfer(ctx, t.ID, s.now())
		if err != nil {
			s.log.Warn("transfer.claim", "transfer_id", t.ID, "err", err)
			continue
		}
		if !ok {
			continue
		}
		started++
		s.wg.Add(1)
		go s.run(ctx, t.ID)
	}
	return started
}

// Wait blocks until in-flight downloads finish (tests/shutdown).
func (s *Service) Wait() { s.wg.Wait() }

// Cleanup removes old terminal rows.
func (s *Service) Cleanup(ctx context.Context) (int64, error) {
	return s.db.CleanupTransfers(ctx, s.now().Add(-transfer.Retention))
}

func (s *Service) run(parent context.Context, id string) {
	defer s.wg.Done()
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	ctx, cancel := context.WithCancel(parent)
	s.mu.Lock()
	s.ctxMap[id] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.ctxMap, id)
		s.mu.Unlock()
		cancel()
	}()

	t, err := s.db.TransferByIDAny(ctx, id)
	if err != nil {
		return
	}
	if st, err := s.db.TransferState(ctx, id); err != nil || st != transfer.StateRunning {
		return
	}
	startedAt := s.now()
	if err := s.download(ctx, t, startedAt); err != nil {
		state := transfer.StateFailed
		// User paused/canceled while we were downloading: keep their state.
		if st, e := s.db.TransferState(context.Background(), id); e == nil && (st == transfer.StatePaused || st == transfer.StateCanceled) {
			state = st
		}
		msg := errMessage(err)
		if err := s.db.FinishTransfer(context.Background(), id, state, msg, nil, 0, t.ReceivedBytes, s.now()); err != nil {
			s.log.Error("transfer.finish", "transfer_id", id, "err", err)
		}
		if state == transfer.StateFailed {
			s.log.Warn("transfer.failed", "transfer_id", id, "err", msg)
		}
	}
}

func (s *Service) download(ctx context.Context, t transfer.Transfer, startedAt time.Time) error {
	policyID := t.StoragePolicyID
	if policyID == 0 {
		parent, err := s.db.NodeByID(ctx, access.UserScope(t.OwnerID, false), t.ParentID)
		if err != nil {
			return err
		}
		if policyID, err = s.policyFor(ctx, parent); err != nil {
			return err
		}
	}
	store, err := s.registry.Writable(ctx, policyID)
	if err != nil {
		switch {
		case errors.Is(err, storagereg.ErrPolicyDisabled):
			return errPolicyDisabled
		case errors.Is(err, storagereg.ErrPolicyNotFound):
			return errPolicyMissing
		default:
			return err
		}
	}
	resp, err := s.fetch.Open(ctx, t.SourceURL)
	if err != nil {
		return classify(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("source responded %d", resp.StatusCode)
	}
	declared := resp.ContentLength
	if declared > transfer.MaxSize {
		return errTooLarge
	}

	tmp, err := os.CreateTemp(store.TempDir(), "dl-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(tmpPath)
		}
	}()

	h := sha256.New()
	cw := &progressWriter{
		w:   io.MultiWriter(tmp, h),
		svc: s,
		id:  t.ID,
		max: transfer.MaxSize,
	}
	buf := make([]byte, copyBuffer)
	written, err := io.CopyBuffer(cw, resp.Body, buf)
	if err != nil {
		_ = tmp.Close()
		return classify(err)
	}
	if cw.tooLarge {
		_ = tmp.Close()
		return errTooLarge
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	sha := hex.EncodeToString(h.Sum(nil))
	if err := store.PublishPath(tmpPath, sha); err != nil {
		return err
	}
	keep = true

	blob, created, err := s.db.InsertBlobInPolicy(ctx, policyID, sha, written)
	if err != nil {
		return err
	}

	sc := access.UserScope(t.OwnerID, false)
	parent, err := s.db.NodeByID(ctx, sc, t.ParentID)
	if err != nil {
		return err
	}
	name, err := s.db.FreeName(ctx, sc, t.ParentID, t.Name, 0)
	if err != nil {
		return err
	}
	node, err := s.db.CommitCompletedUpload(ctx, sc, sqlstore.CompletedUploadInput{
		ParentID:        t.ParentID,
		StoragePolicyID: policyID,
		Name:            name,
		Path:            domainfiles.ChildPath(parent.Path, name),
		Mime:            mimeFor(name),
		SHA256:          sha,
		Size:            written,
		BlobID:          blob.ID,
		IncrementRef:    !created,
		CreatedAt:       s.now(),
	})
	if err != nil {
		// Compensate the blob so a failed commit leaves no orphan refcount.
		if remaining, derr := s.db.DecRef(ctx, blob.ID); derr == nil && remaining == 0 && created {
			_ = store.Remove(sha)
		}
		return err
	}
	size := written
	nodeID := node.ID
	if err := s.db.FinishTransfer(ctx, t.ID, transfer.StateCompleted, "", &nodeID, size, written, s.now()); err != nil {
		return err
	}
	s.log.Info("transfer.completed", "transfer_id", t.ID, "node_id", node.ID, "size", written, "started_at", startedAt)
	return nil
}

var errTooLarge = errors.New("source exceeds the 10 GB limit")

// Policy failures are reported verbatim on the job row, which is what the UI
// shows for a failed transfer.
var (
	errPolicyDisabled = errors.New("storage policy disabled")
	errPolicyMissing  = errors.New("storage policy missing")
)

// policyFor resolves the storage policy a job uploading into parent belongs to
// (the directory policy, or the default policy for a legacy root).
func (s *Service) policyFor(ctx context.Context, parent domainfiles.Node) (int64, error) {
	if parent.StoragePolicyID != nil {
		return *parent.StoragePolicyID, nil
	}
	id, _, err := s.registry.Default(ctx)
	if err != nil {
		return 0, apperr.ErrInternal.WithCause(err)
	}
	return id, nil
}

// writeStore validates that the target policy accepts new content before a job
// is queued: a disabled policy is 507 insufficient_storage, a missing one is
// 400 invalid_target.
func (s *Service) writeStore(ctx context.Context, policyID int64) (*local.Store, error) {
	store, err := s.registry.Writable(ctx, policyID)
	if err != nil {
		switch {
		case errors.Is(err, storagereg.ErrPolicyDisabled):
			return nil, apperr.ErrInsufficientStorage.WithReason(apperr.ReasonStoragePolicyDisabled).WithCause(err)
		case errors.Is(err, storagereg.ErrPolicyNotFound):
			return nil, apperr.ErrInvalidTarget.WithCause(err)
		default:
			return nil, apperr.ErrInternal.WithCause(err)
		}
	}
	return store, nil
}

// progressWriter mirrors bytes into the object store and periodically records
// progress so the UI can show a live bar.
type progressWriter struct {
	w        io.Writer
	svc      *Service
	id       string
	max      int64
	written  int64
	lastSave int64
	tooLarge bool
}

func (p *progressWriter) Write(b []byte) (int, error) {
	if p.written+int64(len(b)) > p.max {
		p.tooLarge = true
		return 0, errTooLarge
	}
	n, err := p.w.Write(b)
	p.written += int64(n)
	if err == nil && p.written-p.lastSave >= 1<<20 {
		p.lastSave = p.written
		_ = p.svc.db.UpdateTransferProgress(context.Background(), p.id, p.written, 0, p.svc.now())
	}
	return n, err
}

func deriveName(p string) string {
	base := path.Base(strings.TrimRight(p, "/"))
	if base == "/" || base == "." || base == "" {
		return "download"
	}
	if unescaped, err := url.PathUnescape(base); err == nil {
		base = unescaped
	}
	return base
}

func mimeFor(name string) string {
	if mt := mime.TypeByExtension(filepath.Ext(name)); mt != "" {
		return mt
	}
	return ""
}

func errMessage(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return string(ae.Code)
	}
	msg := err.Error()
	if len(msg) > 300 {
		msg = msg[:300]
	}
	return msg
}

func classify(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, fetch.ErrBlocked) || errors.Is(err, fetch.ErrInvalidURL) || errors.Is(err, fetch.ErrTooManyRedirects) {
		return fmt.Errorf("source address is not allowed: %v", err)
	}
	return err
}
