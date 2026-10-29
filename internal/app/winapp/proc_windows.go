//go:build windows

package winapp

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
)

func spawnDetached(exe, logPath string, args ...string) error {
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNewProcessGroup | createNoWindow,
	}
	if err := cmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("start rapig serve: %w", err)
	}
	go func() {
		_ = cmd.Wait()
		logFile.Close()
	}()
	return nil
}

func processAlive(pid int) bool {
	out, err := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
	if err != nil {
		return false
	}
	s := strings.TrimSpace(string(out))
	if s == "" || strings.Contains(s, "No tasks are running") || strings.Contains(s, "没有运行的任务") {
		return false
	}
	return strings.Contains(s, "rapig.exe")
}

func terminatePID(pid int, force bool) error {
	args := []string{"/PID", strconv.Itoa(pid)}
	if force {
		args = append(args, "/F")
	}
	cmd := exec.Command("taskkill", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Run()
}

func openURL(url string) error {
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	return cmd.Start()
}

func createLogonTask(wrapper string) error {
	_ = exec.Command("schtasks", "/Delete", "/TN", "Rapig", "/F").Run() // ignore when missing
	cmd := exec.Command("schtasks", "/Create",
		"/TN", "Rapig",
		"/SC", "ONLOGON",
		"/TR", `"`+wrapper+`"`,
		"/RL", "LIMITED",
		"/F",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Create failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func deleteLogonTask() error {
	cmd := exec.Command("schtasks", "/Delete", "/TN", "Rapig", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("schtasks /Delete failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func logonTaskStatus() (bool, string) {
	cmd := exec.Command("schtasks", "/Query", "/TN", "Rapig")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, ""
	}
	display := strings.Join(strings.Fields(string(out)), " ")
	if len(display) > 140 {
		display = display[:140]
	}
	return true, display
}
