package identity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	domainidentity "rapig/internal/domain/identity"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/idgen"
)

// BootstrapResult describes the one-time admin bootstrap.
type BootstrapResult struct {
	Created  bool
	Username string
	Password string
	FilePath string
	UserID   int64
	RootID   int64
}

// Bootstrap creates the initial admin account on an empty database. The
// generated password is printed to the log and written to
// <data>/bootstrap/initial-admin.txt (0600) so the operator can log in once
// and change it.
func (s *Service) Bootstrap(ctx context.Context, bootstrapDir string, policyID int64) (BootstrapResult, error) {
	n, err := s.db.CountUsers(ctx)
	if err != nil {
		return BootstrapResult{}, err
	}
	if n > 0 {
		return BootstrapResult{}, nil
	}

	roleID, err := s.db.RoleIDByCode(ctx, "admin")
	if err != nil {
		return BootstrapResult{}, err
	}
	password := idgen.MustToken(15) // 20 URL-safe characters
	hash, err := hashPassword(password)
	if err != nil {
		return BootstrapResult{}, err
	}

	user, err := s.db.CreateUser(ctx, domainidentity.User{
		Username:       "admin",
		UsernameFolded: "admin",
		DisplayName:    "管理员",
		PasswordHash:   hash,
		RoleID:         roleID,
		Status:         domainidentity.StatusActive,
		// The bootstrap account is the root administrator: the only one
		// allowed to toggle self registration. The migration cannot set this
		// because it runs before this account exists.
		IsRoot: true,
	})
	if err != nil {
		// Another process won the race: treat as already bootstrapped.
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return BootstrapResult{}, nil
		}
		return BootstrapResult{}, err
	}

	root, err := s.db.EnsureRoot(ctx, user.ID, policyID)
	if err != nil {
		return BootstrapResult{}, err
	}

	if err := os.MkdirAll(bootstrapDir, 0o700); err != nil {
		return BootstrapResult{}, fmt.Errorf("identity: create bootstrap dir: %w", err)
	}
	filePath := filepath.Join(bootstrapDir, "initial-admin.txt")
	content := fmt.Sprintf("username: admin\npassword: %s\ncreated_at: %s\n",
		password, time.Now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		return BootstrapResult{}, fmt.Errorf("identity: write initial admin file: %w", err)
	}

	s.audit(ctx, auditInput{
		actorID: user.ID, actorName: user.Username,
		action: "admin.bootstrap", result: "success",
		detail: fmt.Sprintf(`{"root_node_id":%d}`, root.ID),
	})
	return BootstrapResult{
		Created:  true,
		Username: "admin",
		Password: password,
		FilePath: filePath,
		UserID:   user.ID,
		RootID:   root.ID,
	}, nil
}

// User loads a user by id for /auth/me.
func (s *Service) User(ctx context.Context, id int64) (domainidentity.User, error) {
	if id <= 0 {
		return domainidentity.User{}, apperr.ErrUnauthenticated
	}
	return s.db.UserByID(ctx, id)
}
