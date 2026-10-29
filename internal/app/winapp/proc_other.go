//go:build !windows

package winapp

// The Windows desktop workflow has no Linux equivalent; keep the package
// compilable so `go build ./...` and the Linux release still work.
func spawnDetached(exe, logPath string, args ...string) error { return ErrUnsupported }

func processAlive(pid int) bool { return false }

func terminatePID(pid int, force bool) error { return ErrUnsupported }

func openURL(url string) error { return ErrUnsupported }

func createLogonTask(wrapper string) error { return ErrUnsupported }

func deleteLogonTask() error { return ErrUnsupported }

func logonTaskStatus() (bool, string) { return false, "" }
