// Package httpx is rapig's HTTP transport: router, middleware, handlers and
// JSON error mapping. Handlers never touch storage directly; they resolve
// resources, run the policy check and call application services.
package httpx

import (
	"io/fs"
	"log/slog"
	"net/http"

	accountsvc "rapig/internal/app/account"
	adminsvc "rapig/internal/app/admin"
	filessvc "rapig/internal/app/files"
	groupsvc "rapig/internal/app/groups"
	idsvc "rapig/internal/app/identity"
	sharesvc "rapig/internal/app/shares"
	transfersvc "rapig/internal/app/transfers"
	upsvc "rapig/internal/app/uploads"
	"rapig/internal/infra/sql"
	storagereg "rapig/internal/infra/storage"
	"rapig/internal/platform/config"
)

// Server wires services into an http.Handler.
type Server struct {
	cfg       *config.Config
	log       *slog.Logger
	db        *sqlstore.DB
	registry  *storagereg.Registry
	identity  *idsvc.Service
	files     *filessvc.Service
	uploads   *upsvc.Service
	shares    *sharesvc.Service
	transfers *transfersvc.Service
	admin     *adminsvc.Service
	account   *accountsvc.Service
	groups    *groupsvc.Service
	authz     *Authorizer

	webFS        fs.FS
	webAvailable bool
}

// New builds the HTTP server. webFS is the built SPA rooted at the directory
// that contains index.html; nil disables page serving.
func New(cfg config.Config, log *slog.Logger, db *sqlstore.DB, registry *storagereg.Registry, ids *idsvc.Service, fls *filessvc.Service, ups *upsvc.Service, shs *sharesvc.Service, trs *transfersvc.Service, adm *adminsvc.Service, acc *accountsvc.Service, grp *groupsvc.Service, webFS fs.FS) *Server {
	s := &Server{
		cfg:       &cfg,
		log:       log,
		db:        db,
		registry:  registry,
		identity:  ids,
		files:     fls,
		uploads:   ups,
		shares:    shs,
		transfers: trs,
		admin:     adm,
		account:   acc,
		groups:    grp,
		authz:     NewAuthorizer(log),
		webFS:     webFS,
	}
	if webFS != nil {
		if _, err := fs.Stat(webFS, "index.html"); err == nil {
			s.webAvailable = true
		}
	}
	return s
}

