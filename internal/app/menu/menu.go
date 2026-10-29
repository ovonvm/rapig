// Package menu implements the interactive operator menu shown by a bare
// `rapig` invocation on a terminal (and by `rapig menu` for scripted checks).
package menu

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"rapig/internal/app/installer"
)

// Options describe where the installation lives.
type Options struct {
	Prefix  string
	User    string
	Port    string
	DataDir string
	In      io.Reader
	Out     io.Writer
}

// Run displays the menu until the user quits. It returns a process exit code.
func Run(ctx context.Context, opts Options) int {
	if opts.Prefix == "" {
		opts.Prefix = "/opt/rapig"
	}
	if opts.User == "" {
		opts.User = "rapig"
	}
	if opts.Port == "" {
		opts.Port = "50011"
	}
	if opts.In == nil {
		opts.In = os.Stdin
	}
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	installed := installer.Installed(opts.Prefix)
	root := installer.IsRoot()

	fmt.Fprintf(opts.Out, "\nRapig 运维菜单 —— %s（%s）\n", opts.Prefix, modeLabel(installed))
	scanner := bufio.NewScanner(opts.In)
	for {
		fmt.Fprintln(opts.Out, "")
		if installed {
			state := installer.ServiceStatus()
			health := healthLine(opts.Port)
			fmt.Fprintf(opts.Out, "服务状态：%s   健康检查：%s   管理员：admin\n", state, health)
		} else {
			fmt.Fprintf(opts.Out, "便携模式：二进制 %s；数据目录 %s\n", filepath.Join(opts.Prefix, "rapig"), dataDir(opts))
		}
		if installed {
			fmt.Fprintln(opts.Out, "1) 启动服务   2) 停止服务   3) 重启服务")
			fmt.Fprintln(opts.Out, "4) 修改配置   5) doctor 自检  6) 日志（最近 80 行）")
			fmt.Fprintln(opts.Out, "7) 备份       8) 恢复演练    9) 从备份恢复")
			fmt.Fprintln(opts.Out, "10) 升级      11) 回滚       12) 重置管理员密码")
			fmt.Fprintln(opts.Out, "13) 卸载（全删） 14) 等效命令帮助   0) 退出")
		} else {
			fmt.Fprintln(opts.Out, "1) 前台启动（Ctrl+C 停止） 2) doctor 自检")
			fmt.Fprintln(opts.Out, "3) 备份 4) 恢复演练 5) 等效命令帮助 0) 退出")
		}
		fmt.Fprint(opts.Out, "选择: ")
		if !scanner.Scan() {
			fmt.Fprintln(opts.Out, "")
			return 0
		}
		choice := strings.TrimSpace(scanner.Text())
		fmt.Fprintln(opts.Out, "")
		var err error
		switch choice {
		case "0", "q", "quit":
			return 0
		case "1":
			if installed {
				err = installer.Service("start")
			} else {
				return runServeForeground(opts)
			}
		case "2":
			if installed {
				err = installer.Service("stop")
			} else {
				err = doctorCmd(opts)
			}
		case "3":
			if installed {
				err = installer.Service("restart")
			} else {
				err = runScript(opts, "backup.sh")
			}
		case "4":
			if installed {
				err = configCmd(opts, opts.In, opts.Out)
			} else {
				err = drillCmd(opts)
			}
		case "5":
			if installed {
				err = doctorCmd(opts)
			} else {
				helpCmd(opts)
			}
		case "6":
			if installed {
				err = logsCmd()
			}
		case "7":
			if installed {
				err = runScript(opts, "backup.sh")
			}
		case "8":
			if installed {
				err = drillCmd(opts)
			}
		case "9":
			if installed {
				err = restoreCmd(opts, scanner)
			}
		case "10":
			if installed {
				err = upgradeCmd(opts, scanner)
			}
		case "11":
			if installed {
				err = rollbackCmd(opts)
			}
		case "12":
			if installed {
				err = resetAdminCmd(opts, root, scanner)
			}
		case "13":
			if installed {
				err = uninstallCmd(opts, scanner)
			}
		case "14":
			helpCmd(opts)
		default:
			fmt.Fprintf(opts.Out, "未知选项 %q\n", choice)
			continue
		}
		if err != nil {
			fmt.Fprintf(opts.Out, "操作失败：%v\n", err)
		}
	}
}

func modeLabel(installed bool) string {
	if installed {
		return "已安装"
	}
	return "便携模式"
}

func dataDir(opts Options) string {
	if opts.DataDir != "" {
		return opts.DataDir
	}
	if installed := installer.Installed(opts.Prefix); installed {
		return filepath.Join(opts.Prefix, "data")
	}
	exe, err := os.Executable()
	if err == nil {
		return filepath.Join(filepath.Dir(exe), "data")
	}
	return "data"
}

func healthLine(port string) string {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return "无响应"
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return "OK"
	}
	return fmt.Sprintf("HTTP %d", resp.StatusCode)
}

func doctorCmd(opts Options) error {
	args := []string{"doctor", "-data", dataDir(opts)}
	cmd := exec.Command(binaryPath(opts), args...)
	cmd.Stdout, cmd.Stderr = opts.Out, opts.Out
	return cmd.Run()
}

func binaryPath(opts Options) string {
	p := filepath.Join(opts.Prefix, "rapig")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	return "rapig"
}

func configCmd(opts Options, in io.Reader, out io.Writer) error {
	if !installer.IsRoot() {
		return fmt.Errorf("修改配置需要 root：sudo rapig config")
	}
	return installer.ConfigEdit(opts.Prefix, in, out)
}

