package transfers

// qBittorrent (M6.1) support: creating jobs, polling the WebUI API and
// importing a finished torrent as a regular node.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	"rapig/internal/domain/transfer"
	"rapig/internal/infra/qb"
	sqlstore "rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/idgen"
)

const (
	// qbMaxTracked bounds simultaneously polled qb jobs (2s cadence).
	qbMaxTracked = 2
	// qbActionTimeout bounds one side-effecting qB call (pause/resume/delete).
	qbActionTimeout = 15 * time.Second
	// qbFinalizeTimeout bounds cleanup after a terminal state; it runs on a
	// background context so a shutting-down poller still tidies qB.
	qbFinalizeTimeout = 20 * time.Second
	// qbTorrentRequeueError explains why a pending file-based job cannot be
	// resumed: the uploaded bytes are never persisted.
	qbTorrentRequeueError = "任务需要重新上传种子文件"
)

// QBEnabled reports whether qBittorrent offline downloads are configured.
func (s *Service) QBEnabled() bool { return s.qb != nil }

// CreateQB validates a magnet link and queues a qb job. The parent must
// already be authorized and loaded by the caller.
func (s *Service) CreateQB(ctx context.Context, p access.Principal, parent domainfiles.Node, name, magnet string) (transfer.Transfer, error) {
	if s.qb == nil {
		return transfer.Transfer{}, apperr.ErrQBTUnavailable
	}
	m, err := transfer.ParseMagnet(magnet)
	if err != nil {
		return transfer.Transfer{}, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidMagnet).WithCause(err)
	}
	if !parent.IsRoot() && parent.Kind != domainfiles.KindDir {
		return transfer.Transfer{}, apperr.ErrParentNotFound
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = m.DisplayName
		// A display name is advisory (it can exceed the name limit or hold
		// separators); fall back to the infohash when it is unusable.
		if name == "" || domainfiles.ValidateName(name) != nil {
			name = m.Hash
		}
	}
	if err := domainfiles.ValidateName(name); err != nil {
		// An explicit name from the client is an error.
		return transfer.Transfer{}, apperr.ErrInvalidName.WithCause(err)
	}
	policyID, err := s.policyFor(ctx, parent)
	if err != nil {
		return transfer.Transfer{}, err
	}
	if _, err := s.writeStore(ctx, policyID); err != nil {
		return transfer.Transfer{}, err
	}
	now := s.now()
	t := transfer.Transfer{
		ID:              idgen.MustToken(transferIDBytes),
		OwnerID:         p.UserID,
		ParentID:        parent.ID,
		StoragePolicyID: policyID,
		Name:            name,
		NameFolded:      domainfiles.FoldName(name),
		SourceURL:       strings.TrimSpace(magnet),
		Kind:            transfer.KindQB,
		QBHash:          m.Hash,
		QBCategory:      fmt.Sprintf("rapig-%d", p.UserID),
		State:           transfer.StatePending,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	out, err := s.db.CreateTransfer(ctx, access.ScopeFor(p), t)
	if err != nil {
		return transfer.Transfer{}, err
	}
	// Never log the magnet: private-tracker links can embed passkeys.
	s.log.Info("transfer.qb.create", "transfer_id", out.ID, "name", out.Name, "hash", out.QBHash)
	return out, nil
}

