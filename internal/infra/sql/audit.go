package sqlstore

import (
	"context"
	"fmt"
	"time"
)

// AuditEvent is one row of the audit log.
type AuditEvent struct {
	At         time.Time
	ActorID    int64
	ActorName  string
	Action     string
	TargetType string
	TargetID   int64
	Result     string
	IP         string
	UserAgent  string
	RequestID  string
	Detail     string
}

// InsertAudit appends an audit event. Audit failures must never break the
// user visible operation, so callers log and continue when this returns an
// error.
func (d *DB) InsertAudit(ctx context.Context, ev AuditEvent) error {
	at := ev.At
	if at.IsZero() {
		at = time.Now()
	}
	var actor any
	if ev.ActorID > 0 {
		actor = ev.ActorID
	}
	_, err := d.ExecContext(ctx,
		`INSERT INTO audit (at, actor_id, actor_name, action, target_type, target_id,
			result, ip, user_agent, request_id, detail)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		at.Unix(), actor, ev.ActorName, ev.Action, ev.TargetType, ev.TargetID,
		ev.Result, ev.IP, ev.UserAgent, ev.RequestID, ev.Detail)
	if err != nil {
		return fmt.Errorf("sqlstore: insert audit: %w", err)
	}
	return nil
}

// PruneAudit removes audit rows older than before and returns the count. The
// worker calls this with the retention window from settings so an audit table
// on a small server cannot grow without bound.
func (d *DB) PruneAudit(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM audit WHERE at < ?`, before.Unix())
	if err != nil {
		return 0, fmt.Errorf("sqlstore: prune audit: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("sqlstore: prune audit rows: %w", err)
	}
	return n, nil
}
