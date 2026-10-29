package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	stdhttp "net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	accountsvc "rapig/internal/app/account"
	adminsvc "rapig/internal/app/admin"
	doctorpkg "rapig/internal/app/doctor"
	filessvc "rapig/internal/app/files"
	groupssvc "rapig/internal/app/groups"
	idsvc "rapig/internal/app/identity"
	"rapig/internal/app/maintenance"
	sharesvc "rapig/internal/app/shares"
	storagemig "rapig/internal/app/storagemig"
	transfersvc "rapig/internal/app/transfers"
	upsvc "rapig/internal/app/uploads"
	"rapig/internal/infra/fetch"
	"rapig/internal/infra/qb"
	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/platform/config"
	"rapig/internal/platform/logx"
	"rapig/internal/platform/version"
	httpx "rapig/internal/transport/http"
	"rapig/migrations"
	"rapig/web"
)

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		if isTerminal() {
			return runMenuCLI(nil)
		}
		usage()
		return 1
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "version", "-version", "--version":
		fmt.Println(version.String())
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	case "serve", "worker", "migrate", "gc", "fsck", "storage", "doctor",
		"install", "uninstall", "service", "config", "gen-unit", "upgrade",
		"reset-admin-password", "menu",
		"launch", "init-admin", "autostart", "logs":
	default:
		fmt.Fprintf(os.Stderr, "rapig: unknown command %q\n\n", cmd)
		usage()
		return 2
	}

	var admin adminOptions
	if cmd == "gc" || cmd == "fsck" {
		rest, admin = splitAdminFlags(rest)
	}
	switch cmd {
	case "install":
		return runInstallCLI(rest)
	case "uninstall":
		if runtimeGOOS() == "windows" {
			return runUninstallWindowsCLI(rest)
		}
		return runUninstallCLI(rest)
	case "launch":
		return runLaunchCLI(rest)
	case "init-admin":
		return runInitAdminCLI(rest)
	case "autostart":
		return runAutostartCLI(rest)
	case "logs":
		return runLogsCLI(rest)
	case "service":
		if runtimeGOOS() == "windows" {
			return runWindowsServiceCLI(rest)
		}
		return runServiceCLI(rest)
	case "config":
		return runConfigCLI(rest)
	case "gen-unit":
		return runGenUnitCLI(rest)
	case "upgrade":
		return runUpgradeCLI(rest)
	case "reset-admin-password":
		return runResetAdminCLI(rest)
	case "menu":
		return runMenuCLI(rest)
	}

	var doctorOpts doctorOptions
	if cmd == "doctor" {
		rest, doctorOpts = splitDoctorFlags(rest)
	}
	var storageOpts storageMigrateOptions
	if cmd == "storage" {
		var err error
		rest, storageOpts, err = splitStorageMigrateFlags(rest)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rapig: %v\n\n", err)
			usage()
			return 2
		}
	}

	cfg, err := config.Load(rest)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rapig: %v\n", err)
		return 2
	}
	log := logx.New(cfg.LogLevel)

	if cmd == "doctor" {
		return runDoctor(cfg, doctorOpts)
	}

	if err := cfg.EnsureDirs(); err != nil {
		log.Error("cannot create data directories", "err", err, "data_dir", cfg.DataDir)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	applyBudget(cfg, log)

	budget := cfg.Budget()
	db, err := sqlstore.OpenWithOptions(ctx, cfg.DBPath(), sqlstore.Options{
		MaxConns: budget.DBMaxConns,
		CacheKiB: budget.DBCacheKiB,
	})
	if err != nil {
		log.Error("cannot open database", "err", err, "path", cfg.DBPath())
		return 1
	}
	defer db.Close()

	switch cmd {
	case "migrate":
		return runMigrate(ctx, db, log)
	case "worker":
		return runWorker(ctx, cfg, db, log)
	case "gc", "fsck":
		return runAdmin(ctx, cfg, db, log, cmd, admin)
	case "storage":
		return runStorageMigrate(ctx, cfg, db, log, storageOpts)
	default:
		return runServe(ctx, cfg, db, log)
	}
}

