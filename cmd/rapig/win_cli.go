package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"rapig/internal/app/winapp"
	"rapig/internal/platform/config"
)

func runtimeGOOS() string { return runtime.GOOS }

// launchOptions are the flags of `rapig launch` and friends.
type launchOptions struct {
	addr         string
	prefix       string
	dataDir      string
	password     bool
	passwordFile string
	lines        int
}

func splitLaunchFlags(args []string) ([]string, launchOptions) {
	var opts launchOptions
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case a == "-addr" || a == "--addr":
			opts.addr = next()
		case strings.HasPrefix(a, "-addr="):
			opts.addr = strings.TrimPrefix(a, "-addr=")
		case a == "-prefix" || a == "--prefix":
			opts.prefix = next()
		case strings.HasPrefix(a, "-prefix="):
			opts.prefix = strings.TrimPrefix(a, "-prefix=")
		case a == "-data" || a == "--data":
			opts.dataDir = next()
		case strings.HasPrefix(a, "-data="):
			opts.dataDir = strings.TrimPrefix(a, "-data=")
		case a == "--password-stdin":
			opts.password = true
		case a == "--password-file":
			opts.passwordFile = next()
		case strings.HasPrefix(a, "--password-file="):
			opts.passwordFile = strings.TrimPrefix(a, "--password-file=")
		case a == "-n" || a == "--lines":
			fmt.Sscanf(next(), "%d", &opts.lines)
		case strings.HasPrefix(a, "-n="):
			fmt.Sscanf(strings.TrimPrefix(a, "-n="), "%d", &opts.lines)
		default:
			out = append(out, a)
		}
	}
	return out, opts
}

func launchOptionsFromFlags(o launchOptions) (winapp.Options, error) {
	return winapp.OptionsFor(o.prefix, o.addr, o.dataDir)
}

// runLaunchCLI implements `rapig launch` (desktop shortcut / wizard entry).
func runLaunchCLI(args []string) int {
	_, flags := splitLaunchFlags(args)
	opts, err := launchOptionsFromFlags(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rapig launch:", err)
		return 1
	}
	cfg, err := config.Load([]string{"-data", opts.DataDir, "-addr", opts.Addr})
	if err != nil {
		fmt.Fprintln(os.Stderr, "rapig launch:", err)
		return 2
	}
	if _, err := winapp.EnsureAdminForLaunch(context.Background(), cfg, os.Stdin); err != nil {
		fmt.Fprintln(os.Stderr, "rapig launch:", err)
		return 1
	}
	return opts.Run(context.Background())
}

// runInitAdminCLI implements `rapig init-admin --password-stdin` used by the
// Windows installer (and useful for scripted provisioning).
func runInitAdminCLI(args []string) int {
	rest, flags := splitLaunchFlags(args)
	opts, err := launchOptionsFromFlags(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rapig init-admin:", err)
		return 1
	}
	cfg, err := config.Load(append([]string{"-data", opts.DataDir, "-addr", opts.Addr}, rest...))
	if err != nil {
		fmt.Fprintln(os.Stderr, "rapig init-admin:", err)
		return 2
	}
	password := ""
	if flags.password {
		sc := bufio.NewScanner(os.Stdin)
		if sc.Scan() {
			password = strings.TrimSpace(sc.Text())
		}
	}
	if flags.passwordFile != "" {
		raw, err := os.ReadFile(flags.passwordFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "rapig init-admin:", err)
			return 2
		}
		password = strings.TrimSpace(string(raw))
	}
	if password == "" {
		fmt.Fprintln(os.Stderr, "rapig init-admin: password required (use --password-stdin or --password-file)")
		return 2
	}
	created, err := winapp.InitAdmin(context.Background(), cfg, "admin", password)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rapig init-admin:", err)
		return 1
	}
	if !created {
		fmt.Println("administrator already exists; nothing changed")
	}
	return 0
}

// runAutostartCLI implements `rapig autostart on|off|status`.
func runAutostartCLI(args []string) int {
	rest, flags := splitLaunchFlags(args)
	opts, err := launchOptionsFromFlags(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rapig autostart:", err)
		return 1
	}
	action := "status"
	if len(rest) > 0 {
		action = rest[0]
	}
	switch action {
	case "on":
		if err := opts.AutostartOn(); err != nil {
			fmt.Fprintln(os.Stderr, "rapig autostart:", err)
			return 1
		}
		fmt.Println("autostart: on (per-user logon task created)")
	case "off":
		if err := opts.AutostartOff(); err != nil {
			fmt.Fprintln(os.Stderr, "rapig autostart:", err)
			return 1
		}
		fmt.Println("autostart: off")
	case "status":
		opts.AutostartStatus()
	default:
		fmt.Fprintln(os.Stderr, "usage: rapig autostart on|off|status")
		return 2
	}
	return 0
}

// runLogsCLI implements `rapig logs [-n N]`.
func runLogsCLI(args []string) int {
	_, flags := splitLaunchFlags(args)
	opts, err := launchOptionsFromFlags(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rapig logs:", err)
		return 1
	}
	if err := winapp.TailLog(opts.LogPath(), flags.lines, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "rapig logs:", err)
		return 1
	}
	return 0
}

// runWindowsServiceCLI implements the Windows flavour of `rapig service`.
func runWindowsServiceCLI(args []string) int {
	rest, flags := splitLaunchFlags(args)
	opts, err := launchOptionsFromFlags(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, "rapig service:", err)
		return 1
	}
	action := "status"
	if len(rest) > 0 {
		action = rest[0]
	}
	switch action {
	case "start":
		if opts.HealthOK() {
			fmt.Println("already running:", opts.URL())
			return 0
		}
		if err := opts.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "rapig service start:", err)
			return 1
		}
		if err := opts.WaitHealthy(25 * time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "rapig service start:", err)
			return 1
		}
		fmt.Println("started:", opts.URL())
	case "stop":
		if err := opts.Stop(5 * time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "rapig service stop:", err)
			return 1
		}
	case "restart":
		if err := opts.Stop(5 * time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "rapig service restart:", err)
			return 1
		}
		if err := opts.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "rapig service restart:", err)
			return 1
		}
		if err := opts.WaitHealthy(25 * time.Second); err != nil {
			fmt.Fprintln(os.Stderr, "rapig service restart:", err)
			return 1
		}
		fmt.Println("restarted:", opts.URL())
	case "status":
		opts.Status()
	default:
		fmt.Fprintln(os.Stderr, "usage: rapig service start|stop|restart|status")
		return 2
	}
	return 0
}

var _ = filepath.Join
