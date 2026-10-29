package winapp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	domainidentity "rapig/internal/domain/identity"
	sqlstore "rapig/internal/infra/sql"
	"rapig/internal/platform/config"
	"rapig/internal/platform/passwd"
	"rapig/migrations"
)

// InitAdmin creates the initial administrator when the database is empty.
//
// It is used by the Windows first-run wizard and by the installer
// (`rapig init-admin --password-stdin`). When users already exist it does
// nothing and returns created=false, so the installer can call it safely.
func InitAdmin(ctx context.Context, cfg config.Config, username, password string) (created bool, err error) {
	if username == "" {
		username = "admin"
	}
	if password == "" {
		return false, fmt.Errorf("administrator password must not be empty")
	}
	if len(password) < 8 {
		return false, fmt.Errorf("administrator password must be at least 8 characters")
	}
	if err := cfg.EnsureDirs(); err != nil {
		return false, err
	}
	db, err := openMigrated(ctx, cfg)
	if err != nil {
		return false, err
	}
	defer db.Close()

	n, err := db.CountUsers(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	policyID, err := db.EnsureDefaultStoragePolicy(ctx, cfg.StorageDir())
	if err != nil {
		return false, err
	}
	roleID, err := db.RoleIDByCode(ctx, "admin")
	if err != nil {
		return false, err
	}
	hash, err := passwd.Hash(password)
	if err != nil {
		return false, err
	}
	user, err := db.CreateUser(ctx, domainidentity.User{
		Username:       username,
		UsernameFolded: domainidentity.FoldUsername(username),
		DisplayName:    "管理员",
		PasswordHash:   hash,
		RoleID:         roleID,
		Status:         domainidentity.StatusActive,
		IsRoot:         true,
	})
	if err != nil {
		return false, err
	}
	if _, err := db.EnsureRoot(ctx, user.ID, policyID); err != nil {
		return false, err
	}
	// Keep the same bootstrap file the web first start writes; it is removed
	// after the first password change.
	dir := cfg.BootstrapDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, err
	}
	content := fmt.Sprintf("username: %s\npassword: %s\ncreated_at: %s\n",
		username, password, time.Now().UTC().Format(time.RFC3339))
	path := filepath.Join(dir, "initial-admin.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return false, err
	}
	fmt.Printf("administrator %q created (password in %s; removed after first change)\n", username, path)
	return true, nil
}

// EnsureAdminForLaunch creates an administrator on an empty database using the
// password read from stdin (first-run wizard). It returns false when an admin
// already exists.
func EnsureAdminForLaunch(ctx context.Context, cfg config.Config, in *os.File) (bool, error) {
	db, err := openMigrated(ctx, cfg)
	if err != nil {
		return false, err
	}
	n, err := db.CountUsers(ctx)
	db.Close()
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	fmt.Println("首次运行：请设置管理员密码（至少 8 个字符，输入不回显）。")
	password, err := readPasswordTwice(in)
	if err != nil {
		return false, err
	}
	return InitAdmin(ctx, cfg, "admin", password)
}

// openMigrated opens the database and applies migrations; the Windows wizard
// and installer run before `rapig serve` ever created the schema.
func openMigrated(ctx context.Context, cfg config.Config) (*sqlstore.DB, error) {
	db, err := sqlstore.OpenWithOptions(ctx, cfg.DBPath(), sqlstore.Options{})
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err := db.Migrate(ctx, migrations.FS); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}

func readPasswordTwice(in *os.File) (string, error) {
	for {
		fmt.Print("管理员密码: ")
		first, err := readLine(in)
		if err != nil {
			return "", err
		}
		fmt.Print("再输入一次: ")
		second, err := readLine(in)
		if err != nil {
			return "", err
		}
		if first != second {
			fmt.Println("两次输入不一致，请重试。")
			continue
		}
		if len(first) < 8 {
			fmt.Println("密码至少 8 个字符，请重试。")
			continue
		}
		return first, nil
	}
}

func readLine(in *os.File) (string, error) {
	var buf []byte
	one := make([]byte, 1)
	for {
		n, err := in.Read(one)
		if n == 0 && err != nil {
			return "", err
		}
		if one[0] == '\n' {
			break
		}
		if one[0] != '\r' {
			buf = append(buf, one[0])
		}
		if len(buf) > 1024 {
			return "", fmt.Errorf("input too long")
		}
	}
	return strings.TrimSpace(string(buf)), nil
}