// doctorOptions are the flags of `rapig doctor`.
type doctorOptions struct {
	json bool
}

// splitDoctorFlags removes the doctor-only flags from args so the rest can be
// handed to the normal configuration parser.
func splitDoctorFlags(args []string) ([]string, doctorOptions) {
	var out []string
	var opts doctorOptions
	for _, a := range args {
		switch a {
		case "-json", "--json", "-json=true":
			opts.json = true
		default:
			out = append(out, a)
		}
	}
	return out, opts
}

// runDoctor performs the offline self check. It never creates the data
// directory or touches the database, so it is safe on a broken install.
func runDoctor(cfg config.Config, opts doctorOptions) int {
	dopts := doctorpkg.Options{
		Addr:                  cfg.Addr,
		DataDir:               cfg.DataDir,
		LogLevel:              cfg.LogLevel,
		Profile:               cfg.Profile,
		CookieSecure:          cfg.CookieSecure,
		AllowPrivateDownloads: cfg.AllowPrivateDownloads,
		QBConfigured:          cfg.QBURL != "",
	}
	if v := os.Getenv("RAPIG_BACKUP_DIR"); v != "" {
		dopts.BackupDir = v
	}
	if v := os.Getenv("RAPIG_PREFIX"); v != "" {
		dopts.Prefix = v
	} else if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if strings.HasSuffix(filepath.ToSlash(dir), "/opt/rapig") {
			dopts.Prefix = dir
		}
	}
	report := doctorpkg.Run(context.Background(), dopts)
	if opts.json {
		if err := doctorpkg.WriteJSON(os.Stdout, report); err != nil {
			fmt.Fprintf(os.Stderr, "rapig: %v\n", err)
			return 1
		}
	} else {
		doctorpkg.WriteText(os.Stdout, report)
	}
	if !report.OK {
		return 1
	}
	return 0
}

// adminOptions are the flags shared by the offline maintenance commands.
type adminOptions struct {
	dryRun        bool
	repair        bool
	verifyContent bool
}

// splitAdminFlags removes the gc/fsck-only flags from args so the rest can be
// handed to the normal configuration parser.
func splitAdminFlags(args []string) ([]string, adminOptions) {
	var out []string
	var opts adminOptions
	for _, a := range args {
		switch a {
		case "-dry-run", "--dry-run":
			opts.dryRun = true
		case "-repair", "--repair":
			opts.repair = true
		case "-verify-content", "--verify-content":
			opts.verifyContent = true
		default:
			out = append(out, a)
		}
	}
	return out, opts
}

// storageMigrateOptions are the flags of `rapig storage migrate`.
type storageMigrateOptions struct {
	from   string
	to     string
	dryRun bool
}

// splitStorageMigrateFlags consumes the storage-migrate arguments so the rest
// can be handed to the normal configuration parser.
func splitStorageMigrateFlags(args []string) ([]string, storageMigrateOptions, error) {
	var out []string
	var opts storageMigrateOptions
	if len(args) == 0 || args[0] != "migrate" {
		return nil, opts, fmt.Errorf("usage: rapig storage migrate -from <name|#id> -to <name> [-dry-run]")
	}
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		a := rest[i]
		switch {
		case a == "-dry-run" || a == "--dry-run":
			opts.dryRun = true
		case a == "-from" || a == "--from":
			if i+1 >= len(rest) {
				return nil, opts, fmt.Errorf("storage migrate: %s needs a value", a)
			}
			i++
			opts.from = rest[i]
		case a == "-to" || a == "--to":
			if i+1 >= len(rest) {
				return nil, opts, fmt.Errorf("storage migrate: %s needs a value", a)
			}
			i++
			opts.to = rest[i]
		case strings.HasPrefix(a, "-from="):
			opts.from = strings.TrimPrefix(a, "-from=")
		case strings.HasPrefix(a, "--from="):
			opts.from = strings.TrimPrefix(a, "--from=")
		case strings.HasPrefix(a, "-to="):
			opts.to = strings.TrimPrefix(a, "-to=")
		case strings.HasPrefix(a, "--to="):
			opts.to = strings.TrimPrefix(a, "--to=")
		default:
			// Everything else (for example -data) is a regular config flag;
			// let config.Load report unknown flags with its own message.
			out = append(out, a)
		}
	}
	if opts.from == "" || opts.to == "" {
		return nil, opts, fmt.Errorf("storage migrate needs -from and -to")
	}
	return out, opts, nil
}

