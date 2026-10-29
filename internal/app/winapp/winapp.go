// Package winapp implements the Windows desktop experience for Rapig:
// first-run administrator setup, starting the server in the background,
// console log rotation, per-user autostart and the operator menu.
//
// Linux has its own installer/service integration; the Windows path is
// deliberately self contained and uses only per-user APIs (no admin rights).
package winapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ErrUnsupported is returned when a Windows-only action runs elsewhere.
var ErrUnsupported = errors.New("this command is only supported on Windows")

// Options describe the per-user installation layout (%LOCALAPPDATA%\Rapig).
type Options struct {
	Prefix  string // directory containing rapig.exe
	Addr    string // listen address, e.g. 127.0.0.1:50011
	DataDir string // data directory (default <prefix>/data)
}

// OptionsFor resolves the layout from the running executable.
func OptionsFor(prefix, addr, dataDir string) (Options, error) {
	o := Options{Prefix: prefix, Addr: addr, DataDir: dataDir}
	if o.Prefix == "" {
		exe, err := os.Executable()
		if err != nil {
			return Options{}, fmt.Errorf("locate executable: %w", err)
		}
		o.Prefix = filepath.Dir(exe)
	}
	if o.DataDir == "" {
		o.DataDir = filepath.Join(o.Prefix, "data")
	}
	if o.Addr == "" {
		o.Addr = "127.0.0.1:50011"
	}
	return o, nil
}

// ExePath is the rapig.exe alongside which data/ and logs/ live.
func (o Options) ExePath() string { return filepath.Join(o.Prefix, "rapig.exe") }

// Data returns the data directory.
func (o Options) Data() string { return o.DataDir }

// LogDir returns <prefix>/logs.
func (o Options) LogDir() string { return filepath.Join(o.Prefix, "logs") }

// LogPath returns the console log file used by detached instances.
func (o Options) LogPath() string { return filepath.Join(o.LogDir(), "rapig.log") }

// PIDPath returns the pid file written by `rapig serve`.
func (o Options) PIDPath() string { return filepath.Join(o.DataDir, "rapig.pid") }

// URL is the browser URL for the configured address.
func (o Options) URL() string {
	addr := o.Addr
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	return "http://" + addr
}

// HealthOK reports whether /health answers 200 on the configured address.
func (o Options) HealthOK() bool {
	return HealthOK(o.Addr)
}

// HealthOK probes /health once.
func HealthOK(addr string) bool {
	c := httpClient
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	req, err := newRequest("GET", "http://"+addr+"/health")
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	return resp.StatusCode == 200
}

