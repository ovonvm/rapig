//go:build !windows

package installer

import "os"

// IsRoot reports whether the current process has root privileges.
func IsRoot() bool { return os.Geteuid() == 0 }