// runStorageMigrate implements `rapig storage migrate`: an offline, resumable
// move of every blob from one local root to another (M6.2 / F-42).
func runStorageMigrate(ctx context.Context, cfg config.Config, db *sqlstore.DB, log *slog.Logger, opts storageMigrateOptions) int {
	applied, err := db.Migrate(ctx, migrations.FS)
	if err != nil {
		log.Error("migration failed", "err", err)
		return 1
	}
	if len(applied) > 0 {
		log.Info("migrations applied", "count", len(applied), "files", applied)
	}
	fromID, err := storagemig.ResolvePolicy(ctx, db, opts.from)
	if err != nil {
		log.Error("cannot resolve source policy", "from", opts.from, "err", err)
		return 2
	}
	toID, err := storagemig.ResolvePolicy(ctx, db, opts.to)
	if err != nil {
		log.Error("cannot resolve target policy", "to", opts.to, "err", err)
		return 2
	}
	lockPath := filepath.Join(cfg.DataDir, "storage-migrate.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		log.Error("another storage migration looks active; remove the lock file if it is stale", "lock", lockPath, "err", err)
		return 1
	}
	fmt.Fprintf(lock, "%d\n", os.Getpid())
	lock.Close()
	defer os.Remove(lockPath)

	registry := storagereg.NewRegistry(db, log)
	rep, err := storagemig.Migrate(ctx, db, registry, log, storagemig.Options{From: fromID, To: toID, DryRun: opts.dryRun})
	if err != nil {
		log.Error("storage migrate failed", "err", err)
		if errors.Is(err, storagemig.ErrSamePolicy) || errors.Is(err, storagemig.ErrSourceEnabled) ||
			errors.Is(err, storagemig.ErrTargetDisabled) || errors.Is(err, storagemig.ErrPolicyBusy) ||
			errors.Is(err, storagemig.ErrRefcountDrift) {
			return 2
		}
		return 1
	}
	fmt.Printf("storage migrate: from=#%d to=#%d blobs=%d nodes=%d bytes=%d copied=%d reused=%d removed_files=%d dry_run=%t\n",
		rep.FromPolicy, rep.ToPolicy, rep.Blobs, rep.Nodes, rep.Bytes, rep.Copied, rep.Reused, rep.RemovedFiles, rep.DryRun)
	return 0
}