// CreateTorrentQB queues a qb job from an uploaded .torrent file and records
// it as already running: the file is handed to qBittorrent first, and only
// the infohash is kept, so there is nothing to re-add later. The parent must
// already be authorized and loaded by the caller.
func (s *Service) CreateTorrentQB(ctx context.Context, p access.Principal, parent domainfiles.Node, name string, data []byte) (transfer.Transfer, error) {
	if s.qb == nil {
		return transfer.Transfer{}, apperr.ErrQBTUnavailable
	}
	if len(data) == 0 || len(data) > transfer.MaxTorrentSize {
		return transfer.Transfer{}, apperr.ErrPayloadTooLarge.WithReason(apperr.ReasonTorrentTooLarge)
	}
	meta, err := qb.ParseTorrent(data)
	if err != nil {
		return transfer.Transfer{}, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidTorrent).WithCause(err)
	}
	if !parent.IsRoot() && parent.Kind != domainfiles.KindDir {
		return transfer.Transfer{}, apperr.ErrParentNotFound
	}
	name = strings.TrimSpace(name)
	if name == "" {
		// The torrent name is advisory (it can exceed the name limit or hold
		// separators); fall back to the infohash when it is unusable.
		name = strings.TrimSpace(meta.Name)
		if name == "" || domainfiles.ValidateName(name) != nil {
			name = meta.InfoHash
		}
	}
	if err := domainfiles.ValidateName(name); err != nil {
		// An explicit name from the client is an error.
		return transfer.Transfer{}, apperr.ErrInvalidName.WithCause(err)
	}
	policyID, err := s.policyFor(ctx, parent)
	if err != nil {
		return transfer.Transfer{}, err
	}
	if _, err := s.writeStore(ctx, policyID); err != nil {
		return transfer.Transfer{}, err
	}
	now := s.now()
	started := now
	t := transfer.Transfer{
		ID:              idgen.MustToken(transferIDBytes),
		OwnerID:         p.UserID,
		ParentID:        parent.ID,
		StoragePolicyID: policyID,
		Name:            name,
		NameFolded:      domainfiles.FoldName(name),
		SourceURL:       "torrent:" + meta.InfoHash,
		Kind:            transfer.KindQB,
		QBHash:          meta.InfoHash,
		QBCategory:      fmt.Sprintf("rapig-%d", p.UserID),
		State:           transfer.StateRunning,
		CreatedAt:       now,
		UpdatedAt:       now,
		StartedAt:       &started,
	}
	savePath := s.qbSavePath(t)
	if err := os.MkdirAll(savePath, 0o755); err != nil {
		return transfer.Transfer{}, apperr.ErrInternal.WithCause(err)
	}
	if err := s.qb.AddTorrentFile(ctx, meta.InfoHash+".torrent", data, savePath, t.QBCategory, false); err != nil {
		_ = os.RemoveAll(savePath)
		if errors.Is(err, qb.ErrRejected) {
			// A refused torrent is corrupt or unsupported, not a backend
			// outage: qBittorrent's answer is permanent for this file.
			return transfer.Transfer{}, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidTorrent).WithCause(err)
		}
		if errors.Is(err, qb.ErrDuplicate) {
			// The torrent is already in qBittorrent (left over from an
			// earlier attempt); importing it now would read the wrong
			// save path, so ask the user to remove the old task first.
			return transfer.Transfer{}, apperr.ErrDuplicateTorrent.WithCause(err)
		}
		return transfer.Transfer{}, apperr.ErrQBTUnavailable.WithCause(err)
	}
	out, err := s.db.CreateRunningTransfer(ctx, access.ScopeFor(p), t)
	if err != nil {
		// qBittorrent already has the torrent; undo that so a failed insert
		// does not leave an unmanaged download behind.
		s.cleanupQBAsync(t)
		return transfer.Transfer{}, err
	}
	// Never log the torrent bytes, only the metadata.
	s.log.Info("transfer.qb.create", "transfer_id", out.ID, "name", out.Name, "hash", out.QBHash, "source", "file")
	return out, nil
}