func runScript(opts Options, name string, args ...string) error {
	path := filepath.Join(opts.Prefix, "scripts", name)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("未找到 %s（安装包应包含 scripts/）", path)
	}
	cmd := exec.Command("bash", append([]string{path}, args...)...)
	cmd.Stdout, cmd.Stderr = opts.Out, opts.Out
	cmd.Env = append(os.Environ(),
		"RAPIG_DATA_DIR="+dataDir(opts),
		"RAPIG_BACKUP_DIR="+filepath.Join(opts.Prefix, "backup"),
	)
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	return cmd.Run()
}

func logsCmd() error {
	cmd := exec.Command("journalctl", "-u", "rapig", "-n", "80", "--no-pager")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func restoreCmd(opts Options, scanner *bufio.Scanner) error {
	fmt.Fprint(opts.Out, "输入快照名（/opt/rapig/backup 下的目录名，如 20261028-120000）: ")
	if !scanner.Scan() {
		return nil
	}
	snapshot := filepath.Base(strings.TrimSpace(scanner.Text()))
	if snapshot == "" || snapshot == "." || snapshot == string(filepath.Separator) {
		return fmt.Errorf("未输入快照名")
	}
	fmt.Fprint(opts.Out, "恢复会覆盖当前数据，输入 yes 确认: ")
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "yes" {
		return fmt.Errorf("已取消")
	}
	return runScript(opts, "restore.sh", "--snapshot", snapshot)
}

func latestSnapshot(opts Options) (string, error) {
	matches, _ := filepath.Glob(filepath.Join(opts.Prefix, "backup", "*"))
	latest := ""
	for _, m := range matches {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() && m > latest {
			latest = m
		}
	}
	if latest == "" {
		return "", fmt.Errorf("没有可用备份快照，请先执行备份")
	}
	return filepath.Base(latest), nil
}

func drillCmd(opts Options) error {
	snapshot, err := latestSnapshot(opts)
	if err != nil {
		return err
	}
	return runScript(opts, "restore.sh", "--snapshot", snapshot, "--drill")
}

func upgradeCmd(opts Options, scanner *bufio.Scanner) error {
	fmt.Fprint(opts.Out, "输入新二进制路径（例如 /tmp/rapig）: ")
	if !scanner.Scan() {
		return nil
	}
	path := strings.TrimSpace(scanner.Text())
	if path == "" {
		return fmt.Errorf("未输入路径")
	}
	return installer.Upgrade(context.Background(), opts.Prefix, path, opts.Port)
}

func rollbackCmd(opts Options) error {
	matches, _ := filepath.Glob(filepath.Join(opts.Prefix, "rapig.bak-*"))
	if len(matches) == 0 {
		return fmt.Errorf("没有可回滚的备份（rapig.bak-*）")
	}
	newest := matches[0]
	for _, m := range matches {
		if m > newest {
			newest = m
		}
	}
	return installer.Upgrade(context.Background(), opts.Prefix, newest, opts.Port)
}

func uninstallCmd(opts Options, scanner *bufio.Scanner) error {
	fmt.Fprint(opts.Out, "卸载会删除程序、数据、服务与系统用户 rapig。输入 DELETE 确认: ")
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "DELETE" {
		return fmt.Errorf("已取消")
	}
	return installer.Uninstall(context.Background(), installer.Options{Prefix: opts.Prefix, User: opts.User, Port: opts.Port, Stdout: opts.Out, Stderr: opts.Out})
}

func resetAdminCmd(opts Options, root bool, scanner *bufio.Scanner) error {
	if !root {
		return fmt.Errorf("重置密码需要 root：sudo rapig")
	}
	fmt.Fprint(opts.Out, "管理员用户名 [admin]: ")
	username := "admin"
	if scanner.Scan() {
		if v := strings.TrimSpace(scanner.Text()); v != "" {
			username = v
		}
	}
	fmt.Fprint(opts.Out, "新密码（至少 8 位）: ")
	if !scanner.Scan() {
		return nil
	}
	pw := strings.TrimSpace(scanner.Text())
	if len(pw) < 8 {
		return fmt.Errorf("密码至少 8 位")
	}
	_ = installer.Service("stop")
	var out bytes.Buffer
	cmd := exec.Command("runuser", "-u", opts.User, "--",
		binaryPath(opts), "reset-admin-password", "-data", dataDir(opts), "-username", username, "-password-stdin")
	cmd.Stdin = strings.NewReader(pw + "\n")
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	_ = installer.Service("start")
	if err != nil {
		return fmt.Errorf("重置失败：%v\n%s", err, out.String())
	}
	fmt.Fprintf(opts.Out, "已重置 %s 的密码并撤销所有会话；服务已重启\n", username)
	return nil
}

func helpCmd(opts Options) {
	fmt.Fprintln(opts.Out, `等效命令：
  install     sudo rapig install [--prefix /opt/rapig] [--user rapig] [--port 50011] [--no-service] [--dry-run]
  uninstall   sudo rapig uninstall [--yes]
  service     rapig service status|start|stop|restart
  config      sudo rapig config [--show]
  doctor      rapig doctor [-json]
  upgrade     sudo rapig upgrade --binary <path>
  reset       sudo rapig reset-admin-password [-data <dir>] [-username admin] [-password-stdin]
  gen-unit    rapig gen-unit [--prefix ...] [--user ...] [--port ...]
  backup      bash <prefix>/scripts/backup.sh
  restore     bash <prefix>/scripts/restore.sh --snapshot <name> [--drill]`)
}

// runServeForeground starts the binary in the foreground (portable mode) and
// returns its exit code.
func runServeForeground(opts Options) int {
	cmd := exec.Command(binaryPath(opts), "serve")
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(opts.Out, "启动失败:", err)
		return 1
	}
	return 0
}