// runAdmin implements `rapig gc` and `rapig fsck`. Both are offline commands:
// they migrate first (a new binary must be able to read the schema it uses) and
// then work only on the data directory next to the executable.
func runAdmin(ctx context.Context, cfg config.Config, db *sqlstore.DB, log *slog.Logger, cmd string, opts adminOptions) int {
	applied, err := db.Migrate(ctx, migrations.FS)
	if err != nil {
		log.Error("migration failed", "err", err)
		return 1
	}
	if len(applied) > 0 {
		log.Info("migrations applied", "count", len(applied), "files", applied)
	}
	if _, err := db.EnsureDefaultStoragePolicy(ctx, cfg.StorageDir()); err != nil {
		log.Error("cannot prepare storage policy", "err", err)
		return 1
	}
	registry := storagereg.NewRegistry(db, log)
	if err := registry.ProbeEnabled(ctx); err != nil {
		log.Error("storage policies are not usable", "err", err)
		return 1
	}
	cleanTempRoots(ctx, registry, log)

	switch cmd {
	case "gc":
		res, err := maintenance.GC(ctx, db, registry, opts.dryRun, time.Now(), log)
		if err != nil {
			log.Error("gc failed", "err", err)
			return 1
		}
		// One line per root when the deployment has more than one, so an
		// operator can see which disk changed; the final line stays the
		// deployment-wide total and keeps the old output shape.
		if len(res.Policies) > 1 {
			for _, p := range res.Policies {
				fmt.Printf("gc policy: id=%d root=%s blobs=%d bytes=%d\n", p.PolicyID, p.RootPath, p.Removed, p.BytesFreed)
			}
		}
		fmt.Printf("gc: blobs=%d bytes=%d dry_run=%t\n", res.Removed, res.BytesFreed, res.DryRun)
		return 0
	case "fsck":
		rep, err := maintenance.Fsck(ctx, db, registry, opts.verifyContent, opts.repair, log)
		if err != nil {
			log.Error("fsck failed", "err", err)
			return 1
		}
		if len(rep.Policies) > 1 {
			for _, p := range rep.Policies {
				fmt.Printf("fsck policy: id=%d root=%s blobs=%d missing_files=%d size_mismatches=%d bad_content=%d\n",
					p.PolicyID, p.RootPath, p.Blobs, len(p.MissingFiles), len(p.SizeMismatches), len(p.BadContent))
			}
		}
		fmt.Printf("fsck: blobs=%d missing_files=%d size_mismatches=%d bad_content=%d broken_node_blobs=%d refcount_repaired=%d\n",
			rep.Blobs, len(rep.MissingFiles), len(rep.SizeMismatches), len(rep.BadContent),
			len(rep.BrokenNodeBlobs), rep.RefcountRepaired)
		for _, sha := range rep.MissingFiles {
			fmt.Printf("  missing file: %s\n", sha)
		}
		for _, sha := range rep.SizeMismatches {
			fmt.Printf("  size mismatch: %s\n", sha)
		}
		for _, sha := range rep.BadContent {
			fmt.Printf("  checksum mismatch: %s\n", sha)
		}
		for _, id := range rep.BrokenNodeBlobs {
			fmt.Printf("  node with missing blob row: %d\n", id)
		}
		if len(rep.MissingFiles)+len(rep.SizeMismatches)+len(rep.BadContent)+len(rep.BrokenNodeBlobs) > 0 {
			return 1
		}
		return 0
	}
	return 2
}

// cleanTempRoots removes stale temporary upload files from every policy root,
// disabled ones included: a disabled root can still hold leftovers from before
// it was turned off, and they are never served again.
func cleanTempRoots(ctx context.Context, registry *storagereg.Registry, log *slog.Logger) {
	roots, err := registry.All(ctx)
	if err != nil {
		log.Warn("cannot list storage policies", "err", err)
		return
	}
	for _, root := range roots {
		store, err := registry.Get(ctx, root.ID)
		if err != nil {
			log.Warn("cannot open storage policy", "policy_id", root.ID, "err", err)
			continue
		}
		if n, err := store.CleanTemp(); err != nil {
			log.Warn("cannot clean storage temp dir", "policy_id", root.ID, "err", err)
		} else if n > 0 {
			log.Info("stale temporary uploads removed", "policy_id", root.ID, "count", n)
		}
	}
}

func runMigrate(ctx context.Context, db *sqlstore.DB, log *slog.Logger) int {
	applied, err := db.Migrate(ctx, migrations.FS)
	if err != nil {
		log.Error("migration failed", "err", err)
		return 1
	}
	if len(applied) == 0 {
		log.Info("database schema is up to date")
		return 0
	}
	log.Info("migrations applied", "count", len(applied), "files", applied)
	return 0
}

