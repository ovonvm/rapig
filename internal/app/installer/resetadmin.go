package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	domainidentity "rapig/internal/domain/identity"
	sqlstore "rapig/internal/infra/sql"
	"rapig/internal/platform/passwd"
	"rapig/migrations"
)

// resetAdminPassword is the offline part of `rapig reset-admin-password`: it
// updates the stored hash and revokes every session. The service must be
// stopped so WAL writers do not contend.
func resetAdminPassword(ctx context.Context, dataDir, username, password string) error {
	dbPath := filepath.Join(dataDir, "db", "rapig.db")
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("database not found at %s (is the service running? stop it first): %w", dbPath, err)
	}
	db, err := sqlstore.OpenWithOptions(ctx, dbPath, sqlstore.Options{})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	acc, err := db.UserByUsername(ctx, domainidentity.FoldUsername(username))
	if err != nil {
		return fmt.Errorf("account %q not found: %w", username, err)
	}
	hash, err := passwd.Hash(password)
	if err != nil {
		return err
	}
	if err := db.UpdatePassword(ctx, acc.ID, hash, time.Now().UTC()); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	revoked, err := db.DeleteUserSessionsExcept(ctx, acc.ID, "")
	if err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	// The one-time bootstrap file no longer matches the account password.
	_ = os.Remove(filepath.Join(dataDir, "bootstrap", "initial-admin.txt"))
	_ = db.InsertAudit(ctx, sqlstore.AuditEvent{
		Action:     "admin.password_reset",
		TargetType: "user",
		TargetID:   acc.ID,
		Result:     "success",
		Detail:     fmt.Sprintf(`{"via":"cli","revoked_sessions":%d}`, revoked),
	})
	return nil
}
