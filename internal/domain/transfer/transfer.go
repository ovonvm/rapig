// Package transfer is the domain model for offline HTTP(S) downloads (M4.2).
package transfer

import "time"

// State is the lifecycle of an offline download job.
type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StatePaused    State = "paused"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateCanceled  State = "canceled"
)

// Kind distinguishes the download backend. Rows created before M6.1 default
// to KindHTTP.
type Kind string

const (
	KindHTTP Kind = "http"
	KindQB   Kind = "qb"
)

// ValidKind reports whether k is a known download backend.
func ValidKind(k string) bool {
	switch Kind(k) {
	case KindHTTP, KindQB:
		return true
	}
	return false
}

const (
	// MaxSize is the largest single file the downloader will store (10 GB,
	// same bound as uploads; decimal GB per ADR-14).
	MaxSize = 10 * 1000 * 1000 * 1000
	// MaxNameLength mirrors the file naming limit.
	MaxNameLength = 255
	// MaxTorrentSize is the largest .torrent file accepted for a qb upload
	// (20 MiB): real torrent files are far smaller and the body is buffered
	// in memory once per request.
	MaxTorrentSize = 20 << 20
	// Retention is how long finished jobs are kept before cleanup.
	Retention = 7 * 24 * time.Hour
	// MaxQBImportFiles bounds a multi-file torrent import (M6.1c). One row
	// per file is created in a single transaction; 2000 keeps that
	// transaction and the on-disk tree manageable.
	MaxQBImportFiles = 2000
	// MaxQBImportDepth bounds the directory depth below the torrent root.
	MaxQBImportDepth = 32
	// MaxQBImportPathLen bounds one relative path below the torrent root.
	MaxQBImportPathLen = 4096
)

// Transfer is one durable download job row.
type Transfer struct {
	ID              string
	OwnerID         int64
	ParentID        int64
	StoragePolicyID int64
	Name            string
	NameFolded      string
	SourceURL       string
	// Kind is the backend (http or qb). Empty/legacy rows mean http.
	Kind Kind
	// QBHash is the BitTorrent v1 infohash (lowercase hex) for qb jobs.
	QBHash string
	// QBCategory is the qBittorrent category for qb jobs.
	QBCategory    string
	Size          int64
	ReceivedBytes int64
	State         State
	Error         string
	NodeID        *int64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	StartedAt     *time.Time
	FinishedAt    *time.Time
}

// KindValue returns the effective kind, mapping legacy zero values to http.
func (t Transfer) KindValue() Kind {
	if t.Kind == "" {
		return KindHTTP
	}
	return t.Kind
}

// ValidState reports whether s is a known state.
func ValidState(s string) bool {
	switch State(s) {
	case StatePending, StateRunning, StatePaused, StateCompleted, StateFailed, StateCanceled:
		return true
	}
	return false
}

// Active reports whether the job is still worth running or can be paused.
func (t Transfer) Active() bool {
	return t.State == StatePending || t.State == StateRunning || t.State == StatePaused
}

// Terminal reports whether the job has finished (successfully or not).
func (t Transfer) Terminal() bool {
	return t.State == StateCompleted || t.State == StateFailed || t.State == StateCanceled
}

// Progress is a 0..1 fraction; 0 when the total size is unknown.
func (t Transfer) Progress() float64 {
	if t.Size <= 0 {
		return 0
	}
	p := float64(t.ReceivedBytes) / float64(t.Size)
	if p < 0 {
		return 0
	}
	if p > 1 {
		return 1
	}
	return p
}