func runServe(ctx context.Context, cfg config.Config, db *sqlstore.DB, log *slog.Logger) int {
	if err := startPProf(ctx, cfg.PProf, log); err != nil {
		log.Error("cannot start pprof listener", "err", err, "pprof", cfg.PProf)
		return 1
	}
	if !prepare(ctx, db, log, cfg) {
		return 1
	}
	policyID, err := db.EnsureDefaultStoragePolicy(ctx, cfg.StorageDir())
	if err != nil {
		log.Error("cannot prepare storage policy", "err", err)
		return 1
	}
	registry := storagereg.NewRegistry(db, log)
	if err := registry.ProbeEnabled(ctx); err != nil {
		log.Error("storage policies are not usable", "err", err)
		return 1
	}
	cleanTempRoots(ctx, registry, log)

	ids := idsvc.NewService(db, log)
	boot, err := ids.Bootstrap(ctx, cfg.BootstrapDir(), policyID)
	if err != nil {
		log.Error("admin bootstrap failed", "err", err)
		return 1
	}
	if boot.Created {
		log.Warn("initial admin account created — change the password after first login",
			"username", boot.Username,
			"password", boot.Password,
			"password_file", boot.FilePath,
		)
	} else {
		log.Info("admin account already present")
	}

	budget := cfg.Budget()
	fls := filessvc.NewService(db, registry, log)
	ups := upsvc.New(db, registry, fls, log, budget.UploadConcurrency, budget.CopyBufferBytes)
	shs := sharesvc.New(db, fls, log)
	fetcher := fetch.New(cfg.AllowPrivateDownloads)
	trs := transfersvc.New(db, registry, fetcher, log, budget.DownloadConcurrency,
		transfersvc.Options{QB: newQBClient(ctx, cfg, log), DataDir: cfg.DataDir})
	adm := adminsvc.New(db, log)
	acc := accountsvc.New(db, ids, log)
	grp := groupssvc.NewService(db, log)
	runMaintenance(ctx, db, ids, ups, fls, shs, trs, acc, log)

	webFS, err := web.FS()
	if err != nil {
		log.Warn("embedded frontend unavailable, serving API only", "err", err)
	}
	frontendEmbedded := false
	if webFS != nil {
		if _, statErr := fs.Stat(webFS, "index.html"); statErr != nil {
			log.Warn("embedded frontend has no index.html, serving API only (run scripts/build.sh)")
			webFS = nil
		} else {
			frontendEmbedded = true
		}
	}
	handler := httpx.New(cfg, log, db, registry, ids, fls, ups, shs, trs, adm, acc, grp, webFS).Handler()
	srv := &stdhttp.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	cleanupDone := make(chan struct{})
	go func() {
		defer close(cleanupDone)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		transfersTick := time.NewTicker(2 * time.Second)
		defer transfersTick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runMaintenance(ctx, db, ids, ups, fls, shs, trs, acc, log)
			case <-transfersTick.C:
				if cfg.WorkerMode == config.WorkerInternal {
					trs.RunPending(ctx)
					trs.RunQB(ctx)
				}
			}
		}
	}()

	errCh := make(chan error, 1)
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Error("cannot listen", "err", err, "addr", cfg.Addr)
		return 1
	}
	// The pid file lets `rapig service stop` on Windows (and the operator menu)
	// find the detached process.
	pidPath := filepath.Join(cfg.DataDir, "rapig.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		log.Warn("cannot write pid file", "err", err, "path", pidPath)
	} else {
		defer os.Remove(pidPath)
	}
	go func() {
		log.Info("rapig listening",
			"addr", cfg.Addr,
			"data_dir", cfg.DataDir,
			"version", version.Version,
			"frontend_embedded", frontendEmbedded,
		)
		errCh <- srv.Serve(listener)
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			log.Error("http server failed", "err", err)
			return 1
		}
		return 0
	case <-ctx.Done():
		log.Info("shutdown requested")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Warn("graceful shutdown failed", "err", err)
			_ = srv.Close()
		}
		<-cleanupDone
		trs.Wait()
		log.Info("rapig stopped")
		return 0
	}
}