// RunQB starts one poll tick for up to qbMaxTracked jobs and returns how many
// pollers it started. Jobs are polled, not downloaded here: each goroutine
// performs exactly one qBittorrent API round trip.
func (s *Service) RunQB(ctx context.Context) int {
	if s.qb == nil || s.dataDir == "" {
		return 0
	}
	s.qbMu.Lock()
	avail := qbMaxTracked - len(s.qbActive)
	s.qbMu.Unlock()
	if avail <= 0 {
		return 0
	}
	list, err := s.db.ActiveQBTransfers(ctx, qbMaxTracked*2)
	if err != nil {
		s.log.Warn("transfer.qb.pending", "err", err)
		return 0
	}
	started := 0
	for _, t := range list {
		if started >= avail {
			break
		}
		s.qbMu.Lock()
		if _, busy := s.qbActive[t.ID]; busy {
			s.qbMu.Unlock()
			continue
		}
		s.qbActive[t.ID] = struct{}{}
		s.qbMu.Unlock()
		started++
		s.wg.Add(1)
		go func(t transfer.Transfer) {
			defer s.wg.Done()
			defer func() {
				s.qbMu.Lock()
				delete(s.qbActive, t.ID)
				s.qbMu.Unlock()
			}()
			s.syncQBTask(ctx, t)
		}(t)
	}
	return started
}

// syncQBTask performs one poll round: add (pending), refresh progress
// (running) or import (complete).
func (s *Service) syncQBTask(ctx context.Context, t transfer.Transfer) {
	cur, err := s.db.TransferByIDAny(ctx, t.ID)
	if err != nil {
		return
	}
	if cur.KindValue() != transfer.KindQB || !cur.Active() || cur.State == transfer.StatePaused {
		return
	}
	if cur.QBHash == "" {
		s.failQB(ctx, cur, "任务缺少 infohash")
		return
	}
	if err := s.qb.EnsureLogin(ctx); err != nil {
		s.log.Warn("transfer.qb.login", "transfer_id", cur.ID, "err", err)
		return
	}
	infos, err := s.qb.TorrentInfo(ctx, []string{cur.QBHash})
	if err != nil {
		s.log.Warn("transfer.qb.info", "transfer_id", cur.ID, "err", err)
		return
	}
	if len(infos) == 0 {
		if cur.State == transfer.StateRunning {
			// The user removed it in qBittorrent (or qB lost the task).
			s.failQB(ctx, cur, "qBittorrent 中任务已消失")
			return
		}
		s.startQBTorrent(ctx, cur)
		return
	}
	info := infos[0]
	if qbFailedState(info.State) {
		s.failQB(ctx, cur, "qBittorrent 任务失败: "+info.State)
		return
	}
	// A torrent without a usable name was stored under its infohash; qB knows
	// the real name once metadata is loaded, so adopt it. A name the user
	// chose is never overwritten.
	if name, ok := qbBackfillName(cur, info.Name); ok {
		if err := s.db.UpdateTransferName(ctx, cur.ID, name, domainfiles.FoldName(name)); err != nil {
			s.log.Warn("transfer.qb.name", "transfer_id", cur.ID, "err", err)
		} else {
			s.log.Info("transfer.qb.rename", "transfer_id", cur.ID, "name", name)
			cur.Name = name
			cur.NameFolded = domainfiles.FoldName(name)
		}
	}
	received := info.Size
	if info.Progress < 1 {
		received = int64(info.Progress * float64(info.Size))
		if received < 0 {
			received = 0
		}
	}
	if err := s.db.UpdateTransferProgress(ctx, cur.ID, received, info.Size, s.now()); err != nil {
		s.log.Warn("transfer.qb.progress", "transfer_id", cur.ID, "err", err)
	}
	if cur.State == transfer.StatePending {
		ok, err := s.db.ClaimTransfer(ctx, cur.ID, s.now())
		if err != nil {
			s.log.Warn("transfer.qb.claim", "transfer_id", cur.ID, "err", err)
			return
		}
		if !ok {
			// Paused/deleted between the load and the claim.
			return
		}
	}
	if info.Progress < 1 {
		return
	}
	s.importQB(ctx, cur, info)
}

