// Package apperr defines the transport independent error type used across
// rapig. Every error surfaced to HTTP carries a stable machine code, a user
// facing message and an HTTP status; internal causes are wrapped and only
// logged, never sent to clients.
package apperr

import (
	"errors"
	"net/http"
)

// Code is a stable machine readable error code (snake_case).
type Code string

const (
	CodeInternal           Code = "internal"
	CodeBadRequest         Code = "bad_request"
	CodeUnauthenticated    Code = "unauthenticated"
	CodeNotFound           Code = "not_found"
	CodeInvalidName        Code = "invalid_name"
	CodeNameConflict       Code = "name_conflict"
	CodeParentNotFound     Code = "parent_not_found"
	CodeInvalidCredentials Code = "invalid_credentials"
	CodeCSRF               Code = "csrf_failed"
	CodeInvalidScope       Code = "invalid_scope"
	CodeFrontendNotBuilt   Code = "frontend_not_built"
	CodeMethodNotAllowed   Code = "method_not_allowed"

	// M1 additions (docs/08-api.md §4).
	CodePayloadTooLarge     Code = "payload_too_large"
	CodeQuotaExceeded       Code = "quota_exceeded"
	CodeInsufficientStorage Code = "insufficient_storage"
	CodeTooManyAttempts     Code = "too_many_attempts"
	CodeChecksumMismatch    Code = "checksum_mismatch"
	CodeInvalidState        Code = "invalid_state"
	CodeStaleWrite          Code = "stale_write"

	// M2 additions.
	CodeInvalidTarget Code = "invalid_target"

	// M3.2 additions.
	CodeShareExhausted Code = "share_exhausted"

	// M5.2b additions (self registration + three identifier identity).
	CodeForbidden Code = "forbidden"

	// M6.1 additions (qBittorrent offline downloads).
	CodeQBTUnavailable Code = "qbt_unavailable"
)

// Reason values are advisory machine hints carried beside the code. Clients
// must switch on Code; reasons exist so operators and scripts can tell apart
// cases that deliberately share one code (see docs/08-api.md). Documented
// reasons are stable: changing one is a breaking change.
const (
	ReasonUnknownEndpoint = "unknown_endpoint"
	ReasonMissingAsset    = "missing_asset"
	ReasonOriginMismatch  = "origin_mismatch"
	ReasonOutOfRange      = "out_of_range"
	ReasonInvalidID       = "invalid_id"
	ReasonOnlyDirectory   = "only_directory_supported"

	// M1 additions.
	ReasonUploadExpired    = "upload_expired"
	ReasonQuotaExceeded    = "quota_exceeded"
	ReasonChecksumMismatch = "checksum_mismatch"
	ReasonPartsIncomplete  = "parts_incomplete"
	ReasonPasswordReuse    = "password_reuse"
	ReasonLocked           = "locked"
	ReasonPasswordTooShort = "password_too_short"

	// M3.2 additions.
	ReasonInvalidExpiry    = "invalid_expiry"
	ReasonInvalidDownloads = "invalid_downloads"
	ReasonNotDirectory     = "not_directory"

	// M3.2.1 additions (share lifecycle + merged multi-node shares).
	ReasonInvalidStatus = "invalid_status"
	ReasonRootShare     = "root_not_shareable"
	ReasonNotRevoked    = "not_revoked"
	ReasonShareActive   = "share_active"
	ReasonShareExpired  = "share_expired"
	ReasonTooManyItems  = "too_many_items"
	ReasonCopyTooLarge  = "copy_too_large"

	// M5.2 admin additions.
	ReasonSelfTarget   = "self_target"
	ReasonLastAdmin    = "last_admin"
	ReasonInvalidRole  = "invalid_role"
	ReasonInvalidValue = "invalid_value"

	// M5.2b additions.
	ReasonRegistrationDisabled = "registration_disabled"
	ReasonIdentifierTaken      = "identifier_taken"
	ReasonRateLimited          = "rate_limited"
	ReasonRootAdmin            = "root_admin"
	ReasonRootAdminRequired    = "root_admin_required"

	// M5.3 additions (multiple storage policies).
	ReasonCrossPolicyOperation  = "cross_policy_operation"
	ReasonStorageInUse          = "storage_in_use"
	ReasonDefaultPolicy         = "default_policy"
	ReasonLastPolicy            = "last_policy"
	ReasonPathOverlap           = "path_overlap"
	ReasonNotWritable           = "not_writable"
	ReasonSymlinkRoot           = "symlink_root"
	ReasonStoragePolicyDisabled = "storage_policy_disabled"

	// M5.2c additions (self-service account deletion).
	ReasonUsernameReserved = "username_reserved"
	ReasonDeletionPending  = "deletion_pending"
	ReasonDeletionDisabled = "deletion_disabled"
	ReasonAlreadyDeleted   = "already_deleted"

	// M6.1 additions (qBittorrent offline downloads).
	ReasonInvalidMagnet = "invalid_magnet"
	ReasonInvalidKind   = "invalid_kind"

	// M6.1b additions (.torrent file uploads).
	ReasonInvalidTorrent   = "invalid_torrent"
	ReasonTorrentTooLarge  = "torrent_too_large"
	ReasonDuplicateTorrent = "duplicate_torrent"

	// P1 additions (组共享空间).
	// 成员权限非法：400 bad_request，reason group_perms_invalid。
	ReasonGroupPermsInvalid = "group_perms_invalid"
	// 组非空（还有节点/进行中的上传或离线任务/活跃分享）：409 invalid_state。
	ReasonGroupNotEmpty = "group_not_empty"
	// 组至少保留一个 manager：409 invalid_state。
	ReasonLastGroupManager = "last_group_manager"
	// 跨组操作（移动/复制到另一个组或个人空间）：409 invalid_state。
	ReasonCrossGroupOperation = "cross_group_operation"
)

