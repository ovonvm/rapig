package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"rapig/internal/app/installer"
	"rapig/internal/app/menu"
)

// parseCommandFlags is a tiny flag parser for the installer commands. The
// config package owns serve/worker flags; this parser only knows the flags a
// given command declares, so typos fail loudly instead of being ignored.
func parseCommandFlags(args []string, spec map[string]bool) (map[string]string, error) {
	out := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return nil, fmt.Errorf("unexpected argument %q", a)
		}
		name := strings.TrimLeft(a, "-")
		value := ""
		if k, v, ok := strings.Cut(name, "="); ok {
			name, value = k, v
		}
		takesValue, ok := spec[name]
		if !ok {
			return nil, fmt.Errorf("unknown flag -%s", name)
		}
		if takesValue {
			if value == "" {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("flag -%s needs a value", name)
				}
				i++
				value = args[i]
			}
		} else if value == "" {
			value = "true"
		}
		out[name] = value
	}
	return out, nil
}

func installerOptionsFromFlags(f map[string]string) installer.Options {
	opts := installer.Defaults()
	if v := f["prefix"]; v != "" {
		opts.Prefix = v
	}
	if v := f["user"]; v != "" {
		opts.User = v
	}
	if v := f["port"]; v != "" {
		opts.Port = v
	}
	if v := f["data"]; v != "" {
		opts.DataDir = v
	}
	opts.BinaryPath = f["binary"]
	opts.DryRun = f["dry-run"] == "true"
	opts.NoService = f["no-service"] == "true"
	opts.NoCredentials = f["no-credentials"] == "true"
	opts.Yes = f["yes"] == "true"
	return opts
}

func runInstallCLI(args []string) int {
	flags, err := parseCommandFlags(args, map[string]bool{
		"prefix": true, "user": true, "port": true, "data": true, "binary": true,
		"dry-run": false, "no-service": false, "no-credentials": false, "yes": false,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rapig install: %v\n", err)
		return 2
	}
	if _, err := installer.Install(context.Background(), installerOptionsFromFlags(flags)); err != nil {
		fmt.Fprintf(os.Stderr, "rapig install: %v\n", err)
		return 1
	}
	return 0
}

func runUninstallCLI(args []string) int {
	flags, err := parseCommandFlags(args, map[string]bool{
		"prefix": true, "user": true, "port": true, "data": true, "yes": false, "dry-run": false,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rapig uninstall: %v\n", err)
		return 2
	}
	if err := installer.Uninstall(context.Background(), installerOptionsFromFlags(flags)); err != nil {
		fmt.Fprintf(os.Stderr, "rapig uninstall: %v\n", err)
		return 1
	}
	return 0
}

func runServiceCLI(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: rapig service status|start|stop|restart")
		return 2
	}
	if err := installer.Service(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "rapig service: %v\n", err)
		return 1
	}
	if args[0] == "status" {
		fmt.Println("state:", installer.ServiceStatus())
	}
	return 0
}

func runConfigCLI(args []string) int {
	flags, err := parseCommandFlags(args, map[string]bool{"prefix": true, "show": false})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rapig config: %v\n", err)
		return 2
	}
	opts := installerOptionsFromFlags(flags)
	if flags["show"] == "true" {
		if err := installer.ConfigShow(opts.Prefix, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "rapig config: %v\n", err)
			return 1
		}
		return 0
	}
	if !installer.IsRoot() {
		fmt.Fprintln(os.Stderr, "rapig config: needs root; run: sudo rapig config")
		return 1
	}
	if err := installer.ConfigEdit(opts.Prefix, os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "rapig config: %v\n", err)
		return 1
	}
	return 0
}

func runGenUnitCLI(args []string) int {
	flags, err := parseCommandFlags(args, map[string]bool{"prefix": true, "user": true, "port": true, "data": true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rapig gen-unit: %v\n", err)
		return 2
	}
	opts := installerOptionsFromFlags(flags)
	fmt.Print(installer.GenUnit(opts.Prefix, opts.User, opts.Port, opts.DataDir))
	return 0
}

func runUpgradeCLI(args []string) int {
	flags, err := parseCommandFlags(args, map[string]bool{"prefix": true, "port": true, "binary": true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rapig upgrade: %v\n", err)
		return 2
	}
	opts := installerOptionsFromFlags(flags)
	if opts.BinaryPath == "" {
		fmt.Fprintln(os.Stderr, "usage: rapig upgrade --binary <path> [--prefix /opt/rapig] [--port 50011]")
		return 2
	}
	if err := installer.Upgrade(context.Background(), opts.Prefix, opts.BinaryPath, opts.Port); err != nil {
		fmt.Fprintf(os.Stderr, "rapig upgrade: %v\n", err)
		return 1
	}
	fmt.Println("upgrade ok:", opts.Prefix+"/rapig")
	return 0
}

func runResetAdminCLI(args []string) int {
	flags, err := parseCommandFlags(args, map[string]bool{
		"prefix": true, "data": true, "user": true, "port": true,
		"username": true, "password-stdin": false,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rapig reset-admin-password: %v\n", err)
		return 2
	}
	opts := installerOptionsFromFlags(flags)
	if opts.DataDir == "" {
		opts.DataDir = filepath.Join(opts.Prefix, "data")
	}
	username := flags["username"]
	if username == "" {
		username = "admin"
	}
	var password string
	if flags["password-stdin"] == "true" {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && io.EOF != err {
			fmt.Fprintf(os.Stderr, "rapig reset-admin-password: %v\n", err)
			return 1
		}
		password = strings.TrimSpace(line)
	} else {
		fmt.Print("new password: ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		password = strings.TrimSpace(line)
	}
	if err := installer.ResetAdminPassword(context.Background(), opts.DataDir, "", username, password); err != nil {
		fmt.Fprintf(os.Stderr, "rapig reset-admin-password: %v\n", err)
		return 1
	}
	fmt.Printf("password reset for %s; all sessions revoked\n", username)
	return 0
}

func runMenuCLI(args []string) int {
	flags, err := parseCommandFlags(args, map[string]bool{"prefix": true, "user": true, "port": true, "data": true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "rapig menu: %v\n", err)
		return 2
	}
	opts := installerOptionsFromFlags(flags)
	return menu.Run(context.Background(), menu.Options{
		Prefix:  opts.Prefix,
		User:    opts.User,
		Port:    opts.Port,
		DataDir: opts.DataDir,
	})
}

// isTerminal reports whether stdout and stdin are both character devices, the
// condition for showing the interactive menu instead of usage text.
func isTerminal() bool {
	out, err := os.Stdout.Stat()
	if err != nil || out.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	in, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return in.Mode()&os.ModeCharDevice != 0
}