// startQBTorrent adds a pending job to qBittorrent once.
func (s *Service) startQBTorrent(ctx context.Context, t transfer.Transfer) {
	// Uploaded .torrent jobs are queued in qBittorrent at creation time and
	// their bytes are never persisted, so a pending non-magnet row can only
	// be a job qBittorrent lost; re-adding it from SourceURL cannot work.
	if t.KindValue() == transfer.KindQB && !strings.HasPrefix(t.SourceURL, "magnet:") {
		s.failQB(ctx, t, qbTorrentRequeueError)
		return
	}
	savePath := s.qbSavePath(t)
	if err := os.MkdirAll(savePath, 0o755); err != nil {
		s.failQB(ctx, t, "创建下载目录失败: "+err.Error())
		return
	}
	err := s.qb.AddTorrent(ctx, []string{t.SourceURL}, savePath, t.QBCategory, false)
	if err != nil {
		// A refused add is permanent; transport failures are retried on the
		// next tick because qBittorrent may just be restarting.
		if errors.Is(err, qb.ErrRejected) || errors.Is(err, qb.ErrAuth) || errors.Is(err, qb.ErrDisabled) {
			s.failQB(ctx, t, "qBittorrent 添加任务失败: "+err.Error())
			return
		}
		s.log.Warn("transfer.qb.add", "transfer_id", t.ID, "err", err)
		return
	}
	if _, err := s.db.ClaimTransfer(ctx, t.ID, s.now()); err != nil {
		s.log.Warn("transfer.qb.claim", "transfer_id", t.ID, "err", err)
	}
	s.log.Info("transfer.qb.added", "transfer_id", t.ID, "hash", t.QBHash)
}

// importQB imports a finished torrent. A single-file torrent lands directly in
// the target folder under its real on-disk name; a multi-file torrent becomes
// one top-level directory that mirrors its layout (files with priority 0 in
// qBittorrent are skipped). Blobs are copied first, then one SQL transaction
// creates the whole tree and finishes the transfer, so a failure leaves no
// partial tree behind (M6.1c).
func (s *Service) importQB(ctx context.Context, t transfer.Transfer, info qb.Torrent) {
	savePath := s.qbSavePath(t)
	// Do not import into a job the user paused while metadata was loading.
	if st, err := s.db.TransferState(ctx, t.ID); err != nil || (st != transfer.StateRunning && st != transfer.StatePending) {
		return
	}
	list, err := s.qb.Files(ctx, t.QBHash)
	if err != nil {
		// Retried on the next tick: qBittorrent may be restarting.
		s.log.Warn("transfer.qb.files", "transfer_id", t.ID, "err", err)
		return
	}
	plan, ferr := qbPlanImport(savePath, list)
	if ferr != nil {
		s.failQB(ctx, t, ferr.Error())
		return
	}
	if len(plan.entries) == 0 {
		s.failQB(ctx, t, "种子没有可导入的文件")
		return
	}

	policyID := t.StoragePolicyID
	if policyID == 0 {
		parent, perr := s.db.NodeByID(ctx, access.UserScope(t.OwnerID, false), t.ParentID)
		if perr != nil {
			s.failQB(ctx, t, "目标文件夹不存在")
			return
		}
		if policyID, perr = s.policyFor(ctx, parent); perr != nil {
			s.failQB(ctx, t, errMessage(perr))
			return
		}
	}
	store, err := s.registry.Writable(ctx, policyID)
	if err != nil {
		switch {
		case errors.Is(err, storagereg.ErrPolicyDisabled):
			s.failQB(ctx, t, errPolicyDisabled.Error())
		case errors.Is(err, storagereg.ErrPolicyNotFound):
			s.failQB(ctx, t, errPolicyMissing.Error())
		default:
			s.failQB(ctx, t, errMessage(err))
		}
		return
	}

	sc := access.UserScope(t.OwnerID, false)
	var (
		created   []string
		entries   []sqlstore.QBImportEntry
		first     int64
		firstInc  bool
		firstSize int64
	)
	copyOne := func(disk string) (blobID int64, size int64, isNew bool, err error) {
		src, oerr := os.Open(disk)
		if oerr != nil {
			return 0, 0, false, fmt.Errorf("打开下载文件失败: %w", oerr)
		}
		sha, size, perr := store.Put(ctx, src)
		_ = src.Close()
		if perr != nil {
			return 0, 0, false, fmt.Errorf("写入存储失败: %w", perr)
		}
		blob, isNew, berr := s.db.InsertBlobInPolicy(ctx, policyID, sha, size)
		if berr != nil {
			return 0, 0, false, berr
		}
		if isNew {
			created = append(created, sha)
		}
		return blob.ID, size, isNew, nil
	}

	if plan.single {
		blobID, size, isNew, cerr := copyOne(plan.entries[0].disk)
		if cerr != nil {
			s.failQB(ctx, t, errMessage(cerr))
			return
		}
		first, firstSize, firstInc = blobID, size, !isNew
	} else {
		for _, e := range plan.entries {
			blobID, size, isNew, cerr := copyOne(e.disk)
			if cerr != nil {
				s.rollbackQBImport(store, created)
				s.failQB(ctx, t, errMessage(cerr))
				return
			}
			entries = append(entries, sqlstore.QBImportEntry{
				Segs: e.segs, Size: size, BlobID: blobID, IncrementRef: !isNew,
				Mime: mimeFor(e.segs[len(e.segs)-1]),
			})
		}
	}

	topName := plan.topName(t.Name)
	free, err := s.db.FreeName(ctx, sc, t.ParentID, topName, 0)
	if err != nil {
		s.rollbackQBImport(store, created)
		s.failQB(ctx, t, errMessage(err))
		return
	}
	node, err := s.db.CommitQBImport(ctx, sc, sqlstore.QBImportInput{
		TransferID:      t.ID,
		ParentID:        t.ParentID,
		StoragePolicyID: policyID,
		TopName:         free,
		TopIsFile:       plan.single,
		TopSize:         firstSize,
		TopBlobID:       first,
		TopIncrementRef: firstInc,
		TopMime:         mimeFor(free),
		Entries:         entries,
		Now:             s.now(),
	})
	if err != nil {
		s.rollbackQBImport(store, created)
		s.failQB(ctx, t, errMessage(err))
		return
	}
	s.log.Info("transfer.qb.completed", "transfer_id", t.ID, "node_id", node.ID,
		"files", len(plan.entries), "multi", !plan.single, "size", plan.total,
		"torrent_state", info.State)
	// Delete the torrent and its files only after the job row is terminal;
	// otherwise a crash in between would re-import the same torrent.
	s.cleanupQBAsync(t)
}

