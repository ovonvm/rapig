// Package config holds runtime configuration for the rapig binary.
//
// M0 deliberately keeps configuration to flags plus RAPIG_* environment
// variables: no config file format is frozen yet (see docs/07-known-issues.md).
// M1 adds the low/high memory profile, an optional loopback-only pprof
// listener and an explicit cookie Secure policy (see docs/09-m1-plan.md §3).
package config

import (
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Profile names. low targets a 512 MB machine; high is for 4 GB+ hosts.
const (
	ProfileLow  = "low"
	ProfileHigh = "high"

	// WorkerInternal runs the worker loop inside `serve` (default, single
	// process); WorkerExternal is a separate `rapig worker` process.
	WorkerInternal = "internal"
	WorkerExternal = "external"
)

// CookieSecurePolicy decides whether auth cookies carry the Secure flag.
// auto follows the request TLS state; always is required behind a TLS
// terminating reverse proxy; never is for plain HTTP development.
const (
	CookieSecureAuto   = "auto"
	CookieSecureAlways = "always"
	CookieSecureNever  = "never"
)

// Config is the resolved runtime configuration.
type Config struct {
	// Addr is the HTTP listen address, e.g. "127.0.0.1:50011".
	Addr string
	// DataDir holds the database, object storage and bootstrap material.
	DataDir string
	// AllowOrigins lists extra browser origins allowed to send mutating
	// requests. The dev Vite server (http://127.0.0.1:5173) needs one entry;
	// in production same-origin requests are always accepted.
	AllowOrigins []string
	// LogLevel is one of debug|info|warn|error.
	LogLevel string
	// Profile is low|high and selects the memory budget.
	Profile string
	// PProf is an optional loopback listen address for net/http/pprof.
	PProf string
	// CookieSecure is auto|always|never and drives the Secure cookie flag.
	CookieSecure string
	// WorkerMode is internal|external. internal (default) runs the worker loop
	// inside `serve`; external leaves it to a separate `rapig worker` process.
	WorkerMode string
	// AllowPrivateDownloads permits offline downloads from private/loopback
	// addresses (unsafe; only for trusted LAN sources and acceptance tests).
	AllowPrivateDownloads bool
	// QBURL is the qBittorrent WebUI base URL (for example
	// "http://127.0.0.1:8080"). An empty value disables qBittorrent offline
	// downloads.
	QBURL string
	// QBUsername is the qBittorrent WebUI user. Credentials live in memory
	// only and are never persisted or logged.
	QBUsername string
	// QBPassword is the qBittorrent WebUI password.
	QBPassword string
}

// Budget is the concrete memory/IO envelope derived from Profile. It is the
// single place where low/high numbers live so docs and tests can assert them.
type Budget struct {
	Profile             string
	DBMaxConns          int
	DBCacheKiB          int
	GOGC                int
	MemLimitBytes       int64
	UploadConcurrency   int
	DownloadConcurrency int
	CopyBufferBytes     int
}

// Budget returns the preset for the configured profile.
func (c Config) Budget() Budget {
	if c.Profile == ProfileHigh {
		return Budget{
			Profile:             ProfileHigh,
			DBMaxConns:          8,
			DBCacheKiB:          65536,
			GOGC:                100,
			MemLimitBytes:       3 << 30, // 3 GiB
			UploadConcurrency:   8,
			DownloadConcurrency: 4,
			CopyBufferBytes:     64 << 10,
		}
	}
	return Budget{
		Profile:             ProfileLow,
		DBMaxConns:          2,
		DBCacheKiB:          8192,
		GOGC:                50,
		MemLimitBytes:       280 << 20, // 280 MiB
		UploadConcurrency:   2,
		DownloadConcurrency: 2,
		CopyBufferBytes:     64 << 10,
	}
}

type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }

func (l *stringList) Set(v string) error {
	for _, p := range strings.Split(v, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			*l = append(*l, p)
		}
	}
	return nil
}

// DefaultDataDir returns <executable dir>/data. The user chose this layout on
// 2026-10-27: the data directory always sits next to the binary, e.g.
// /var/www/pan/rapig -> /var/www/pan/data, D:\app\rapig.exe -> D:\app\data.
// Keeping it relative to the binary (not the current working directory) means
// a systemd unit or a double-clicked exe resolves the same data every time.
func DefaultDataDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "data"
	}
	if abs, err := filepath.Abs(exe); err == nil {
		exe = abs
	}
	return filepath.Join(filepath.Dir(exe), "data")
}

