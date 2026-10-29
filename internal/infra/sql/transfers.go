package sqlstore

import (
	"context"
	"database/sql"
	"time"

	"rapig/internal/domain/access"
	"rapig/internal/domain/transfer"
	"rapig/internal/platform/apperr"
)

const transferColumns = `id, owner_id, parent_id, storage_policy_id, name, name_folded, source_url, kind, qb_hash, qb_category, size,
	received_bytes, state, error, node_id, created_at, updated_at, started_at, finished_at`

type transferScanner interface {
	Scan(dest ...any) error
}

func scanTransfer(row transferScanner) (transfer.Transfer, error) {
	var (
		out               transfer.Transfer
		policyID          sql.NullInt64
		kind              string
		state             string
		nodeID            sql.NullInt64
		created, updated  int64
		started, finished sql.NullInt64
	)
	if err := row.Scan(&out.ID, &out.OwnerID, &out.ParentID, &policyID, &out.Name, &out.NameFolded,
		&out.SourceURL, &kind, &out.QBHash, &out.QBCategory, &out.Size, &out.ReceivedBytes, &state, &out.Error, &nodeID,
		&created, &updated, &started, &finished); err != nil {
		return transfer.Transfer{}, err
	}
	if policyID.Valid {
		out.StoragePolicyID = policyID.Int64
	}
	out.Kind = transfer.Kind(kind)
	out.State = transfer.State(state)
	out.CreatedAt = unixUTC(created)
	out.UpdatedAt = unixUTC(updated)
	if nodeID.Valid {
		v := nodeID.Int64
		out.NodeID = &v
	}
	if started.Valid {
		v := unixUTC(started.Int64)
		out.StartedAt = &v
	}
	if finished.Valid {
		v := unixUTC(finished.Int64)
		out.FinishedAt = &v
	}
	return out, nil
}

// FreeName is the exported conflict-free sibling name helper used by app
// services that create nodes outside the repository (copy, transfers).
func (d *DB) FreeName(ctx context.Context, sc access.Scope, parentID int64, name string, exceptID int64) (string, error) {
	if err := sc.Validate(); err != nil {
		return "", err
	}
	return d.freeName(ctx, sc, parentID, name, exceptID), nil
}

// CreateTransfer inserts a pending job for an active directory the caller owns.
func (d *DB) CreateTransfer(ctx context.Context, sc access.Scope, t transfer.Transfer) (transfer.Transfer, error) {
	if err := sc.Validate(); err != nil {
		return transfer.Transfer{}, err
	}
	return d.createTransfer(ctx, sc, t, transfer.StatePending, nil)
}

// CreateRunningTransfer inserts a job that is already running in its backend.
// .torrent uploads (M6.1b) hand the file to qBittorrent before the row
// exists, so the row must never be pending: the poller would try to add it a
// second time, without the file bytes.
func (d *DB) CreateRunningTransfer(ctx context.Context, sc access.Scope, t transfer.Transfer) (transfer.Transfer, error) {
	if err := sc.Validate(); err != nil {
		return transfer.Transfer{}, err
	}
	return d.createTransfer(ctx, sc, t, transfer.StateRunning, t.StartedAt)
}

