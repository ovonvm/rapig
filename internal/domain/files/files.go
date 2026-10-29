// Package files contains the file tree domain model and name rules shared by
// transports, services and storage.
package files

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"rapig/internal/platform/apperr"
)

// Kind is the node type.
type Kind string

const (
	KindDir  Kind = "dir"
	KindFile Kind = "file"
)

// Node states.
const (
	StateActive  = "active"
	StateTrashed = "trashed"
	StatePurged  = "purged"
)

// MaxNameLength is the maximum node name length in runes.
const MaxNameLength = 255

// Node is a file tree entry.
type Node struct {
	ID              int64
	OwnerID         int64
	ParentID        *int64
	Kind            Kind
	Name            string
	NameFolded      string
	Path            string
	BlobID          *int64
	Size            int64
	StoragePolicyID *int64
	State           string
	// TrashedFrom is the parent the node was deleted from (nil while active).
	TrashedFrom *int64
	// TrashedAt is when the node was moved to the trash (nil while active).
	TrashedAt *time.Time
	// GroupID 非空表示节点属于某个组共享空间（P1）；nil 表示个人空间。
	GroupID *int64
	// Rev is bumped on every tree write; it is the optimistic locking token.
	Rev       int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsRoot reports whether n is an owner's root node.
func (n Node) IsRoot() bool { return n.ParentID == nil }

// Revision is the opaque concurrency token for M2 write operations (move,
// rename, delete, restore). Clients echo it back; a mismatch means someone
// else changed the node in between and the request must fail with stale_write.
func (n Node) Revision() string {
	return strconv.FormatInt(n.Rev, 10)
}

// ValidateName enforces the cross platform name rules: 1..255 runes, no path
// separators or Windows reserved characters, no control characters, and no
// trailing dot/space (Windows silently strips those, which would break
// lookups). Leading/trailing whitespace is preserved but trailing is rejected.
func ValidateName(name string) error {
	if name == "" {
		return apperr.ErrInvalidName.WithCause(fmt.Errorf("name is empty"))
	}
	if !utf8.ValidString(name) {
		return apperr.ErrInvalidName.WithCause(fmt.Errorf("name is not valid utf-8"))
	}
	if utf8.RuneCountInString(name) > MaxNameLength {
		return apperr.ErrInvalidName.WithCause(fmt.Errorf("name longer than %d runes", MaxNameLength))
	}
	if name == "." || name == ".." {
		return apperr.ErrInvalidName.WithCause(fmt.Errorf("name %q is reserved", name))
	}
	if strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return apperr.ErrInvalidName.WithCause(fmt.Errorf("name must not end with dot or space"))
	}
	for _, r := range name {
		if r == '/' || r == '\\' {
			return apperr.ErrInvalidName.WithCause(fmt.Errorf("name must not contain path separators"))
		}
		if r < 0x20 || r == 0x7f {
			return apperr.ErrInvalidName.WithCause(fmt.Errorf("name must not contain control characters"))
		}
		if strings.ContainsRune(`:*?"<>|`, r) {
			return apperr.ErrInvalidName.WithCause(fmt.Errorf("name must not contain %q", r))
		}
	}
	if isWindowsReservedName(name) {
		return apperr.ErrInvalidName.WithCause(fmt.Errorf("name %q is reserved on Windows", name))
	}
	return nil
}

// windowsReserved are the DOS device names Windows still refuses to create as
// ordinary files (case insensitive, with or without an extension). A file
// named "CON.txt" is as unusable as a bare "CON", so the check looks at the
// part before the first dot. COM0/LPT0 are included even though only COM1-9 /
// LPT1-9 are documented, because Windows treats the whole family as devices.
var windowsReserved = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com0": true, "com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt0": true, "lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

func isWindowsReservedName(name string) bool {
	base := name
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSpace(base)
	return windowsReserved[strings.ToLower(base)]
}

// FoldName is the case-insensitive comparison form used for sibling
// uniqueness. It lowercases with Unicode simple folding, which is stable
// across platforms (unlike filesystem specific folding).
func FoldName(name string) string { return strings.ToLower(name) }

// SanitizeDisplayName cleans a user supplied display name; empty is allowed.
func SanitizeDisplayName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > 64 {
		runes := []rune(s)
		s = string(runes[:64])
	}
	return s
}

// ChildPath joins a parent's relative path with a child name.
func ChildPath(parentPath, name string) string {
	if parentPath == "" {
		return name
	}
	return parentPath + "/" + name
}
