package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// AuditFilter narrows the admin audit page. Zero values mean "no filter".
type AuditFilter struct {
	Actor  string // substring match on actor_name
	Action string // exact action code
	From   time.Time
	To     time.Time
	Limit  int
	Offset int
}

// AuditPage is one page of audit rows plus the total under the same filter.
type AuditPage struct {
	Items []AuditEvent
	Total int
}

// ListAudit returns audit rows newest first under the filter.
func (d *DB) ListAudit(ctx context.Context, f AuditFilter) (AuditPage, error) {
	conds := make([]string, 0, 4)
	args := make([]any, 0, 8)
	if f.Actor != "" {
		conds = append(conds, "actor_name LIKE ?")
		args = append(args, "%"+f.Actor+"%")
	}
	if f.Action != "" {
		conds = append(conds, "action = ?")
		args = append(args, f.Action)
	}
	if !f.From.IsZero() {
		conds = append(conds, "at >= ?")
		args = append(args, f.From.Unix())
	}
	if !f.To.IsZero() {
		conds = append(conds, "at <= ?")
		args = append(args, f.To.Unix())
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	var total int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit`+where, args...).Scan(&total); err != nil {
		return AuditPage{}, fmt.Errorf("sqlstore: count audit: %w", err)
	}
	rows, err := d.QueryContext(ctx,
		`SELECT at, COALESCE(actor_id, 0), actor_name, action, target_type, target_id,
			result, ip, user_agent, request_id, detail
		 FROM audit`+where+` ORDER BY at DESC, id DESC LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
	if err != nil {
		return AuditPage{}, fmt.Errorf("sqlstore: list audit: %w", err)
	}
	defer rows.Close()
	out := make([]AuditEvent, 0, f.Limit)
	for rows.Next() {
		var (
			at      int64
			ev      AuditEvent
			target  sql.NullInt64
			detail  sql.NullString
			action  string
			actorID int64
		)
		if err := rows.Scan(&at, &actorID, &ev.ActorName, &action, &ev.TargetType, &target,
			&ev.Result, &ev.IP, &ev.UserAgent, &ev.RequestID, &detail); err != nil {
			return AuditPage{}, fmt.Errorf("sqlstore: scan audit: %w", err)
		}
		ev.At = time.Unix(at, 0).UTC()
		ev.ActorID = actorID
		ev.Action = action
		if target.Valid {
			ev.TargetID = target.Int64
		}
		if detail.Valid {
			ev.Detail = detail.String
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return AuditPage{}, fmt.Errorf("sqlstore: audit rows: %w", err)
	}
	return AuditPage{Items: out, Total: total}, nil
}

// AuditActions returns the distinct action codes currently present, for the
// audit page's filter dropdown.
func (d *DB) AuditActions(ctx context.Context) ([]string, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT DISTINCT action FROM audit ORDER BY action ASC`)
	if err != nil {
		return nil, fmt.Errorf("sqlstore: audit actions: %w", err)
	}
	defer rows.Close()
	out := make([]string, 0, 16)
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err != nil {
			return nil, fmt.Errorf("sqlstore: scan audit action: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlstore: audit action rows: %w", err)
	}
	return out, nil
}