// rollbackQBImport drops the blob references taken by a failed import; the
// row disappears with its last reference and the object file follows.
func (s *Service) rollbackQBImport(store interface{ Remove(string) error }, shas []string) {
	for _, sha := range shas {
		blob, err := s.db.BlobBySHA(context.Background(), sha)
		if err != nil {
			continue
		}
		if remaining, derr := s.db.DecRef(context.Background(), blob.ID); derr == nil && remaining == 0 {
			_ = store.Remove(sha)
		}
	}
}

// qbImportFile is one regular file to import, with its path segments below the
// imported top node and its absolute on-disk path.
type qbImportFile struct {
	segs []string
	disk string
	size int64
}

// qbImportPlan is the validated shape of a finished torrent.
type qbImportPlan struct {
	single  bool
	entries []qbImportFile
	total   int64
	// torrentName is the qBittorrent-reported name, used when the on-disk
	// single file name is unusable.
	torrentName string
}

// topName returns the imported top-level node name: the torrent name for a
// multi-file torrent, the real file name for a single-file one.
func (p qbImportPlan) topName(reported string) string {
	if !p.single {
		return reported
	}
	name := p.entries[0].segs[len(p.entries[0].segs)-1]
	if domainfiles.ValidateName(name) != nil {
		return reported
	}
	return name
}