// WaitHealthy polls /health until it answers or the timeout expires.
func (o Options) WaitHealthy(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if o.HealthOK() {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("server did not answer %s within %s", o.URL()+"/health", timeout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// RotateLog renames rapig.log to rapig.log.1 (and .1 to .2) when it exceeds
// maxBytes, keeping at most maxBytes*1.5 of history (3 files).
func RotateLog(path string, maxBytes int64) error {
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Size() < maxBytes {
		return nil
	}
	if err := os.Remove(path + ".2"); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(path+".1", path+".2"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Rename(path, path+".1")
}

// TailLog writes the last n lines of path to w.
func TailLog(path string, n int, w io.Writer) error {
	if n <= 0 {
		n = 80
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	const maxTail = 1 << 20
	start := fi.Size() - maxTail
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\r\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	for _, line := range lines {
		fmt.Fprintln(w, line)
	}
	return nil
}

// ReadPID reads the pid file, returning 0 when it is missing or invalid.
func ReadPID(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}

// IsWindows is a helper for call sites that need to branch.
func IsWindows() bool { return runtime.GOOS == "windows" }

// Start launches `<prefix>/rapig.exe serve` detached from the current console,
// redirecting its output to the rotating log file. It returns once the child
// has been spawned; callers should WaitHealthy afterwards.
func (o Options) Start() error {
	if err := o.rotateAndEnsureLog(); err != nil {
		return err
	}
	return spawnDetached(o.ExePath(), o.LogPath(), "serve", "-data", o.DataDir, "-addr", o.Addr)
}

// StatusRunning reports whether the pid file exists and that process is alive.
func (o Options) StatusRunning() bool {
	pid := ReadPID(o.PIDPath())
	return pid != 0 && processAlive(pid)
}

// Stop asks a detached instance to exit, waiting up to grace for it to leave,
// then kills it. It uses the pid file written by `rapig serve`.
func (o Options) Stop(grace time.Duration) error {
	pid := ReadPID(o.PIDPath())
	if pid == 0 {
		if o.HealthOK() {
			return fmt.Errorf("server is answering at %s but no pid file exists at %s", o.URL(), o.PIDPath())
		}
		fmt.Println("rapig is not running")
		return nil
	}
	// Windows has no portable SIGTERM for a detached console process, so a
	// polite taskkill is best effort and the force kill is the real stop.
	_ = terminatePID(pid, false)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			_ = os.Remove(o.PIDPath())
			fmt.Println("rapig stopped")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	if err := terminatePID(pid, true); err != nil {
		return fmt.Errorf("force stop pid %d: %w", pid, err)
	}
	for i := 0; i < 25 && processAlive(pid); i++ {
		time.Sleep(200 * time.Millisecond)
	}
	_ = os.Remove(o.PIDPath())
	fmt.Println("rapig force-stopped (SQLite WAL keeps the data consistent)")
	return nil
}

// Status prints a human readable status line.
func (o Options) Status() {
	pid := ReadPID(o.PIDPath())
	state := "not running"
	if pid != 0 && processAlive(pid) {
		state = fmt.Sprintf("running (pid %d)", pid)
	}
	health := "unhealthy"
	if o.HealthOK() {
		health = "healthy"
	}
	fmt.Printf("rapig: %s, %s, %s\n", state, health, o.URL())
}

func (o Options) rotateAndEnsureLog() error {
	if err := os.MkdirAll(o.LogDir(), 0o755); err != nil {
		return err
	}
	return RotateLog(o.LogPath(), 5<<20)
}

// OpenBrowser opens url in the default browser without waiting.
func OpenBrowser(url string) error {
	if !IsWindows() {
		return ErrUnsupported
	}
	return openURL(url)
}

// AutostartOn registers the per-user logon task (no elevation required).
func (o Options) AutostartOn() error {
	if !IsWindows() {
		return ErrUnsupported
	}
	dir := filepath.Join(o.Prefix, "autostart")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	wrapper := filepath.Join(dir, "rapig-autostart.cmd")
	script := "@echo off\r\n" +
		"rem Rapig per-user autostart: retry a few times, then give up quietly.\r\n" +
		"setlocal\r\n" +
		"for /L %%i in (1,1,3) do (\r\n" +
		"  start \"\" /min \"" + o.ExePath() + "\" serve -data \"" + o.DataDir + "\" -addr " + o.Addr + "\r\n" +
		"  timeout /t 60 /nobreak >nul\r\n" +
		"  tasklist /FI \"IMAGENAME eq rapig.exe\" | find /I \"rapig.exe\" >nul && exit /b 0\r\n" +
		")\r\n" +
		"exit /b 1\r\n"
	if err := os.WriteFile(wrapper, []byte(script), 0o755); err != nil {
		return err
	}
	return createLogonTask(wrapper)
}

// AutostartOff removes the per-user logon task.
func (o Options) AutostartOff() error {
	if !IsWindows() {
		return ErrUnsupported
	}
	return deleteLogonTask()
}

// AutostartStatus prints whether the logon task exists.
func (o Options) AutostartStatus() {
	if !IsWindows() {
		fmt.Println("autostart: not supported on this platform")
		return
	}
	enabled, detail := logonTaskStatus()
	if enabled {
		fmt.Println("autostart: on")
		return
	}
	fmt.Println("autostart: off")
	if detail != "" {
		fmt.Println("  (" + detail + ")")
	}
}

// Run is a convenience entry used by `rapig launch`; it is split out so tests
// can call the pieces individually.
func (o Options) Run(ctx context.Context) int {
	_ = ctx
	fmt.Println("Rapig 桌面入口")
	fmt.Println("  程序目录:", o.Prefix)
	fmt.Println("  数据目录:", o.DataDir)
	fmt.Println()
	if !o.HealthOK() {
		fmt.Println("正在后台启动 Rapig...")
		if err := o.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "启动失败:", err)
			return 1
		}
		if err := o.WaitHealthy(25 * time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "启动失败:", err)
			fmt.Fprintln(os.Stderr, "请查看日志:", o.LogPath())
			return 1
		}
	}
	if err := OpenBrowser(o.URL()); err != nil {
		fmt.Println("无法自动打开浏览器，请手动访问:", o.URL())
	} else {
		fmt.Println("已在浏览器打开:", o.URL())
	}
	return RunMenu(o)
}
