//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"rapig/internal/app/winapp"
)

// runUninstallWindowsCLI removes a per-user installation: service, autostart
// task, desktop shortcut, "Apps & features" entry and finally the directory.
func runUninstallWindowsCLI(args []string) int {
	rest, flags := splitLaunchFlags(args)
	opts, err := launchOptionsFromFlags(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rapig uninstall:", err)
		return 1
	}
	yes := false
	for _, a := range rest {
		if a == "--yes" || a == "-y" {
			yes = true
		}
	}
	prefix := opts.Prefix
	if prefix == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "rapig uninstall:", err)
			return 1
		}
		prefix = filepath.Dir(exe)
	}
	prefix, err = filepath.Abs(prefix)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rapig uninstall:", err)
		return 1
	}
	if _, err := os.Stat(filepath.Join(prefix, "rapig.exe")); err != nil {
		fmt.Fprintf(os.Stderr, "rapig uninstall: %s 中没有 rapig.exe，取消\n", prefix)
		return 1
	}

	if !yes {
		fmt.Println("将删除以下内容：")
		fmt.Printf("  程序与数据：%s（包括 data 与 logs）\n", prefix)
		fmt.Println("  桌面快捷方式、登录自启任务、「应用和功能」注册项")
		fmt.Print("输入 DELETE 确认删除: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.TrimSpace(line) != "DELETE" {
			fmt.Println("已取消")
			return 1
		}
	}

	fmt.Println("正在停止服务…")
	if opts.HealthOK() || opts.StatusRunning() {
		_ = opts.Stop(5 * 1e9)
	}
	fmt.Println("正在移除登录自启…")
	_ = opts.AutostartOff()
	fmt.Println("正在删除桌面快捷方式…")
	removeDesktopShortcut()
	fmt.Println("正在删除「应用和功能」注册项…")
	_ = registry.DeleteKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall\Rapig`)

	// Delete everything we can while we are still running (the server has been
	// stopped, so data/logs are no longer locked). rapig.exe itself is locked
	// by the running process and is handled below.
	fmt.Println("正在删除数据与日志…")
	removeDirContentsExcept(prefix, "rapig.exe")

	fmt.Println("正在删除程序目录（后台完成）…")
	// The running exe cannot delete itself; let a detached cmd finish the job.
	cmdLine := fmt.Sprintf(`ping -n 3 127.0.0.1 >NUL & rmdir /S /Q "%s"`, prefix)
	cmd := exec.Command("cmd", "/c", cmdLine)
	// Run from the parent so the child can delete its own directory.
	cmd.Dir = filepath.Dir(prefix)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "rapig uninstall: 无法启动删除进程：", err)
		return 1
	}
	fmt.Println("卸载完成。程序目录会在数秒内删除。")
	fmt.Println("若数秒后目录仍存在，请手动删除：", prefix)
	return 0
}

func removeDirContentsExcept(dir, keep string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.EqualFold(e.Name(), keep) {
			continue
		}
		_ = os.RemoveAll(filepath.Join(dir, e.Name()))
	}
}

func removeDesktopShortcut() {
	desktop := filepath.Join(os.Getenv("USERPROFILE"), "Desktop")
	_ = os.Remove(filepath.Join(desktop, "Rapig.lnk"))
	_ = os.Remove(filepath.Join(desktop, "Rapig.url"))
}

var _ = winapp.Options{}