// Load resolves configuration from environment defaults and then flags.
// args must not include the program name or the subcommand.
func Load(args []string) (Config, error) {
	c := Config{
		Addr:                  envOr("RAPIG_ADDR", "127.0.0.1:50011"),
		DataDir:               envOr("RAPIG_DATA_DIR", DefaultDataDir()),
		LogLevel:              envOr("RAPIG_LOG_LEVEL", "info"),
		Profile:               envOr("RAPIG_PROFILE", ProfileLow),
		PProf:                 envOr("RAPIG_PPROF", ""),
		CookieSecure:          envOr("RAPIG_COOKIE_SECURE", CookieSecureAuto),
		WorkerMode:            envOr("RAPIG_WORKER", WorkerInternal),
		AllowPrivateDownloads: envBool("RAPIG_ALLOW_PRIVATE_DOWNLOADS"),
		QBURL:                 envOr("RAPIG_QB_URL", ""),
		QBUsername:            os.Getenv("RAPIG_QB_USERNAME"),
		QBPassword:            os.Getenv("RAPIG_QB_PASSWORD"),
	}
	var origins stringList
	if v := os.Getenv("RAPIG_ALLOW_ORIGINS"); v != "" {
		if err := origins.Set(v); err != nil {
			return Config{}, err
		}
	}

	fs := flag.NewFlagSet("rapig", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&c.Addr, "addr", c.Addr, "HTTP listen address")
	fs.StringVar(&c.DataDir, "data", c.DataDir, "data directory")
	fs.StringVar(&c.LogLevel, "log-level", c.LogLevel, "debug|info|warn|error")
	fs.Var(&origins, "allow-origin", "extra allowed browser origin (repeatable / comma separated)")
	fs.StringVar(&c.Profile, "profile", c.Profile, "low|high memory profile")
	fs.StringVar(&c.PProf, "pprof", c.PProf, "loopback address for pprof (empty disables)")
	fs.StringVar(&c.CookieSecure, "cookie-secure", c.CookieSecure, "auto|always|never")
	fs.StringVar(&c.WorkerMode, "worker", c.WorkerMode, "internal|external background worker")
	fs.BoolVar(&c.AllowPrivateDownloads, "allow-private-downloads", c.AllowPrivateDownloads, "allow offline downloads from private addresses (unsafe)")
	fs.StringVar(&c.QBURL, "qb-url", c.QBURL, "qBittorrent WebUI base URL (empty disables offline torrents)")
	fs.StringVar(&c.QBUsername, "qb-username", c.QBUsername, "qBittorrent WebUI username")
	fs.Var(secretFlag{&c.QBPassword}, "qb-password", "qBittorrent WebUI password")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	c.AllowOrigins = append(c.AllowOrigins, origins...)
	c.Profile = strings.ToLower(strings.TrimSpace(c.Profile))
	c.CookieSecure = strings.ToLower(strings.TrimSpace(c.CookieSecure))
	c.WorkerMode = strings.ToLower(strings.TrimSpace(c.WorkerMode))
	c.QBURL = strings.TrimSpace(c.QBURL)
	return c, c.validate()
}

// secretFlag hides its value from flag defaults and error messages so a
// password supplied as a flag is never echoed by -h.
type secretFlag struct{ target *string }

func (s secretFlag) String() string { return "" }

func (s secretFlag) Set(v string) error {
	*s.target = v
	return nil
}

func (c Config) validate() error {
	if strings.TrimSpace(c.Addr) == "" {
		return fmt.Errorf("config: -addr must not be empty")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("config: -data must not be empty")
	}
	switch c.Profile {
	case ProfileLow, ProfileHigh:
	default:
		return fmt.Errorf("config: -profile must be low or high, got %q", c.Profile)
	}
	switch c.CookieSecure {
	case CookieSecureAuto, CookieSecureAlways, CookieSecureNever:
	default:
		return fmt.Errorf("config: -cookie-secure must be auto, always or never, got %q", c.CookieSecure)
	}
	switch c.WorkerMode {
	case WorkerInternal, WorkerExternal:
	default:
		return fmt.Errorf("config: -worker must be internal or external, got %q", c.WorkerMode)
	}
	return validateLoopback(c.PProf)
}

// envBool reads a boolean env var (1/true/yes/on).
func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// validateLoopback rejects pprof listeners that are reachable off the machine.
// An empty address disables pprof.
func validateLoopback(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("config: -pprof must be host:port: %w", err)
	}
	if port == "" {
		return fmt.Errorf("config: -pprof must include a port")
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("config: -pprof port %q is invalid", port)
	}
	if host == "" {
		return fmt.Errorf("config: -pprof must bind a loopback address, empty host is rejected")
	}
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return nil
	}
	return fmt.Errorf("config: -pprof must bind a loopback address, got %q", host)
}

// DBDir is where the SQLite database lives.
func (c Config) DBDir() string { return filepath.Join(c.DataDir, "db") }

// DBPath is the SQLite database file.
func (c Config) DBPath() string { return filepath.Join(c.DBDir(), "rapig.db") }

// StorageDir is the root of content addressed blob storage.
func (c Config) StorageDir() string { return filepath.Join(c.DataDir, "storage") }

// BootstrapDir holds one-shot secrets such as the initial admin password.
func (c Config) BootstrapDir() string { return filepath.Join(c.DataDir, "bootstrap") }

// LogDir is reserved for file logging; M0 logs to stdout only.
func (c Config) LogDir() string { return filepath.Join(c.DataDir, "logs") }

// EnsureDirs creates the data directories with restrictive permissions for
// bootstrap material.
func (c Config) EnsureDirs() error {
	dirs := []struct {
		path string
		mode os.FileMode
	}{
		{c.DBDir(), 0o755},
		{c.StorageDir(), 0o755},
		{c.BootstrapDir(), 0o700},
		{c.LogDir(), 0o755},
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d.path, d.mode); err != nil {
			return fmt.Errorf("config: create %s: %w", d.path, err)
		}
	}
	return nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
