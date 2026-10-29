package files

import "time"

// UploadState is the lifecycle of a resumable upload session.
type UploadState string

const (
	UploadOpen      UploadState = "open"
	UploadCompleted UploadState = "completed"
	UploadCanceled  UploadState = "canceled"
)

// Upload limits. They are policy, not schema, so they live here for both the
// service and the handlers to share (docs/09-m1-plan.md §7).
const (
	// PartSizeDefault is the part size offered by POST /uploads.
	PartSizeDefault int64 = 8 << 20 // 8 MiB
	// MaxPartSize is the largest accepted part body.
	MaxPartSize int64 = 8 << 20
	// MaxFileSize caps a single logical file: 10 GB decimal (10_000_000_000
	// bytes). The unit is GB, not GiB, because that is the product unit the
	// user asked for (2026-10-27); quota display uses the same decimal unit.
	MaxFileSize int64 = 10 * 1000 * 1000 * 1000
	// MaxParts caps parts_total.
	MaxParts int64 = 10000
	// UploadTTL is how long an open upload survives before maintenance
	// cancels it and releases the reserved quota.
	UploadTTL = 24 * time.Hour
)

// Upload is one resumable upload session.
type Upload struct {
	ID              string
	OwnerID         int64
	ParentID        int64
	StoragePolicyID int64
	Name            string
	NameFolded      string
	Size            int64
	SHA256          string
	PartSize        int64
	PartsTotal      int64
	ReceivedBytes   int64
	State           UploadState
	NodeID          *int64
	CreatedAt       time.Time
	ExpiresAt       time.Time
}

// IsOpen reports whether the session still accepts parts.
func (u Upload) IsOpen() bool { return u.State == UploadOpen }

// UploadPart is one stored part row (metadata only, the bytes live in tmp/).
type UploadPart struct {
	PartNo int64
	Offset int64
	Size   int64
	SHA256 string
}

// ExpectedPartSize returns the number of bytes part n must contain for an
// upload of this size. The last part may be shorter; empty files have no
// parts at all.
func (u Upload) ExpectedPartSize(partNo int64) (int64, bool) {
	if partNo < 0 || partNo >= u.PartsTotal {
		return 0, false
	}
	offset := partNo * u.PartSize
	if u.Size == 0 {
		return 0, false
	}
	remaining := u.Size - offset
	if remaining <= 0 {
		return 0, false
	}
	if remaining > u.PartSize {
		return u.PartSize, true
	}
	return remaining, true
}