// runWorker is the separable background process. It owns everything that does
// not need to happen inside a request: expired session cleanup and audit log
// retention. Both are idempotent, so running them at startup and on a ticker
// is safe and keeps the server process free of background writes.
func runWorker(ctx context.Context, cfg config.Config, db *sqlstore.DB, log *slog.Logger) int {
	if !prepare(ctx, db, log, cfg) {
		return 1
	}
	ids := idsvc.NewService(db, log)
	if _, err := db.EnsureDefaultStoragePolicy(ctx, cfg.StorageDir()); err != nil {
		log.Error("cannot prepare storage policy", "err", err)
		return 1
	}
	registry := storagereg.NewRegistry(db, log)
	if err := registry.ProbeEnabled(ctx); err != nil {
		log.Error("storage policies are not usable", "err", err)
		return 1
	}
	budget := cfg.Budget()
	fls := filessvc.NewService(db, registry, log)
	ups := upsvc.New(db, registry, fls, log, budget.UploadConcurrency, budget.CopyBufferBytes)
	shs := sharesvc.New(db, fls, log)
	fetcher := fetch.New(cfg.AllowPrivateDownloads)
	trs := transfersvc.New(db, registry, fetcher, log, budget.DownloadConcurrency,
		transfersvc.Options{QB: newQBClient(ctx, cfg, log), DataDir: cfg.DataDir})
	acc := accountsvc.New(db, ids, log)
	log.Info("rapig worker started", "data_dir", cfg.DataDir, "version", version.Version)
	runMaintenance(ctx, db, ids, ups, fls, shs, trs, acc, log)
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	transfersTick := time.NewTicker(2 * time.Second)
	defer transfersTick.Stop()
	for {
		select {
		case <-ctx.Done():
			trs.Wait()
			log.Info("rapig worker stopped")
			return 0
		case <-ticker.C:
			runMaintenance(ctx, db, ids, ups, fls, shs, trs, acc, log)
		case <-transfersTick.C:
			trs.RunPending(ctx)
			trs.RunQB(ctx)
		}
	}
}

// auditRetentionDays reads settings.audit_retention_days with a conservative
// default. A bad value is logged and ignored rather than failing the worker:
// housekeeping must never take the service down.
func auditRetentionDays(ctx context.Context, db *sqlstore.DB, log *slog.Logger) int {
	const fallback = sqlstore.DefaultAuditRetentionDays
	raw, ok, err := db.Setting(ctx, sqlstore.SettingAuditRetentionDays)
	if err != nil {
		log.Warn("cannot read audit retention setting", "err", err)
		return fallback
	}
	if !ok || raw == "" {
		return fallback
	}
	days, err := strconv.Atoi(raw)
	if err != nil || days < 1 || days > 3650 {
		log.Warn("invalid audit_retention_days, using default",
			"value", raw, "default", fallback)
		return fallback
	}
	return days
}

