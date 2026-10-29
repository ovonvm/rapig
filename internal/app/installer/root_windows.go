//go:build windows

package installer

// IsRoot is always false on Windows: there is no POSIX root here and the Linux
// installer commands refuse to run.
func IsRoot() bool { return false }
