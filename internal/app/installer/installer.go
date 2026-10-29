// Package installer implements the Linux install/upgrade/service operations
// behind the `rapig install|uninstall|service|config|gen-unit|upgrade`
// subcommands and the interactive ops menu (M6.4).
//
// Everything here is Linux specific by nature (systemd, useradd); the CLI
// refuses to run these commands on other platforms. The functions are small
// and synchronous so the menu and the command line share one implementation.
package installer

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Options carry the install-time choices. Defaults are filled by Defaults.
type Options struct {
	Prefix     string
	User       string
	Port       string
	DataDir    string
	BinaryPath string

	DryRun        bool
	NoService     bool
	NoCredentials bool
	Yes           bool

	Stdout io.Writer
	Stderr io.Writer
}

// Credentials is the first administrator account created on the first start.
type Credentials struct {
	Username string
	Password string
	FilePath string
}

// Defaults returns the documented defaults: /opt/rapig, user rapig, port 50011.
func Defaults() Options {
	return Options{
		Prefix: "/opt/rapig",
		User:   "rapig",
		Port:   "50011",
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
}

func (o *Options) normalize() {
	if o.Prefix == "" {
		o.Prefix = "/opt/rapig"
	}
	if o.User == "" {
		o.User = "rapig"
	}
	if o.Port == "" {
		o.Port = "50011"
	}
	if o.DataDir == "" {
		o.DataDir = filepath.Join(o.Prefix, "data")
	}
	if o.Stdout == nil {
		o.Stdout = os.Stdout
	}
	if o.Stderr == nil {
		o.Stderr = os.Stderr
	}
}

func (o Options) addr() string    { return "127.0.0.1:" + o.Port }
func (o Options) envPath() string { return filepath.Join(o.Prefix, "rapig.env") }
func (o Options) unitPath() string {
	return filepath.Join("/etc/systemd/system", "rapig.service")
}

func (o Options) printf(format string, args ...any) {
	fmt.Fprintf(o.Stdout, format+"\n", args...)
}

func (o Options) logf(format string, args ...any) {
	fmt.Fprintf(o.Stdout, "[rapig] "+format+"\n", args...)
}

// Installed reports whether prefix contains an installed binary and unit.
func Installed(prefix string) bool {
	_, err := os.Stat(filepath.Join(prefix, "rapig"))
	return err == nil
}

// Install copies the binary, writes config and unit, starts systemd and waits
// for /health. It returns the bootstrap credentials when the service created
// them (first install on fresh data).
func Install(ctx context.Context, opts Options) (Credentials, error) {
	opts.normalize()
	src := opts.BinaryPath
	if src == "" {
		exe, err := os.Executable()
		if err != nil {
			return Credentials{}, fmt.Errorf("cannot locate own binary: %w", err)
		}
		src = exe
	}
	if fi, err := os.Stat(src); err != nil || fi.IsDir() {
		return Credentials{}, fmt.Errorf("source binary %q is not usable: %v", src, err)
	}
	dst := filepath.Join(opts.Prefix, "rapig")
	if _, err := os.Stat(dst); err == nil && !opts.DryRun {
		if pid, running := runningFromBinary(dst); running {
			return Credentials{}, fmt.Errorf("refusing to overwrite %s while it is running (pid %d); stop the service or use `rapig upgrade`", dst, pid)
		}
	}

	steps := []string{
		fmt.Sprintf("create system user %s", opts.User),
		fmt.Sprintf("prepare %s and %s", opts.Prefix, opts.DataDir),
		fmt.Sprintf("install binary to %s", dst),
		fmt.Sprintf("copy operator scripts to %s/scripts", opts.Prefix),
		fmt.Sprintf("write %s", opts.envPath()),
		fmt.Sprintf("write %s", opts.unitPath()),
		"install symlink /usr/local/bin/rapig",
	}
	if !opts.NoService {
		steps = append(steps, "systemctl daemon-reload && enable --now rapig")
	}
	if opts.DryRun {
		opts.printf("would perform:")
		for _, s := range steps {
			opts.printf("  - %s", s)
		}
		return Credentials{}, nil
	}

	if !IsRoot() {
		return Credentials{}, fmt.Errorf("install needs root; run: sudo rapig install")
	}

	if err := ensureUser(opts.User, opts.Prefix); err != nil {
		return Credentials{}, err
	}
	for _, dir := range []string{opts.Prefix, opts.DataDir, filepath.Join(opts.Prefix, "scripts")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return Credentials{}, fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := os.Chmod(opts.DataDir, 0o700); err != nil {
		return Credentials{}, err
	}
	if err := chownTo(opts.DataDir, opts.User); err != nil {
		return Credentials{}, err
	}

	if err := copyFile(src, dst, 0o755); err != nil {
		return Credentials{}, fmt.Errorf("install binary: %w", err)
	}
	if err := chownTo(dst, opts.User); err != nil {
		return Credentials{}, err
	}
	opts.logf("installed binary %s", dst)

	// 安装随包脚本（backup/restore/healthcheck），便于 cron 与菜单直接调用。
	pkgDir := filepath.Dir(src)
	for _, name := range []string{"backup.sh", "restore.sh", "healthcheck.sh"} {
		from := filepath.Join(pkgDir, "scripts", name)
		to := filepath.Join(opts.Prefix, "scripts", name)
		if _, err := os.Stat(from); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Credentials{}, fmt.Errorf("inspect %s: %w", from, err)
		}
		if err := copyFile(from, to, 0o755); err != nil {
			return Credentials{}, fmt.Errorf("install %s: %w", name, err)
		}
		if err := chownTo(to, opts.User); err != nil {
			return Credentials{}, err
		}
	}
	if from := filepath.Join(pkgDir, "nginx-rapig.conf"); fileExists(from) {
		to := filepath.Join(opts.Prefix, "nginx-rapig.conf")
		if err := copyFile(from, to, 0o644); err != nil {
			return Credentials{}, fmt.Errorf("install nginx sample: %w", err)
		}
	}

	if _, err := os.Stat(opts.envPath()); os.IsNotExist(err) {
		if err := os.WriteFile(opts.envPath(), []byte(envFile(opts)), 0o600); err != nil {
			return Credentials{}, fmt.Errorf("write env file: %w", err)
		}
	}
	opts.logf("configuration %s", opts.envPath())

	if err := os.WriteFile(opts.unitPath(), []byte(GenUnit(opts.Prefix, opts.User, opts.Port, opts.DataDir)), 0o644); err != nil {
		return Credentials{}, fmt.Errorf("write systemd unit: %w", err)
	}
	opts.logf("systemd unit %s", opts.unitPath())

	if err := os.Symlink(dst, "/usr/local/bin/rapig"); err != nil && !os.IsExist(err) {
		return Credentials{}, fmt.Errorf("install /usr/local/bin/rapig: %w", err)
	}

	if !opts.NoService {
		if err := systemctl("daemon-reload"); err != nil {
			return Credentials{}, err
		}
		if err := systemctl("enable", "--now", "rapig"); err != nil {
			_ = systemctl("disable", "--now", "rapig")
			return Credentials{}, fmt.Errorf("%w (see: journalctl -u rapig -n 50)", err)
		}
		if err := waitHealthy(ctx, opts.addr(), 20*time.Second); err != nil {
			_ = systemctl("disable", "--now", "rapig")
			return Credentials{}, fmt.Errorf("service started but /health did not answer: %w", err)
		}
		opts.logf("service is running at http://%s", opts.addr())
	}

	creds := Credentials{}
	if data, err := os.ReadFile(filepath.Join(opts.DataDir, "bootstrap", "initial-admin.txt")); err == nil {
		creds = parseCredentials(string(data), filepath.Join(opts.DataDir, "bootstrap", "initial-admin.txt"))
	}

	opts.printf("")
	opts.printf("installed:")
	opts.printf("  address:   http://%s", opts.addr())
	opts.printf("  data dir:  %s", opts.DataDir)
	opts.printf("  config:    %s", opts.envPath())
	opts.printf("  command:   /usr/local/bin/rapig")
	if creds.Username != "" && !opts.NoCredentials {
		opts.printf("  admin:     %s / %s", creds.Username, creds.Password)
		opts.printf("             (also in %s; it is removed after the first password change)", creds.FilePath)
	} else if opts.NoCredentials {
		opts.printf("  admin:     %s (initial password in %s; not printed because --no-credentials)", "admin",
			filepath.Join(opts.DataDir, "bootstrap", "initial-admin.txt"))
	} else {
		opts.printf("  admin:     admin (initial password file not found; check %s)",
			filepath.Join(opts.DataDir, "bootstrap", "initial-admin.txt"))
	}
	opts.printf("")
	opts.printf("next: rapig doctor && rapig (interactive menu)")
	return creds, nil
}

// Uninstall removes the service, the symlink, the prefix (including data) and
// the system user.
func Uninstall(ctx context.Context, opts Options) error {
	opts.normalize()
	if !IsRoot() {
		return fmt.Errorf("uninstall needs root; run: sudo rapig uninstall")
	}
	if opts.DryRun {
		opts.printf("would remove: unit %s, symlink /usr/local/bin/rapig, prefix %s (with data), user %s",
			opts.unitPath(), opts.Prefix, opts.User)
		return nil
	}
	_ = systemctl("disable", "--now", "rapig")
	_ = os.Remove(opts.unitPath())
	_ = systemctl("daemon-reload")
	_ = os.Remove("/usr/local/bin/rapig")
	if err := os.RemoveAll(opts.Prefix); err != nil {
		return fmt.Errorf("remove %s: %w", opts.Prefix, err)
	}
	if err := removeUser(opts.User); err != nil {
		opts.printf("note: system user %s not removed: %v", opts.User, err)
	}
	opts.printf("uninstalled rapig (prefix %s, user %s)", opts.Prefix, opts.User)
	return nil
}

// Service runs `systemctl <action> rapig` and returns the state.
func Service(action string) error {
	switch action {
	case "status", "start", "stop", "restart":
	default:
		return fmt.Errorf("service: unknown action %q (status|start|stop|restart)", action)
	}
	if err := systemctl(action, "rapig"); err != nil {
		return err
	}
	return nil
}

// ServiceStatus returns the one-word systemd state, or "" when unavailable.
func ServiceStatus() string {
	out, err := exec.Command("systemctl", "is-active", "rapig").Output()
	state := strings.TrimSpace(string(out))
	if err != nil && state == "" {
		return "unknown"
	}
	return state
}

// GenUnit renders the systemd unit for the given layout.
func GenUnit(prefix, username, port, dataDir string) string {
	if dataDir == "" {
		dataDir = filepath.Join(prefix, "data")
	}
	return fmt.Sprintf(`[Unit]
Description=Rapig cloud drive
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=%s
EnvironmentFile=%s
ExecStart=%s serve
Restart=on-failure
RestartSec=3
TimeoutStopSec=20
LimitNOFILE=65536

NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=%s

MemoryMax=512M
MemorySwapMax=0
CPUQuota=100%%

StandardOutput=journal
StandardError=journal
SyslogIdentifier=rapig

[Install]
WantedBy=multi-user.target
`, username, username, prefix, filepath.Join(prefix, "rapig.env"), filepath.Join(prefix, "rapig"), prefix)
}

func envFile(opts Options) string {
	return fmt.Sprintf(`# Rapig runtime configuration (read by the systemd unit).
RAPIG_ADDR=%s
RAPIG_DATA_DIR=%s
RAPIG_PROFILE=low
RAPIG_COOKIE_SECURE=auto
`, opts.addr(), opts.DataDir)
}

// ConfigShow prints the current environment file.
func ConfigShow(prefix string, w io.Writer) error {
	path := filepath.Join(prefix, "rapig.env")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

// ConfigEdit interactively updates the well-known keys of the env file.
func ConfigEdit(prefix string, in io.Reader, out io.Writer) error {
	path := filepath.Join(prefix, "rapig.env")
	values := readEnvFile(path)
	defaults := []struct{ key, label string }{
		{"RAPIG_ADDR", "listen address"},
		{"RAPIG_DATA_DIR", "data directory"},
		{"RAPIG_PROFILE", "profile (low|high)"},
		{"RAPIG_COOKIE_SECURE", "cookie secure (auto|always|never)"},
	}
	scanner := bufio.NewScanner(in)
	for _, d := range defaults {
		current := values[d.key]
		fmt.Fprintf(out, "%s [%s]: ", d.label, current)
		if !scanner.Scan() {
			break
		}
		if v := strings.TrimSpace(scanner.Text()); v != "" {
			values[d.key] = v
		}
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("# Rapig runtime configuration (read by the systemd unit).\n")
	for _, k := range keys {
		b.WriteString(k + "=" + values[k] + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(out, "saved %s; restart the service for the change to take effect\n", path)
	return nil
}

// Upgrade replaces the installed binary, restarts the service and rolls back
// when the new binary does not become healthy.
func Upgrade(ctx context.Context, prefix, newBinary string, port string) error {
	if !IsRoot() {
		return fmt.Errorf("upgrade needs root; run: sudo rapig upgrade --binary <path>")
	}
	if port == "" {
		port = "50011"
	}
	target := filepath.Join(prefix, "rapig")
	if _, err := os.Stat(newBinary); err != nil {
		return fmt.Errorf("new binary %q: %w", newBinary, err)
	}
	if err := systemctl("stop", "rapig"); err != nil {
		return err
	}
	backup := target + ".bak-" + time.Now().UTC().Format("20060102-150405")
	if err := os.Rename(target, backup); err != nil {
		return fmt.Errorf("backup current binary: %w", err)
	}
	restore := func() {
		_ = os.Rename(backup, target)
		_ = systemctl("start", "rapig")
	}
	if err := copyFile(newBinary, target, 0o755); err != nil {
		restore()
		return fmt.Errorf("install new binary: %w", err)
	}
	if err := systemctl("start", "rapig"); err != nil {
		restore()
		return fmt.Errorf("start with new binary failed: %w", err)
	}
	if err := waitHealthy(ctx, "127.0.0.1:"+port, 20*time.Second); err != nil {
		restore()
		return fmt.Errorf("new binary unhealthy, rolled back: %w", err)
	}
	return nil
}

// ResetAdminPassword sets a new password for an account while the service is
// stopped, then revokes all sessions. It is used by the ops menu.
func ResetAdminPassword(ctx context.Context, dataDir, dbPath, username, password string) error {
	if len(password) < 8 {
		return fmt.Errorf("password must be at least 8 characters")
	}
	_ = dbPath
	return resetAdminPassword(ctx, dataDir, username, password)
}

func systemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func waitHealthy(ctx context.Context, addr string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	url := "http://" + addr + "/health"
	var last error
	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			last = fmt.Errorf("health returned %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timeout")
	}
	return last
}

func ensureUser(username, home string) error {
	if _, err := user.Lookup(username); err == nil {
		return nil
	}
	cmd := exec.Command("useradd", "--system", "--home", home, "--shell", "/usr/sbin/nologin", username)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create system user: %s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func removeUser(username string) error {
	cmd := exec.Command("userdel", username)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func chownTo(path, username string) error {
	u, err := user.Lookup(username)
	if err != nil {
		return fmt.Errorf("lookup user %s: %w", username, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("chown %s: %w", path, err)
	}
	return nil
}

func runningFromBinary(path string) (int, bool) {
	out, err := exec.Command("pgrep", "-f", path).Output()
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(out))
	for _, f := range fields {
		if pid, err := strconv.Atoi(f); err == nil && pid != os.Getpid() {
			return pid, true
		}
	}
	return 0, false
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp-install"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func readEnvFile(path string) map[string]string {
	values := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return values
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			values[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return values
}

func parseCredentials(content, path string) Credentials {
	c := Credentials{FilePath: path}
	for _, line := range strings.Split(content, "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			switch strings.TrimSpace(k) {
			case "username":
				c.Username = strings.TrimSpace(v)
			case "password":
				c.Password = strings.TrimSpace(v)
			}
		}
	}
	return c
}