// runMaintenance performs one housekeeping pass. Failures are logged, never
// fatal: the next tick retries.
func runMaintenance(ctx context.Context, db *sqlstore.DB, ids *idsvc.Service, ups *upsvc.Service, fls *filessvc.Service, shs *sharesvc.Service, trs *transfersvc.Service, acc *accountsvc.Service, log *slog.Logger) {
	if n, err := ids.CleanupSessions(ctx); err != nil {
		log.Warn("session cleanup failed", "err", err)
	} else if n > 0 {
		log.Info("expired sessions removed", "count", n)
	}
	if n, err := ups.CleanupExpired(ctx, 200); err != nil {
		log.Warn("upload cleanup failed", "err", err)
	} else if n > 0 {
		log.Info("expired uploads canceled", "count", n)
	}
	if n, err := fls.PurgeExpiredTrash(ctx, 200); err != nil {
		log.Warn("trash cleanup failed", "err", err)
	} else if n > 0 {
		log.Info("expired trash entries purged", "count", n)
	}
	if n, err := shs.CleanupDead(ctx); err != nil {
		log.Warn("share cleanup failed", "err", err)
	} else if n > 0 {
		log.Info("dead share rows removed", "count", n)
	}
	if n, err := trs.Cleanup(ctx); err != nil {
		log.Warn("transfer cleanup failed", "err", err)
	} else if n > 0 {
		log.Info("old transfer rows removed", "count", n)
	}
	if n, tombstones, err := acc.PurgeExpired(ctx); err != nil {
		log.Warn("account purge failed", "err", err)
	} else if n > 0 || tombstones > 0 {
		log.Info("account purge", "purged", n, "tombstones_removed", tombstones)
	}

	days := auditRetentionDays(ctx, db, log)
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	if n, err := db.PruneAudit(ctx, cutoff); err != nil {
		log.Warn("audit prune failed", "err", err)
	} else if n > 0 {
		log.Info("old audit rows removed", "count", n, "retention_days", days)
	}
}