// Handler returns the fully wrapped HTTP handler.
//
// Middleware order: request-id -> recover -> logging -> authn -> csrf ->
// handlers (which run authz themselves).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", s.handleMe)
	mux.HandleFunc("GET /api/v1/auth/registration", s.handleRegistrationStatus)
	mux.HandleFunc("POST /api/v1/auth/register", s.handleRegister)
	mux.HandleFunc("POST /api/v1/auth/password", s.handleChangePassword)
	mux.HandleFunc("GET /api/v1/auth/deletion", s.handleDeletionStatus)
	mux.HandleFunc("POST /api/v1/auth/deletion/request", s.handleDeletionRequest)
	mux.HandleFunc("POST /api/v1/auth/deletion/cancel", s.handleDeletionCancel)
	mux.HandleFunc("GET /api/v1/nodes", s.handleListNodes)
	mux.HandleFunc("GET /api/v1/nodes/search", s.handleSearchNodes)
	mux.HandleFunc("POST /api/v1/nodes", s.handleCreateNode)
	mux.HandleFunc("DELETE /api/v1/nodes/{id}", s.handleDeleteNode)
	mux.HandleFunc("PATCH /api/v1/nodes/{id}", s.handleUpdateNode)
	mux.HandleFunc("POST /api/v1/nodes/{id}/restore", s.handleRestoreNode)
	mux.HandleFunc("POST /api/v1/nodes/{id}/copy", s.handleCopyNode)
	mux.HandleFunc("GET /api/v1/nodes/{id}/content", s.handleDownload)
	mux.HandleFunc("GET /api/v1/trash", s.handleListTrash)
	mux.HandleFunc("POST /api/v1/trash/empty", s.handleEmptyTrash)
	mux.HandleFunc("POST /api/v1/uploads", s.handleInitUpload)
	mux.HandleFunc("PUT /api/v1/uploads/{id}/parts/{part}", s.handlePutPart)
	mux.HandleFunc("POST /api/v1/uploads/{id}/complete", s.handleCompleteUpload)
	mux.HandleFunc("DELETE /api/v1/uploads/{id}", s.handleCancelUpload)
	mux.HandleFunc("POST /api/v1/shares", s.handleCreateShare)
	mux.HandleFunc("GET /api/v1/shares", s.handleListShares)
	mux.HandleFunc("DELETE /api/v1/shares/{id}", s.handleRevokeShare)
	mux.HandleFunc("POST /api/v1/shares/{id}/restore", s.handleRestoreShare)
	mux.HandleFunc("POST /api/v1/transfers", s.handleCreateTransfer)
	mux.HandleFunc("GET /api/v1/transfers", s.handleListTransfers)
	mux.HandleFunc("GET /api/v1/transfers/qb/status", s.handleQBTransferStatus)
	mux.HandleFunc("POST /api/v1/transfers/torrent", s.handleCreateTorrentTransfer)
	mux.HandleFunc("POST /api/v1/transfers/{id}/pause", s.handlePauseTransfer)
	mux.HandleFunc("POST /api/v1/transfers/{id}/resume", s.handleResumeTransfer)
	mux.HandleFunc("DELETE /api/v1/transfers/{id}", s.handleDeleteTransfer)
	mux.HandleFunc("GET /api/v1/admin/users", s.handleAdminListUsers)
	mux.HandleFunc("POST /api/v1/admin/users", s.handleAdminCreateUser)
	mux.HandleFunc("PATCH /api/v1/admin/users/{id}", s.handleAdminUpdateUser)
	mux.HandleFunc("POST /api/v1/admin/users/{id}/reset-password", s.handleAdminResetPassword)
	mux.HandleFunc("POST /api/v1/admin/users/{id}/restore", s.handleAdminRestoreUser)
	mux.HandleFunc("POST /api/v1/admin/users/{id}/purge", s.handleAdminPurgeUser)
	mux.HandleFunc("GET /api/v1/admin/audit", s.handleAdminAudit)
	mux.HandleFunc("GET /api/v1/admin/audit/actions", s.handleAdminAuditActions)
	mux.HandleFunc("GET /api/v1/admin/settings", s.handleAdminGetSettings)
	mux.HandleFunc("PUT /api/v1/admin/settings", s.handleAdminPutSettings)
	mux.HandleFunc("GET /api/v1/public/shares/{token}", s.handlePublicShareMeta)
	mux.HandleFunc("POST /api/v1/public/shares/{token}/verify", s.handlePublicShareVerify)
	mux.HandleFunc("GET /api/v1/public/shares/{token}/nodes", s.handlePublicShareNodes)
	mux.HandleFunc("GET /api/v1/public/shares/{token}/nodes/{id}/content", s.handlePublicShareContent)
	mux.HandleFunc("GET /api/v1/storage/policies", s.handleStoragePolicies)
	mux.HandleFunc("GET /api/v1/admin/storage", s.handleAdminListStorage)
	mux.HandleFunc("POST /api/v1/admin/storage", s.handleAdminCreateStorage)
	mux.HandleFunc("PATCH /api/v1/admin/storage/{id}", s.handleAdminUpdateStorage)
	mux.HandleFunc("DELETE /api/v1/admin/storage/{id}", s.handleAdminDeleteStorage)
	mux.HandleFunc("GET /api/v1/groups", s.handleListGroups)
	mux.HandleFunc("POST /api/v1/groups", s.handleCreateGroup)
	mux.HandleFunc("GET /api/v1/admin/groups", s.handleAdminListGroups)
	mux.HandleFunc("POST /api/v1/admin/groups", s.handleAdminCreateGroup)
	mux.HandleFunc("PATCH /api/v1/admin/groups/{id}", s.handleAdminUpdateGroup)
	mux.HandleFunc("DELETE /api/v1/admin/groups/{id}", s.handleAdminDeleteGroup)
	mux.HandleFunc("GET /api/v1/admin/groups/{id}/members", s.handleAdminListGroupMembers)
	mux.HandleFunc("POST /api/v1/admin/groups/{id}/members", s.handleAdminAddGroupMember)
	mux.HandleFunc("DELETE /api/v1/admin/groups/{id}/members/{user_id}", s.handleAdminRemoveGroupMember)
	// "/" is the SPA/static fallback; it also renders JSON 404 for unknown
	// /api paths. There is deliberately no catch-all proxy.
	mux.HandleFunc("/", s.handleFallback)

	var h http.Handler = mux
	h = s.withCSRF(h)
	h = s.withAuthn(h)
	h = withLogging(s.log, h)
	h = withRecover(s.log, h)
	h = withRequestID(h)
	return h
}
