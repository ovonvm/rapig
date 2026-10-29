//go:build !windows

package main

import (
	"fmt"
	"os"
)

// runUninstallWindowsCLI is only available on Windows; the Linux flavour of
// `rapig uninstall` lives in installer_cli.go.
func runUninstallWindowsCLI(args []string) int {
	fmt.Fprintln(os.Stderr, "rapig uninstall: 仅 Windows 使用该入口")
	return 2
}