// newQBClient builds the qBittorrent client from configuration. An empty
// QBURL disables magnet downloads entirely (nil client). A failed startup
// probe only warns: an unreachable qBittorrent must not stop the server, and
// the poller retries its own login on every pass. Credentials are never
// logged.
func newQBClient(ctx context.Context, cfg config.Config, log *slog.Logger) *qb.Client {
	if cfg.QBURL == "" {
		return nil
	}
	client, err := qb.New(cfg.QBURL, cfg.QBUsername, cfg.QBPassword)
	if err != nil {
		log.Warn("qBittorrent disabled: invalid configuration", "err", err)
		return nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ver, err := client.Version(probeCtx)
	if err != nil {
		log.Warn("qBittorrent not reachable at startup, magnet downloads will retry", "err", err)
		return client
	}
	log.Info("qBittorrent connected", "version", ver)
	return client
}

func prepare(ctx context.Context, db *sqlstore.DB, log *slog.Logger, cfg config.Config) bool {
	applied, err := db.Migrate(ctx, migrations.FS)
	if err != nil {
		log.Error("migration failed", "err", err)
		return false
	}
	if len(applied) > 0 {
		log.Info("migrations applied", "count", len(applied), "files", applied)
	}
	return true
}

// applyBudget installs the low/high memory preset with runtime/debug. When
// GOGC or GOMEMLIMIT is set explicitly in the environment we do not override
// it: the operator asked for that value (docs/09-m1-plan.md §3).
func applyBudget(cfg config.Config, log *slog.Logger) {
	b := cfg.Budget()
	if v := os.Getenv("GOGC"); v != "" {
		log.Info("GOGC overridden by environment", "value", v, "profile_default", b.GOGC)
	} else {
		debug.SetGCPercent(b.GOGC)
	}
	if v := os.Getenv("GOMEMLIMIT"); v != "" {
		log.Info("GOMEMLIMIT overridden by environment", "value", v, "profile_default", b.MemLimitBytes)
	} else {
		debug.SetMemoryLimit(b.MemLimitBytes)
	}
	log.Info("memory budget",
		"profile", b.Profile,
		"db_max_conns", b.DBMaxConns,
		"db_cache_kib", b.DBCacheKiB,
		"gogc", b.GOGC,
		"mem_limit_bytes", b.MemLimitBytes,
		"upload_concurrency", b.UploadConcurrency,
		"download_concurrency", b.DownloadConcurrency,
	)
}

// startPProf serves net/http/pprof on a dedicated loopback listener. It is
// disabled unless -pprof is set and the address is validated as loopback by
// config.Load, so profiles never leave the machine.
func startPProf(ctx context.Context, addr string, log *slog.Logger) error {
	if addr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("pprof: listen %s: %w", addr, err)
	}
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &stdhttp.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, stdhttp.ErrServerClosed) {
			log.Warn("pprof listener stopped", "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	log.Info("pprof listening (loopback only, diagnostic)", "addr", ln.Addr().String())
	return nil
}

func usage() {
	fmt.Fprintf(os.Stderr, `rapig %s — self-hosted cloud drive (files, shares, offline downloads, groups, admin, deploy)

usage:
  rapig serve   [flags]   start the API and the embedded web UI
  rapig worker  [flags]   start the background worker process
  rapig migrate [flags]   apply database migrations and exit
  rapig gc      [flags]   delete unreferenced blobs (offline; -dry-run to preview)
  rapig fsck    [flags]   check blobs/nodes (offline; -repair -verify-content)
  rapig storage migrate -from <name|#id> -to <name> [-dry-run]
                           move every blob between storage policies (offline)
  rapig doctor  [flags]   read-only self check (data, database, storage, port)
  rapig install [flags]   install the systemd service (root; see rapig menu)
                          flags: --prefix/--user/--port/--data/--no-service/
                                 --no-credentials/--dry-run/--yes
  rapig uninstall [flags] remove the service, binary, data and system user (root)
  rapig service <action>  status|start|stop|restart the systemd service
  rapig config [--show]   edit or print <prefix>/rapig.env (root)
  rapig gen-unit          print the systemd unit for the current layout
  rapig upgrade           replace the binary with --binary, restart and roll back on failure
  rapig reset-admin-password  offline admin password reset (root; service stopped)
  rapig menu              interactive operator menu (also shown by a bare rapig on a TTY)
  rapig launch            Windows: ensure the service is running and open the web UI
  rapig init-admin        Windows: create the first admin (--password-stdin; used by setup)
  rapig autostart <on|off|status>
                           Windows: start automatically at sign-in (no admin rights)
  rapig logs              Windows: print the tail of logs/rapig.log
  rapig version           print build information

flags (all subcommands):
  -addr string           HTTP listen address (default 127.0.0.1:50011)
  -data string           data directory (default <exe dir>/data)
  -log-level string      debug|info|warn|error (default info)
  -allow-origin value    extra allowed browser origin, repeatable
  -profile string        low|high memory profile (default low; high for 4GB+ hosts)
  -pprof string          loopback host:port for pprof, e.g. 127.0.0.1:6060 (default off)
  -cookie-secure string  auto|always|never (default auto; use always behind TLS proxy)
  -worker string         internal|external background worker (default internal)
  -allow-private-downloads  allow offline downloads from private addresses (unsafe)
  -qb-url string         qBittorrent WebUI base URL, e.g. http://127.0.0.1:8080 (default off)
  -qb-username string    qBittorrent WebUI username
  -qb-password string    qBittorrent WebUI password (prefer RAPIG_QB_PASSWORD)

maintenance flags (gc, fsck):
  -dry-run               gc: report what would be removed, change nothing
  -repair                fsck: rebuild blob refcounts from node rows
  -verify-content        fsck: re-hash every blob file (slow, use for audits)

doctor flags:
  -json                  print the report as JSON

storage migrate flags:
  -from string           source policy name or #id (must be disabled)
  -to string             target policy name or #id (must be enabled)
  -dry-run               report what would move, change nothing

environment:
  RAPIG_ADDR, RAPIG_DATA_DIR, RAPIG_LOG_LEVEL, RAPIG_ALLOW_ORIGINS,
  RAPIG_PROFILE, RAPIG_PPROF, RAPIG_COOKIE_SECURE, RAPIG_WORKER,
  RAPIG_ALLOW_PRIVATE_DOWNLOADS, RAPIG_QB_URL, RAPIG_QB_USERNAME,
  RAPIG_QB_PASSWORD
  GOGC / GOMEMLIMIT override the profile defaults when set explicitly
`, version.Version)
}