// Error is a rapig application error.
type Error struct {
	Code       Code
	HTTPStatus int
	Message    string
	// Reason carries a machine hint for cases that share one code; it must
	// never leak the existence of other users' resources.
	Reason string
	// RetryAfter is an optional delay in seconds for 429 responses.
	RetryAfter int
	// Err is the wrapped cause, logged but never serialized.
	Err error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return string(e.Code) + ": " + e.Message + ": " + e.Err.Error()
	}
	return string(e.Code) + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// WithReason returns a shallow copy carrying reason.
func (e *Error) WithReason(reason string) *Error {
	cp := *e
	cp.Reason = reason
	return &cp
}

// WithCause returns a shallow copy wrapping err.
func (e *Error) WithCause(err error) *Error {
	cp := *e
	cp.Err = err
	return &cp
}

// WithRetryAfter returns a shallow copy carrying a Retry-After delay.
func (e *Error) WithRetryAfter(seconds int) *Error {
	cp := *e
	cp.RetryAfter = seconds
	return &cp
}

// New builds a custom application error.
func New(code Code, status int, message string) *Error {
	return &Error{Code: code, HTTPStatus: status, Message: message}
}

// From extracts an *Error from err, if any.
func From(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// CodeOf returns the application code of err, or CodeInternal for foreign
// errors.
func CodeOf(err error) Code {
	if e, ok := From(err); ok {
		return e.Code
	}
	return CodeInternal
}

// Predefined errors. They are returned as pointers; call WithReason/WithCause
// to derive variants instead of mutating shared values.
var (
	ErrInternal           = New(CodeInternal, http.StatusInternalServerError, "服务器内部错误")
	ErrBadRequest         = New(CodeBadRequest, http.StatusBadRequest, "请求参数不合法")
	ErrUnauthenticated    = New(CodeUnauthenticated, http.StatusUnauthorized, "请先登录")
	ErrNotFound           = New(CodeNotFound, http.StatusNotFound, "资源不存在")
	ErrInvalidName        = New(CodeInvalidName, http.StatusBadRequest, "名称不合法")
	ErrNameConflict       = New(CodeNameConflict, http.StatusConflict, "同名文件夹已存在")
	ErrParentNotFound     = New(CodeParentNotFound, http.StatusNotFound, "目标文件夹不存在")
	ErrInvalidCredentials = New(CodeInvalidCredentials, http.StatusUnauthorized, "用户名或密码错误")
	ErrCSRF               = New(CodeCSRF, http.StatusForbidden, "请求校验失败，请刷新页面重试")
	ErrInvalidScope       = New(CodeInvalidScope, http.StatusInternalServerError, "缺少访问范围（这是程序错误）")
	ErrFrontendNotBuilt   = New(CodeFrontendNotBuilt, http.StatusServiceUnavailable, "前端资源未构建")
	ErrMethodNotAllowed   = New(CodeMethodNotAllowed, http.StatusMethodNotAllowed, "方法不允许")

	// M1 additions.
	ErrPayloadTooLarge     = New(CodePayloadTooLarge, http.StatusRequestEntityTooLarge, "文件或分片超过上限")
	ErrQuotaExceeded       = New(CodeQuotaExceeded, http.StatusRequestEntityTooLarge, "存储配额不足")
	ErrInsufficientStorage = New(CodeInsufficientStorage, http.StatusInsufficientStorage, "服务器磁盘空间不足")
	ErrTooManyAttempts     = New(CodeTooManyAttempts, http.StatusTooManyRequests, "尝试次数过多，请稍后再试")
	ErrChecksumMismatch    = New(CodeChecksumMismatch, http.StatusUnprocessableEntity, "文件校验失败，请重试")
	ErrInvalidState        = New(CodeInvalidState, http.StatusConflict, "当前状态不允许该操作")
	ErrStaleWrite          = New(CodeStaleWrite, http.StatusConflict, "资源已被修改，请刷新后重试")
	// ErrDuplicateTorrent reports a .torrent qBittorrent already has.
	ErrDuplicateTorrent = New(CodeInvalidState, http.StatusConflict, "qBittorrent 中已存在相同的种子任务").WithReason(ReasonDuplicateTorrent)

	// M2 additions.
	ErrInvalidTarget = New(CodeInvalidTarget, http.StatusBadRequest, "目标不合法")

	// M3.2 additions.
	ErrShareExhausted = New(CodeShareExhausted, http.StatusGone, "分享下载次数已用完")

	// M5.2b additions.
	ErrForbidden = New(CodeForbidden, http.StatusForbidden, "没有权限执行该操作")

	// M6.1 additions.
	ErrQBTUnavailable = New(CodeQBTUnavailable, http.StatusServiceUnavailable, "qBittorrent 离线下载未启用")

	// P1 additions (组共享空间).
	ErrGroupPermsInvalid = New(CodeBadRequest, http.StatusBadRequest, "组成员权限不合法").
				WithReason(ReasonGroupPermsInvalid)
	ErrGroupNotEmpty = New(CodeInvalidState, http.StatusConflict, "组内仍有内容，无法删除").
				WithReason(ReasonGroupNotEmpty)
	ErrLastGroupManager = New(CodeInvalidState, http.StatusConflict, "组至少需要一个管理者").
				WithReason(ReasonLastGroupManager)
	ErrCrossGroupOperation = New(CodeInvalidState, http.StatusConflict, "不能在组与个人空间之间移动或复制").
				WithReason(ReasonCrossGroupOperation)
)
