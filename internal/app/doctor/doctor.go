// Package doctor implements the offline, read-only self check behind
// `rapig doctor` (M6.4).
//
// The command never mutates the installation: the database is opened in
// read-only mode, storage probes are deleted immediately, and the configured
// address is only test-bound for a moment. Every check keeps running even when
// an earlier one fails, so a single report explains everything that is wrong.
package doctor

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"rapig/internal/platform/version"
	"rapig/migrations"
)

// Status is one check outcome.
type Status string

const (
	StatusOK   Status = "ok"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
	StatusInfo Status = "info"
)

// Check is a single diagnostic result.
type Check struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail"`
}

// Summary counts the checks by status.
type Summary struct {
	OK   int `json:"ok"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

// Report is the full doctor result. OK is true when no check failed.
type Report struct {
	OK      bool    `json:"ok"`
	Checks  []Check `json:"checks"`
	Summary Summary `json:"summary"`
}

// Options are the local facts doctor inspects. They come from the same config
// the service uses, so the check reflects the real deployment.
type Options struct {
	Addr                  string
	DataDir               string
	BackupDir             string // optional override; empty means <prefix>/backup
	LogLevel              string
	Profile               string
	CookieSecure          string
	AllowPrivateDownloads bool
	QBConfigured          bool
	Prefix                string // install prefix when installed; empty in portable mode
}

// Run executes every check and returns the report.
func Run(ctx context.Context, opts Options) Report {
	var checks []Check
	add := func(name string, status Status, detail string) {
		checks = append(checks, Check{Name: name, Status: status, Detail: detail})
	}

	info := version.Current()
	add("version", StatusInfo, fmt.Sprintf("%s (%s, %s)", info.Version, info.Platform, info.GoVersion))

	// 2. data directory
	if fi, err := os.Stat(opts.DataDir); err != nil {
		if os.IsNotExist(err) {
			add("data dir", StatusWarn, fmt.Sprintf("%s does not exist yet (the first start creates it)", opts.DataDir))
		} else {
			add("data dir", StatusFail, fmt.Sprintf("%s: %v", opts.DataDir, err))
		}
	} else if !fi.IsDir() {
		add("data dir", StatusFail, fmt.Sprintf("%s is not a directory", opts.DataDir))
	} else {
		if err := probeWritable(opts.DataDir); err != nil {
			add("data dir", StatusFail, fmt.Sprintf("%s is not writable: %v", opts.DataDir, err))
		} else {
			detail := fmt.Sprintf("%s (writable)", opts.DataDir)
			if free, err := freeBytes(opts.DataDir); err == nil {
				detail += fmt.Sprintf(", %s free", humanBytes(free))
			}
			add("data dir", StatusOK, detail)
			// 3. disk space (only meaningful when the directory exists)
			if free, err := freeBytes(opts.DataDir); err == nil {
				switch {
				case free < 200<<20:
					add("disk space", StatusFail, fmt.Sprintf("%s free (< 200 MiB)", humanBytes(free)))
				case free < 1<<30:
					add("disk space", StatusWarn, fmt.Sprintf("%s free (< 1 GiB)", humanBytes(free)))
				default:
					add("disk space", StatusOK, humanBytes(free)+" free")
				}
			}
		}
	}

	// 4/5. database and migrations
	dbPath := filepath.Join(opts.DataDir, "db", "rapig.db")
	if !fileExists(dbPath) {
		add("database", StatusWarn, "not created yet (the first start creates it)")
		add("migrations", StatusWarn, "not created yet")
	} else if db, err := openReadOnly(dbPath); err != nil {
		add("database", StatusFail, err.Error())
		add("migrations", StatusFail, "cannot read migrations without the database")
	} else {
		defer db.Close()
		var quick string
		if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quick); err != nil {
			add("database", StatusFail, fmt.Sprintf("quick_check failed: %v", err))
		} else if quick != "ok" {
			add("database", StatusFail, "quick_check: "+quick)
		} else {
			add("database", StatusOK, "quick_check ok")
		}
		embedded := countMigrations()
		var applied int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&applied); err != nil {
			add("migrations", StatusWarn, "schema_migrations not readable: "+err.Error())
		} else if applied < embedded {
			add("migrations", StatusFail, fmt.Sprintf("%d/%d applied; run `rapig migrate` or start the service once", applied, embedded))
		} else {
			add("migrations", StatusOK, fmt.Sprintf("%d/%d applied", applied, embedded))
		}
		// 6. storage policies
		checks = append(checks, storageChecks(ctx, db)...)
	}

	// 7. address
	if ln, err := net.Listen("tcp", opts.Addr); err != nil {
		add("address", StatusWarn, fmt.Sprintf("%s is in use (is rapig already running?)", opts.Addr))
	} else {
		_ = ln.Close()
		add("address", StatusOK, opts.Addr+" is free")
	}

	// 8. configuration risks
	checks = append(checks, configStatus(opts)...)

	// 9. backup age
	checks = append(checks, backupCheck(opts)...)

	// 10. service state (Linux systemd; Windows has no service concept here)
	if runtime.GOOS == "linux" {
		sts, detail := systemdStatus()
		add("service", sts, detail)
	}

	report := Report{Checks: checks, OK: true}
	for _, c := range checks {
		switch c.Status {
		case StatusFail:
			report.OK = false
			report.Summary.Fail++
		case StatusWarn:
			report.Summary.Warn++
		case StatusOK:
			report.Summary.OK++
		}
	}
	return report
}

// WriteText renders the human readable report.
func WriteText(w io.Writer, r Report) {
	width := 0
	for _, c := range r.Checks {
		if len(c.Name) > width {
			width = len(c.Name)
		}
	}
	for _, c := range r.Checks {
		status := "ok  "
		switch c.Status {
		case StatusWarn:
			status = "warn"
		case StatusFail:
			status = "FAIL"
		case StatusInfo:
			status = "info"
		}
		fmt.Fprintf(w, "[%s] %-*s  %s\n", status, width, c.Name, c.Detail)
	}
	fails, warns := r.Summary.Fail, r.Summary.Warn
	switch {
	case fails > 0:
		fmt.Fprintf(w, "[FAIL] doctor         %d failure(s), %d warning(s)\n", fails, warns)
	case warns > 0:
		fmt.Fprintf(w, "[ok]   doctor         no failures; %d warning(s)\n", warns)
	default:
		fmt.Fprintf(w, "[ok]   doctor         no failures, no warnings\n")
	}
}

// WriteJSON renders the machine readable report.
func WriteJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

func configStatus(opts Options) []Check {
	var out []Check
	if opts.CookieSecure == "never" {
		out = append(out, Check{Name: "config", Status: StatusWarn, Detail: "cookie-secure=never; use always behind HTTPS"})
	}
	host, _, err := net.SplitHostPort(opts.Addr)
	if err == nil && !isLoopbackHost(host) {
		out = append(out, Check{Name: "config", Status: StatusWarn, Detail: fmt.Sprintf("listening on %s; keep it on loopback and use a reverse proxy", opts.Addr)})
	}
	if opts.AllowPrivateDownloads {
		out = append(out, Check{Name: "config", Status: StatusWarn, Detail: "allow-private-downloads is enabled (only for trusted LAN sources)"})
	}
	if opts.QBConfigured {
		out = append(out, Check{Name: "config", Status: StatusOK, Detail: "qBittorrent is configured"})
	}
	if len(out) == 0 {
		out = append(out, Check{Name: "config", Status: StatusOK, Detail: "no risky options detected"})
	}
	return out
}

func backupCheck(opts Options) []Check {
	dir := opts.BackupDir
	if dir == "" {
		parent := filepath.Dir(opts.DataDir)
		if opts.Prefix != "" {
			parent = opts.Prefix
		}
		dir = filepath.Join(parent, "backup")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []Check{{Name: "backup", Status: StatusWarn, Detail: fmt.Sprintf("no backup directory at %s", dir)}}
	}
	var newest time.Time
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return []Check{{Name: "backup", Status: StatusWarn, Detail: "no backups found"}}
	}
	age := time.Since(newest)
	if age > 7*24*time.Hour {
		return []Check{{Name: "backup", Status: StatusWarn, Detail: fmt.Sprintf("newest backup is %d days old", int(age.Hours()/24))}}
	}
	return []Check{{Name: "backup", Status: StatusOK, Detail: fmt.Sprintf("newest backup is %s old", humanDuration(age))}}
}

// storageChecks probes every enabled storage policy root.
func storageChecks(ctx context.Context, db *sql.DB) []Check {
	rows, err := db.QueryContext(ctx, "SELECT name, root_path, enabled FROM storage_policies ORDER BY id")
	if err != nil {
		return []Check{{Name: "storage", Status: StatusWarn, Detail: "storage policies not readable: " + err.Error()}}
	}
	defer rows.Close()
	var enabled, total int
	var failures []string
	for rows.Next() {
		var name, root string
		var isEnabled bool
		if err := rows.Scan(&name, &root, &isEnabled); err != nil {
			return []Check{{Name: "storage", Status: StatusWarn, Detail: "cannot read storage policy row: " + err.Error()}}
		}
		total++
		if !isEnabled {
			continue
		}
		enabled++
		if err := probeWritable(root); err != nil {
			failures = append(failures, fmt.Sprintf("%s (%s): %v", name, root, err))
		}
	}
	if err := rows.Err(); err != nil {
		return []Check{{Name: "storage", Status: StatusWarn, Detail: err.Error()}}
	}
	if total == 0 {
		return []Check{{Name: "storage", Status: StatusWarn, Detail: "no storage policies yet"}}
	}
	if len(failures) > 0 {
		return []Check{{Name: "storage", Status: StatusFail, Detail: strings.Join(failures, "; ")}}
	}
	return []Check{{Name: "storage", Status: StatusOK, Detail: fmt.Sprintf("%d policies, %d enabled, roots writable", total, enabled)}}
}

func openReadOnly(path string) (*sql.DB, error) {
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(2000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cannot open database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("cannot open database read-only: %w", err)
	}
	return db, nil
}

func countMigrations() int {
	entries, err := migrations.FS.ReadDir(".")
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			n++
		}
	}
	return n
}

func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".rapig-doctor-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_, werr := f.WriteString("ok")
	cerr := f.Close()
	_ = os.Remove(name)
	if werr != nil {
		return werr
	}
	return cerr
}

func systemdStatus() (Status, string) {
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return StatusInfo, "systemd not available (portable run?)"
	}
	out, err := exec.Command(systemctl, "is-active", "rapig").Output()
	state := strings.TrimSpace(string(out))
	if err != nil {
		if state == "" {
			return StatusInfo, "systemd unit rapig not installed"
		}
		return StatusWarn, "systemd unit rapig is " + state
	}
	if state == "active" {
		return StatusOK, "systemd unit rapig is active"
	}
	return StatusWarn, "systemd unit rapig is " + state
}

func isLoopbackHost(host string) bool {
	if host == "" || host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	suffix := "KMGTPE"[exp]
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), suffix)
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

// SortChecks is used by tests to compare reports independent of order.
func SortChecks(r *Report) {
	sort.Slice(r.Checks, func(i, j int) bool { return r.Checks[i].Name < r.Checks[j].Name })
}
