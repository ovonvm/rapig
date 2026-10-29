package winapp

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"rapig/internal/app/installer"
)

// RunMenu shows the small Windows operator menu until the user quits.
func RunMenu(o Options) int {
	fmt.Println()
	fmt.Println("Rapig 运维菜单（Windows，关闭本窗口不会停止后台服务）")
	scanner := bufio.NewScanner(os.Stdin)
	for {
		fmt.Println()
		o.Status()
		fmt.Println("1) 启动   2) 停止   3) 重启   4) doctor 自检")
		fmt.Println("5) 日志   6) 重置管理员密码   7) 开机自启状态   8) 打开网页")
		fmt.Println("h) 等效命令帮助   0) 退出")
		fmt.Print("选择: ")
		if !scanner.Scan() {
			fmt.Println()
			return 0
		}
		choice := strings.TrimSpace(scanner.Text())
		fmt.Println()
		switch choice {
		case "0", "q", "quit":
			return 0
		case "1":
			if o.HealthOK() {
				fmt.Println("已经在运行:", o.URL())
				continue
			}
			if err := o.Start(); err != nil {
				fmt.Println("启动失败:", err)
				continue
			}
			if err := o.WaitHealthy(25 * time.Second); err != nil {
				fmt.Println("启动失败:", err)
			} else {
				fmt.Println("已启动:", o.URL())
			}
		case "2":
			if err := o.Stop(3 * time.Second); err != nil {
				fmt.Println("停止失败:", err)
			}
		case "3":
			_ = o.Stop(3 * time.Second)
			if err := o.Start(); err == nil {
				_ = o.WaitHealthy(25 * time.Second)
			}
			o.Status()
		case "4":
			runSelf("doctor")
		case "5":
			if err := TailLog(o.LogPath(), 80, os.Stdout); err != nil {
				fmt.Println("日志不可用:", err)
			}
		case "6":
			resetPassword(o, scanner)
		case "7":
			o.AutostartStatus()
		case "8":
			if err := OpenBrowser(o.URL()); err != nil {
				fmt.Println("请手动访问:", o.URL())
			}
		case "h", "help", "?":
			fmt.Println(`等效命令：
  rapig launch                 启动服务并打开网页（桌面快捷方式）
  rapig service status|start|stop|restart
  rapig doctor                 自检
  rapig logs [-n 80]           查看日志
  rapig autostart on|off|status
  rapig init-admin --password-stdin
  rapig config --show          查看配置/数据路径`)
		default:
			fmt.Println("未知选项:", choice)
		}
	}
}

func resetPassword(o Options, scanner *bufio.Scanner) {
	fmt.Print("管理员用户名 [admin]: ")
	if !scanner.Scan() {
		return
	}
	username := strings.TrimSpace(scanner.Text())
	if username == "" {
		username = "admin"
	}
	fmt.Println("重置前会停止服务（避免数据库被占用）。")
	if o.HealthOK() {
		if err := o.Stop(3 * time.Second); err != nil {
			fmt.Println("停止失败:", err)
			return
		}
	}
	fmt.Print("新密码（至少 8 个字符）: ")
	if !scanner.Scan() {
		return
	}
	password := strings.TrimSpace(scanner.Text())
	if len(password) < 8 {
		fmt.Println("密码太短")
		return
	}
	if err := installer.ResetAdminPassword(context.Background(), o.DataDir, "", username, password); err != nil {
		fmt.Println("重置失败:", err)
		return
	}
	fmt.Println("密码已重置；请重新启动服务（菜单 1）")
}

func runSelf(args ...string) {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "命令失败:", err)
	}
}