// qbPlanImport validates the qBittorrent file list against the download
// directory. Skipped files are dropped, path segments must be safe, and every
// remaining file must exist with the reported size (M6.1c decisions 2/3/5).
func qbPlanImport(savePath string, list []qb.TorrentFile) (qbImportPlan, error) {
	plan := qbImportPlan{}
	multi, err := qbIsMulti(savePath)
	if err != nil {
		return plan, fmt.Errorf("读取 qBittorrent 下载目录失败: %w", err)
	}
	plan.single = !multi
	count := 0
	for _, f := range list {
		if f.Priority == 0 {
			continue // skipped in qBittorrent
		}
		name := filepath.ToSlash(f.Name)
		if strings.HasSuffix(name, ".!qB") {
			continue // qBittorrent metadata file for in-progress downloads
		}
		segs := strings.Split(name, "/")
		if multi {
			// Multi-file torrents include their root directory in every name.
			if len(segs) < 2 {
				return plan, fmt.Errorf("种子文件路径不完整：%q", f.Name)
			}
			segs = segs[1:]
			if plan.torrentName == "" {
				plan.torrentName = strings.SplitN(name, "/", 2)[0]
			}
		}
		if len(segs) == 0 || segs[0] == "" {
			continue
		}
		if len(segs) > transfer.MaxQBImportDepth {
			return plan, fmt.Errorf("种子目录层级超过上限 %d", transfer.MaxQBImportDepth)
		}
		rel := ""
		for _, seg := range segs {
			if seg == "" || seg == "." || seg == ".." {
				return plan, fmt.Errorf("种子包含非法路径：%q", f.Name)
			}
			if seg[0] == '.' && seg != "." {
				// Hidden directories (qBittorrent internals) are not imported.
				return plan, fmt.Errorf("种子包含非法路径：%q", f.Name)
			}
			if verr := domainfiles.ValidateName(seg); verr != nil {
				return plan, fmt.Errorf("种子包含非法文件名：%q", seg)
			}
			rel += "/" + seg
		}
		if len(rel) > transfer.MaxQBImportPathLen {
			return plan, fmt.Errorf("种子路径过长：%q", f.Name)
		}
		if f.Size > transfer.MaxSize {
			return plan, fmt.Errorf("文件超过 10 GB 上限：%q", f.Name)
		}
		count++
		if count > transfer.MaxQBImportFiles {
			return plan, fmt.Errorf("种子包含超过 %d 个文件，超过上限", transfer.MaxQBImportFiles)
		}
		disk := filepath.Join(savePath, filepath.FromSlash(name))
		if !qbInside(savePath, disk) {
			return plan, fmt.Errorf("种子包含非法路径：%q", f.Name)
		}
		fi, serr := os.Lstat(disk)
		if serr != nil {
			return plan, fmt.Errorf("读取下载文件失败：%q", f.Name)
		}
		if !fi.Mode().IsRegular() {
			return plan, fmt.Errorf("下载内容是符号链接或目录：%q", f.Name)
		}
		if fi.Size() != f.Size {
			return plan, fmt.Errorf("文件大小与种子不一致：%q", f.Name)
		}
		plan.entries = append(plan.entries, qbImportFile{segs: segs, disk: disk, size: f.Size})
		plan.total += f.Size
	}
	return plan, nil
}

// qbIsMulti reports whether the finished torrent left a single root directory
// in the save path (multi-file torrent) rather than plain files.
func qbIsMulti(savePath string) (bool, error) {
	entries, err := os.ReadDir(savePath)
	if err != nil {
		return false, err
	}
	dirs, files := 0, 0
	for _, e := range entries {
		if e.IsDir() {
			dirs++
		} else {
			files++
		}
	}
	return dirs == 1 && files == 0, nil
}

// qbInside reports whether path stays below root after cleaning.
func qbInside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// failQB records a terminal failure, then removes the qBittorrent task and
// the temporary download directory.
func (s *Service) failQB(ctx context.Context, t transfer.Transfer, msg string) {
	fctx, cancel := context.WithTimeout(context.Background(), qbFinalizeTimeout)
	defer cancel()
	// The in-memory snapshot can predate the last progress update; keep the
	// bytes the user has already seen in the UI.
	size, received := t.Size, t.ReceivedBytes
	if cur, err := s.db.TransferByIDAny(fctx, t.ID); err == nil {
		size, received = cur.Size, cur.ReceivedBytes
	}
	if err := s.db.FinishTransfer(fctx, t.ID, transfer.StateFailed, msg, nil, size, received, s.now()); err != nil {
		s.log.Error("transfer.qb.finish", "transfer_id", t.ID, "err", err)
	}
	s.cleanupQB(fctx, t)
	s.log.Warn("transfer.qb.failed", "transfer_id", t.ID, "err", msg)
}