func (d *DB) createTransfer(ctx context.Context, sc access.Scope, t transfer.Transfer, state transfer.State, started *time.Time) (transfer.Transfer, error) {
	if err := sc.Validate(); err != nil {
		return transfer.Transfer{}, err
	}
	var startedAt any
	if started != nil {
		startedAt = started.Unix()
	}
	accessSQL, accessArgs := nodeAccessSQLFor(sc, "n.")
	insertArgs := []any{t.ID, sc.UserID, t.StoragePolicyID, t.Name, t.NameFolded, t.SourceURL,
		string(t.KindValue()), t.QBHash, t.QBCategory,
		string(state), startedAt, t.CreatedAt.Unix(), t.UpdatedAt.Unix(), t.ParentID}
	insertArgs = append(insertArgs, accessArgs...)
	res, err := d.ExecContext(ctx, `INSERT INTO transfers
		(id, owner_id, parent_id, storage_policy_id, name, name_folded, source_url, kind, qb_hash, qb_category, size, received_bytes, state, error, started_at, created_at, updated_at)
		SELECT ?, ?, n.id, COALESCE(n.storage_policy_id, ?), ?, ?, ?, ?, ?, ?, 0, 0, ?, '', ?, ?, ?
		FROM nodes n WHERE n.id = ? AND `+accessSQL+` AND n.state = 'active' AND n.kind = 'dir'`,
		insertArgs...)
	if err != nil {
		if isForeignKeyViolation(err) {
			return transfer.Transfer{}, apperr.ErrNotFound
		}
		return transfer.Transfer{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return transfer.Transfer{}, apperr.ErrParentNotFound
	}
	return d.TransferByID(ctx, sc, t.ID)
}

// TransferByID loads one job owned by the scope user.
func (d *DB) TransferByID(ctx context.Context, sc access.Scope, id string) (transfer.Transfer, error) {
	if err := sc.Validate(); err != nil {
		return transfer.Transfer{}, err
	}
	row := d.QueryRowContext(ctx, `SELECT `+transferColumns+` FROM transfers WHERE id = ? AND owner_id = ?`, id, sc.UserID)
	out, err := scanTransfer(row)
	if isNoRows(err) {
		return transfer.Transfer{}, apperr.ErrNotFound
	}
	return out, err
}

// TransferByIDAny is the worker lookup (no owner scope).
func (d *DB) TransferByIDAny(ctx context.Context, id string) (transfer.Transfer, error) {
	row := d.QueryRowContext(ctx, `SELECT `+transferColumns+` FROM transfers WHERE id = ?`, id)
	out, err := scanTransfer(row)
	if isNoRows(err) {
		return transfer.Transfer{}, apperr.ErrNotFound
	}
	return out, err
}

// ListTransfers pages the caller's jobs, newest first, optionally by state.
func (d *DB) ListTransfers(ctx context.Context, sc access.Scope, state string, limit, offset int) ([]transfer.Transfer, int, error) {
	if err := sc.Validate(); err != nil {
		return nil, 0, err
	}
	where := `owner_id = ?`
	args := []any{sc.UserID}
	if state != "" {
		where += ` AND state = ?`
		args = append(args, state)
	}
	var total int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM transfers WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := d.QueryContext(ctx, `SELECT `+transferColumns+` FROM transfers WHERE `+where+`
		ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := make([]transfer.Transfer, 0, limit)
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// PauseTransfer moves a pending/running/paused job to paused.
func (d *DB) PauseTransfer(ctx context.Context, sc access.Scope, id string, now time.Time) (bool, error) {
	if err := sc.Validate(); err != nil {
		return false, err
	}
	res, err := d.ExecContext(ctx, `UPDATE transfers SET state = 'paused', updated_at = ?
		WHERE id = ? AND owner_id = ? AND state IN ('pending','running','paused')`,
		now.Unix(), id, sc.UserID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ResumeTransfer re-queues a paused/failed job; progress restarts from zero.
func (d *DB) ResumeTransfer(ctx context.Context, sc access.Scope, id string, now time.Time) (bool, error) {
	if err := sc.Validate(); err != nil {
		return false, err
	}
	res, err := d.ExecContext(ctx, `UPDATE transfers SET state = 'pending', received_bytes = 0,
		size = 0, error = '', started_at = NULL, finished_at = NULL, updated_at = ?
		WHERE id = ? AND owner_id = ? AND state IN ('paused','failed')`,
		now.Unix(), id, sc.UserID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// CancelTransfer moves an active job to canceled.
func (d *DB) CancelTransfer(ctx context.Context, sc access.Scope, id string, now time.Time) (bool, error) {
	if err := sc.Validate(); err != nil {
		return false, err
	}
	res, err := d.ExecContext(ctx, `UPDATE transfers SET state = 'canceled', finished_at = ?, updated_at = ?
		WHERE id = ? AND owner_id = ? AND state IN ('pending','running','paused')`,
		now.Unix(), now.Unix(), id, sc.UserID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteTransfer removes a job row the caller owns.
func (d *DB) DeleteTransfer(ctx context.Context, sc access.Scope, id string) (bool, error) {
	if err := sc.Validate(); err != nil {
		return false, err
	}
	res, err := d.ExecContext(ctx, `DELETE FROM transfers WHERE id = ? AND owner_id = ?`, id, sc.UserID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// PendingTransfers returns the oldest pending HTTP jobs (worker).
func (d *DB) PendingTransfers(ctx context.Context, limit int) ([]transfer.Transfer, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+transferColumns+` FROM transfers
		WHERE kind = 'http' AND state = 'pending' ORDER BY created_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]transfer.Transfer, 0, limit)
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ActiveQBTransfers returns pending/running qb jobs for the poller, least
// recently polled first so a backlog rotates through the bounded tracker
// instead of starving the newest job.
func (d *DB) ActiveQBTransfers(ctx context.Context, limit int) ([]transfer.Transfer, error) {
	rows, err := d.QueryContext(ctx, `SELECT `+transferColumns+` FROM transfers
		WHERE kind = 'qb' AND state IN ('pending','running') ORDER BY updated_at, created_at, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]transfer.Transfer, 0, limit)
	for rows.Next() {
		t, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ClaimTransfer atomically moves a pending job to running (worker).
func (d *DB) ClaimTransfer(ctx context.Context, id string, now time.Time) (bool, error) {
	res, err := d.ExecContext(ctx, `UPDATE transfers SET state = 'running', started_at = ?, updated_at = ?
		WHERE id = ? AND state = 'pending'`, now.Unix(), now.Unix(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateTransferProgress records downloaded bytes (worker).
func (d *DB) UpdateTransferProgress(ctx context.Context, id string, received, size int64, now time.Time) error {
	_, err := d.ExecContext(ctx, `UPDATE transfers SET received_bytes = ?, size = ?, updated_at = ?
		WHERE id = ?`, received, size, now.Unix(), id)
	return err
}

// UpdateTransferName rewrites the display name of a job (worker rename). It
// deliberately leaves updated_at alone: the same poll round refreshes it via
// UpdateTransferProgress, and ActiveQBTransfers rotates on that column.
func (d *DB) UpdateTransferName(ctx context.Context, id, name, nameFolded string) error {
	_, err := d.ExecContext(ctx, `UPDATE transfers SET name = ?, name_folded = ? WHERE id = ?`,
		name, nameFolded, id)
	return err
}

// FinishTransfer writes a terminal state (worker).
func (d *DB) FinishTransfer(ctx context.Context, id string, state transfer.State, errMsg string, nodeID *int64, size, received int64, now time.Time) error {
	var node any
	if nodeID != nil {
		node = *nodeID
	}
	_, err := d.ExecContext(ctx, `UPDATE transfers SET state = ?, error = ?, node_id = ?,
		size = ?, received_bytes = ?, finished_at = ?, updated_at = ?
		WHERE id = ?`, string(state), errMsg, node, size, received, now.Unix(), now.Unix(), id)
	return err
}

// TransferState returns only the current state (worker pause/cancel detection).
func (d *DB) TransferState(ctx context.Context, id string) (transfer.State, error) {
	var s string
	err := d.QueryRowContext(ctx, `SELECT state FROM transfers WHERE id = ?`, id).Scan(&s)
	if isNoRows(err) {
		return "", apperr.ErrNotFound
	}
	return transfer.State(s), err
}

// CleanupTransfers deletes terminal jobs older than before.
func (d *DB) CleanupTransfers(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM transfers WHERE state IN ('completed','failed','canceled')
		AND updated_at < ?`, before.Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