// cleanupQBAsync removes the qBittorrent task on a background context so a
// canceled poll does not leave downloads behind.
func (s *Service) cleanupQBAsync(t transfer.Transfer) {
	ctx, cancel := context.WithTimeout(context.Background(), qbFinalizeTimeout)
	defer cancel()
	s.cleanupQB(ctx, t)
}

// cleanupQB deletes the torrent (with files) and the task directory.
func (s *Service) cleanupQB(ctx context.Context, t transfer.Transfer) {
	if s.qb != nil && t.QBHash != "" {
		cctx, cancel := context.WithTimeout(ctx, qbActionTimeout)
		if err := s.qb.Delete(cctx, []string{t.QBHash}, true); err != nil {
			s.log.Warn("transfer.qb.delete", "transfer_id", t.ID, "hash", t.QBHash, "err", err)
		}
		cancel()
	}
	if s.dataDir != "" {
		if err := os.RemoveAll(s.qbSavePath(t)); err != nil {
			s.log.Warn("transfer.qb.cleanup", "transfer_id", t.ID, "err", err)
		}
	}
}

func (s *Service) qbSavePath(t transfer.Transfer) string {
	return filepath.Join(s.dataDir, "tmp", "qb", strconv.FormatInt(t.OwnerID, 10), t.ID)
}

// pauseQBTorrent forwards a pause; failures are advisory because the row is
// already paused locally.
func (s *Service) pauseQBTorrent(ctx context.Context, t transfer.Transfer) {
	if s.qb == nil || t.QBHash == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, qbActionTimeout)
	defer cancel()
	if err := s.qb.Pause(cctx, []string{t.QBHash}); err != nil {
		s.log.Warn("transfer.qb.pause", "transfer_id", t.ID, "err", err)
	}
}

// resumeQBTorrent forwards a resume; a missing torrent is re-added by the
// next poll tick.
func (s *Service) resumeQBTorrent(ctx context.Context, t transfer.Transfer) {
	if s.qb == nil || t.QBHash == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, qbActionTimeout)
	defer cancel()
	if err := s.qb.Resume(cctx, []string{t.QBHash}); err != nil {
		s.log.Warn("transfer.qb.resume", "transfer_id", t.ID, "err", err)
	}
}

// deleteQBTorrent removes the qBittorrent task and the temporary directory
// before the row itself is deleted.
func (s *Service) deleteQBTorrent(ctx context.Context, t transfer.Transfer) {
	cctx, cancel := context.WithTimeout(ctx, qbActionTimeout)
	defer cancel()
	s.cleanupQB(cctx, t)
}

// qbFailedState reports qBittorrent states that can never finish.
func qbFailedState(state string) bool {
	s := strings.ToLower(state)
	return s == "error" || s == "missingfiles" || strings.Contains(s, "error")
}

// qbBackfillName reports whether qBittorrent's torrent name may replace the
// row name: only while the row still shows a placeholder (empty, the infohash
// or a 40 hex string) and the reported name is valid for Rapig.
func qbBackfillName(cur transfer.Transfer, reported string) (string, bool) {
	name := strings.TrimSpace(reported)
	if name == "" || name == cur.Name {
		return "", false
	}
	if cur.Name != "" && !strings.EqualFold(cur.Name, cur.QBHash) && !isInfohashHex(cur.Name) {
		// The user picked this name; keep it.
		return "", false
	}
	if domainfiles.ValidateName(name) != nil {
		return "", false
	}
	return name, true
}

// isInfohashHex reports whether s is a 40 character hex string.
func isInfohashHex(s string) bool {
	if len(s) != 40 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
