// Package shares implements public share links (M3.2): creation and revoke by
// the owner, plus anonymous read access through a token. Public methods derive
// an internal owner scope from the share row, so every node read still goes
// through the scoped repository; policy checks for owner actions live in the
// transport layer, while the subtree boundary is enforced here.
package shares

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"rapig/internal/app/files"
	"rapig/internal/domain/access"
	domainfiles "rapig/internal/domain/files"
	"rapig/internal/domain/share"
	"rapig/internal/infra/sql"
	"rapig/internal/platform/apperr"
	"rapig/internal/platform/idgen"
	"rapig/internal/platform/passwd"
)

const (
	secretSettingKey = "share_secret"

	// DefaultExpiry is used when the create request omits expires_in.
	DefaultExpiry = 7 * 24 * time.Hour

	verifyMaxFails  = 10
	verifyBlockTime = 10 * time.Minute
)

var expiryPresets = map[int64]bool{
	0:       true, // permanent
	3600:    true,
	86400:   true,
	604800:  true,
	2592000: true,
}

// Service is the share application service.
type Service struct {
	db    *sqlstore.DB
	files *files.Service
	log   *slog.Logger
	now   func() time.Time

	secretMu sync.Mutex
	secret   []byte

	limitMu sync.Mutex
	limit   map[string]*verifyAttempt
}

type verifyAttempt struct {
	fails   int
	blocked time.Time
}

// New builds the share service.
func New(db *sqlstore.DB, files *files.Service, log *slog.Logger) *Service {
	return &Service{db: db, files: files, log: log, now: time.Now, limit: make(map[string]*verifyAttempt)}
}

// CreateRequest is the owner create payload. NodeIDs (one or more) builds a
// merged share; NodeID is the single-node legacy form.
type CreateRequest struct {
	NodeID       int64
	NodeIDs      []int64
	Password     string
	ExpiresIn    *int64
	MaxDownloads int64
}

// Meta is what the public page may know about a share.
type Meta struct {
	Share     share.Share
	Node      domainfiles.Node
	Locked    bool
	ItemCount int
}

// Create makes a share for one of the caller's active nodes. Two or more nodes
// are merged under one token (kind "multi"); the root directory is never
// shareable because it would expose the whole account through one link.
func (s *Service) Create(ctx context.Context, p access.Principal, req CreateRequest) (share.Share, error) {
	ids, err := normalizeNodeIDs(req.NodeID, req.NodeIDs)
	if err != nil {
		return share.Share{}, err
	}
	if len(ids) > share.MaxItems {
		return share.Share{}, apperr.ErrInvalidTarget.WithReason(apperr.ReasonTooManyItems)
	}
	nodes := make([]domainfiles.Node, 0, len(ids))
	for _, id := range ids {
		node, err := s.files.Node(ctx, p, id)
		if err != nil {
			return share.Share{}, err
		}
		decision := access.Check(p, access.ActionShare, access.Resource{
			Kind: access.ResourceNode, ID: node.ID, OwnerID: node.OwnerID,
		})
		if !decision.Allow {
			return share.Share{}, apperr.ErrNotFound
		}
		if node.State != domainfiles.StateActive {
			return share.Share{}, apperr.ErrNotFound
		}
		if node.IsRoot() {
			return share.Share{}, apperr.ErrInvalidTarget.WithReason(apperr.ReasonRootShare)
		}
		if node.Kind != domainfiles.KindFile && node.Kind != domainfiles.KindDir {
			return share.Share{}, apperr.ErrInvalidTarget
		}
		nodes = append(nodes, node)
	}
	passwordHash := ""
	if req.Password != "" {
		if n := len([]rune(req.Password)); n < share.MinPasswordLen || n > share.MaxPasswordLen {
			return share.Share{}, apperr.ErrBadRequest.WithReason(apperr.ReasonPasswordTooShort)
		}
		h, err := passwd.Hash(req.Password)
		if err != nil {
			return share.Share{}, err
		}
		passwordHash = h
	}
	expiresAt, err := s.expiry(req.ExpiresIn)
	if err != nil {
		return share.Share{}, err
	}
	if req.MaxDownloads < 0 || req.MaxDownloads > share.MaxDownloadsLimit {
		return share.Share{}, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidDownloads)
	}
	kind := share.Kind(nodes[0].Kind)
	if len(nodes) > 1 {
		kind = share.KindMulti
	}
	sh := share.Share{
		ID:           idgen.MustToken(share.IDBytes),
		Token:        idgen.MustToken(share.TokenBytes),
		OwnerID:      p.UserID,
		NodeID:       nodes[0].ID,
		Kind:         kind,
		PasswordHash: passwordHash,
		ExpiresAt:    expiresAt,
		MaxDownloads: req.MaxDownloads,
		CreatedAt:    s.now(),
		Items:        ids,
	}
	created, err := s.db.CreateShare(ctx, access.ScopeFor(p), sh, ids)
	if err != nil {
		return share.Share{}, err
	}
	s.audit(ctx, p.UserID, "share.create", nodes[0].ID, map[string]string{
		"kind":  string(created.Kind),
		"items": strconv.Itoa(len(ids)),
	})
	return created, nil
}

// normalizeNodeIDs deduplicates ids, preserving order, and rejects empty input.
func normalizeNodeIDs(single int64, many []int64) ([]int64, error) {
	out := make([]int64, 0, len(many)+1)
	seen := make(map[int64]bool, len(many)+1)
	add := func(id int64) {
		if id <= 0 || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range many {
		add(id)
	}
	add(single)
	if len(out) == 0 {
		return nil, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID)
	}
	return out, nil
}

// List returns the caller's shares, newest first.
func (s *Service) List(ctx context.Context, p access.Principal, status string, limit, offset int) ([]share.Share, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	return s.db.ListShares(ctx, access.ScopeFor(p), status, limit, offset, s.now())
}

// Revoke disables a share immediately. Revoking twice is not an error.
func (s *Service) Revoke(ctx context.Context, p access.Principal, id string) error {
	sc := access.ScopeFor(p)
	sh, err := s.db.ShareByID(ctx, sc, id)
	if err != nil {
		return err
	}
	if _, err := s.db.RevokeShare(ctx, sc, id, s.now()); err != nil {
		return err
	}
	s.audit(ctx, p.UserID, "share.revoke", sh.NodeID, nil)
	return nil
}

// Restore re-enables a revoked share. Tokens are not rotated and counters are
// not reset (see docs/13 for the rationale); expired shares cannot be restored.
func (s *Service) Restore(ctx context.Context, p access.Principal, id string) (share.Share, error) {
	sc := access.ScopeFor(p)
	sh, err := s.db.ShareByID(ctx, sc, id)
	if err != nil {
		return share.Share{}, err
	}
	if sh.RevokedAt == nil {
		return share.Share{}, apperr.ErrInvalidState.WithReason(apperr.ReasonNotRevoked)
	}
	if sh.ExpiresAt != nil && !s.now().Before(*sh.ExpiresAt) {
		return share.Share{}, apperr.ErrInvalidState.WithReason(apperr.ReasonShareExpired)
	}
	ok, err := s.db.RestoreShare(ctx, sc, id, s.now())
	if err != nil {
		return share.Share{}, err
	}
	if !ok {
		return share.Share{}, apperr.ErrInvalidState
	}
	s.audit(ctx, p.UserID, "share.restore", sh.NodeID, nil)
	return s.db.ShareByID(ctx, sc, id)
}

// Purge physically removes a dead share. Active shares must be revoked first so
// one click cannot silently destroy a live link.
func (s *Service) Purge(ctx context.Context, p access.Principal, id string) error {
	sc := access.ScopeFor(p)
	sh, err := s.db.ShareByID(ctx, sc, id)
	if err != nil {
		return err
	}
	if sh.RevokedAt == nil && sh.Active(s.now()) {
		return apperr.ErrInvalidState.WithReason(apperr.ReasonShareActive)
	}
	ok, err := s.db.PurgeShare(ctx, sc, id, s.now())
	if err != nil {
		return err
	}
	if !ok {
		return apperr.ErrNotFound
	}
	s.audit(ctx, p.UserID, "share.purge", sh.NodeID, nil)
	return nil
}

// CleanupDead removes old revoked/expired rows; stats only.
func (s *Service) CleanupDead(ctx context.Context) (int64, error) {
	return s.db.DeleteDeadShares(ctx, s.now().Add(-share.StatsRetention))
}

// Meta resolves a public token. ticket is the raw cookie value (may be empty).
func (s *Service) Meta(ctx context.Context, token, ticket string) (Meta, error) {
	sh, node, err := s.loadActive(ctx, token)
	if err != nil {
		return Meta{}, err
	}
	locked := sh.HasPassword() && !s.ticketValid(sh, ticket)
	if !locked {
		if err := s.db.TouchVisit(ctx, sh.ID, s.now()); err != nil {
			s.log.Warn("share visit touch failed", "err", err)
		}
	}
	return Meta{Share: sh, Node: node, Locked: locked, ItemCount: sh.ItemCount()}, nil
}

// VerifyResult is returned after a correct password.
type VerifyResult struct {
	Share     share.Share
	Node      domainfiles.Node
	Ticket    string
	ExpiresAt time.Time
}

// Verify checks a share password and issues a short-lived ticket. Wrong
// passwords are rate limited per token+IP.
func (s *Service) Verify(ctx context.Context, token, password, ip string) (VerifyResult, error) {
	key := token + "|" + ip
	if retry, blocked := s.verifyBlocked(key); blocked {
		secs := int(retry.Seconds())
		if secs < 1 {
			secs = 1
		}
		return VerifyResult{}, apperr.ErrTooManyAttempts.WithRetryAfter(secs)
	}
	sh, node, err := s.loadActive(ctx, token)
	if err != nil {
		return VerifyResult{}, err
	}
	if sh.HasPassword() {
		if err := passwd.Verify(sh.PasswordHash, password); err != nil {
			if !errors.Is(err, passwd.ErrMismatch) {
				s.log.Warn("share password hash malformed", "share", sh.ID, "err", err)
			}
			s.recordVerifyFail(key)
			return VerifyResult{}, apperr.ErrInvalidCredentials
		}
		s.resetVerify(key)
	}
	ticket, exp, err := s.issueTicket(sh)
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{Share: sh, Node: node, Ticket: ticket, ExpiresAt: exp}, nil
}

// Nodes lists one public level. parentID 0 is the share root: for a single
// folder share that is the folder itself, for a merged share it is the member
// set. Deeper levels stay inside the member subtrees.
func (s *Service) Nodes(ctx context.Context, token, ticket string, parentID int64, limit, offset int) (Meta, domainfiles.Node, []domainfiles.Node, int, error) {
	meta, err := s.Meta(ctx, token, ticket)
	if err != nil {
		return Meta{}, domainfiles.Node{}, nil, 0, err
	}
	if meta.Locked {
		return Meta{}, domainfiles.Node{}, nil, 0, apperr.ErrUnauthenticated
	}
	sh := meta.Share
	if sh.Multi() && parentID == 0 {
		if limit <= 0 || limit > 200 {
			limit = 50
		}
		if offset < 0 {
			offset = 0
		}
		items := make([]domainfiles.Node, 0, len(sh.Items))
		for _, id := range sh.Items {
			n, err := s.lookupOwner(ctx, sh, id)
			if err != nil {
				return Meta{}, domainfiles.Node{}, nil, 0, err
			}
			items = append(items, n)
		}
		total := len(items)
		if offset > total {
			offset = total
		}
		end := offset + limit
		if end > total {
			end = total
		}
		return meta, domainfiles.Node{}, items[offset:end], total, nil
	}
	var parent domainfiles.Node
	switch {
	case parentID == 0 || parentID == meta.Node.ID:
		parent = meta.Node
	case sh.Multi():
		parent, err = s.lookupAny(ctx, sh, parentID)
		if err != nil {
			return Meta{}, domainfiles.Node{}, nil, 0, err
		}
	default:
		parent, err = s.lookup(ctx, sh, parentID)
		if err != nil {
			return Meta{}, domainfiles.Node{}, nil, 0, err
		}
	}
	if parent.Kind != domainfiles.KindDir {
		return Meta{}, domainfiles.Node{}, nil, 0, apperr.ErrBadRequest.WithReason(apperr.ReasonNotDirectory)
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	items, total, err := s.db.ListChildren(ctx, s.shareScope(ctx, sh), parent.ID, limit, offset)
	if err != nil {
		return Meta{}, domainfiles.Node{}, nil, 0, err
	}
	return meta, parent, items, total, nil
}

// Content resolves a downloadable file inside the shared node(s).
func (s *Service) Content(ctx context.Context, token, ticket string, nodeID int64) (share.Share, domainfiles.Node, string, string, error) {
	meta, err := s.Meta(ctx, token, ticket)
	if err != nil {
		return share.Share{}, domainfiles.Node{}, "", "", err
	}
	if meta.Locked {
		return share.Share{}, domainfiles.Node{}, "", "", apperr.ErrUnauthenticated
	}
	var start domainfiles.Node
	if meta.Share.Multi() {
		if nodeID == 0 {
			return share.Share{}, domainfiles.Node{}, "", "", apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidID)
		}
		start, err = s.lookupAny(ctx, meta.Share, nodeID)
	} else if nodeID == 0 || nodeID == meta.Node.ID {
		start = meta.Node
	} else {
		start, err = s.lookup(ctx, meta.Share, nodeID)
	}
	if err != nil {
		return share.Share{}, domainfiles.Node{}, "", "", err
	}
	if start.Kind != domainfiles.KindFile {
		return share.Share{}, domainfiles.Node{}, "", "", apperr.ErrBadRequest
	}
	node, sha, mime, err := s.db.FileContent(ctx, s.shareScope(ctx, meta.Share), start.ID)
	if err != nil {
		return share.Share{}, domainfiles.Node{}, "", "", err
	}
	return meta.Share, node, sha, mime, nil
}

// Consume records one download against the share budget. It returns
// apperr.ErrShareExhausted when the budget is gone.
func (s *Service) Consume(ctx context.Context, sh share.Share) error {
	ok, err := s.db.TryConsumeDownload(ctx, sh.ID, s.now())
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	// Distinguish "expired/revoked between check and consume" from "budget".
	fresh, err := s.db.ShareByToken(ctx, sh.Token)
	if err != nil {
		return apperr.ErrNotFound
	}
	if !fresh.Active(s.now()) {
		return apperr.ErrNotFound
	}
	return apperr.ErrShareExhausted
}

// ---- internals ----

func (s *Service) loadActive(ctx context.Context, token string) (share.Share, domainfiles.Node, error) {
	sh, err := s.db.ShareByToken(ctx, token)
	if err != nil {
		return share.Share{}, domainfiles.Node{}, err
	}
	if !sh.Active(s.now()) {
		return share.Share{}, domainfiles.Node{}, apperr.ErrNotFound
	}
	node, err := s.lookupOwner(ctx, sh, sh.NodeID)
	if err != nil {
		return share.Share{}, domainfiles.Node{}, err
	}
	if node.ID == 0 {
		return share.Share{}, domainfiles.Node{}, apperr.ErrNotFound
	}
	if !sh.Multi() {
		if node.Kind == domainfiles.KindFile {
			sh.Kind = share.KindFile
		} else {
			sh.Kind = share.KindDir
		}
	}
	return sh, node, nil
}

// lookupOwner loads a node by the share owner, without a subtree check.
func (s *Service) lookupOwner(ctx context.Context, sh share.Share, nodeID int64) (domainfiles.Node, error) {
	n, err := s.db.NodeByID(ctx, s.shareScope(ctx, sh), nodeID)
	if err != nil {
		return domainfiles.Node{}, apperr.ErrNotFound
	}
	if n.State != domainfiles.StateActive {
		return domainfiles.Node{}, apperr.ErrNotFound
	}
	return n, nil
}

// shareScope derives the repository scope used for public reads. A share of a
// group node must see that group, not just the owner's personal space; when the
// scope cannot be resolved the caller falls back to the owner scope, which then
// fails closed for group nodes.
func (s *Service) shareScope(ctx context.Context, sh share.Share) access.Scope {
	sc, err := s.db.ShareScope(ctx, sh.NodeID)
	if err != nil {
		return access.UserScope(sh.OwnerID, false)
	}
	return sc
}

// lookup loads a node and enforces the subtree boundary.
func (s *Service) lookup(ctx context.Context, sh share.Share, nodeID int64) (domainfiles.Node, error) {
	root, err := s.lookupOwner(ctx, sh, sh.NodeID)
	if err != nil {
		return domainfiles.Node{}, err
	}
	n, err := s.lookupOwner(ctx, sh, nodeID)
	if err != nil {
		return domainfiles.Node{}, err
	}
	if !within(root, n) {
		return domainfiles.Node{}, apperr.ErrNotFound
	}
	return n, nil
}

// lookupAny loads a node and enforces the member-set boundary of a merged
// share (any member subtree, including the member itself).
func (s *Service) lookupAny(ctx context.Context, sh share.Share, nodeID int64) (domainfiles.Node, error) {
	n, err := s.lookupOwner(ctx, sh, nodeID)
	if err != nil {
		return domainfiles.Node{}, err
	}
	ok, err := s.withinShare(ctx, sh, n)
	if err != nil {
		return domainfiles.Node{}, err
	}
	if !ok {
		return domainfiles.Node{}, apperr.ErrNotFound
	}
	return n, nil
}

// withinShare reports whether n belongs to the share's member set (or a
// descendant of a member directory).
func (s *Service) withinShare(ctx context.Context, sh share.Share, n domainfiles.Node) (bool, error) {
	if !sh.Multi() {
		root, err := s.lookupOwner(ctx, sh, sh.NodeID)
		if err != nil {
			return false, err
		}
		return within(root, n), nil
	}
	for _, id := range sh.Items {
		member, err := s.lookupOwner(ctx, sh, id)
		if err != nil {
			return false, err
		}
		if within(member, n) {
			return true, nil
		}
	}
	return false, nil
}

// within reports whether n is root itself or a descendant of root. Paths are
// relative ("a/b"); an empty root path is the owner's root directory, whose
// subtree is everything the owner has. The two nodes must also live in the same
// space: path prefixes alone could otherwise match a personal node and a group
// node that happen to share a name.
func within(root, n domainfiles.Node) bool {
	if !sameSpace(root, n) {
		return false
	}
	if root.ID == n.ID {
		return true
	}
	if root.Kind != domainfiles.KindDir {
		return false
	}
	if root.Path == "" {
		return true
	}
	return strings.HasPrefix(n.Path, root.Path+"/")
}

// sameSpace reports whether two nodes belong to the same storage space: both
// personal, or both members of the same group.
func sameSpace(a, b domainfiles.Node) bool {
	switch {
	case a.GroupID == nil && b.GroupID == nil:
		return true
	case a.GroupID == nil || b.GroupID == nil:
		return false
	default:
		return *a.GroupID == *b.GroupID
	}
}

func (s *Service) expiry(expiresIn *int64) (*time.Time, error) {
	if expiresIn == nil {
		t := s.now().Add(DefaultExpiry)
		return &t, nil
	}
	if !expiryPresets[*expiresIn] {
		return nil, apperr.ErrBadRequest.WithReason(apperr.ReasonInvalidExpiry)
	}
	if *expiresIn == 0 {
		return nil, nil
	}
	t := s.now().Add(time.Duration(*expiresIn) * time.Second)
	return &t, nil
}

func (s *Service) secretBytes(ctx context.Context) ([]byte, error) {
	s.secretMu.Lock()
	defer s.secretMu.Unlock()
	if s.secret != nil {
		return s.secret, nil
	}
	raw, ok, err := s.db.Setting(ctx, secretSettingKey)
	if err != nil {
		return nil, err
	}
	if !ok || raw == "" {
		b, err := idgen.Bytes(32)
		if err != nil {
			return nil, err
		}
		raw = base64.RawURLEncoding.EncodeToString(b)
		if err := s.db.SetSetting(ctx, secretSettingKey, raw); err != nil {
			return nil, err
		}
	}
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(b) == 0 {
		return nil, fmt.Errorf("shares: malformed share_secret setting")
	}
	s.secret = b
	return s.secret, nil
}

func (s *Service) issueTicket(sh share.Share) (string, time.Time, error) {
	secret, err := s.secretBytes(context.Background())
	if err != nil {
		return "", time.Time{}, err
	}
	exp := s.now().Add(share.TicketTTL)
	mac := signTicket(secret, sh.ID, exp)
	return strconv.FormatInt(exp.Unix(), 10) + "." + mac, exp, nil
}

func (s *Service) ticketValid(sh share.Share, value string) bool {
	if value == "" {
		return false
	}
	parts := strings.SplitN(value, ".", 2)
	if len(parts) != 2 {
		return false
	}
	expUnix, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || !s.now().Before(time.Unix(expUnix, 0)) {
		return false
	}
	secret, err := s.secretBytes(context.Background())
	if err != nil {
		return false
	}
	want := signTicket(secret, sh.ID, time.Unix(expUnix, 0))
	return hmac.Equal([]byte(want), []byte(parts[1]))
}

func signTicket(secret []byte, shareID string, exp time.Time) string {
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "%s|%d", shareID, exp.Unix())
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) verifyBlocked(key string) (time.Duration, bool) {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	ent, ok := s.limit[key]
	if !ok {
		return 0, false
	}
	if d := time.Until(ent.blocked); d > 0 {
		return d, true
	}
	if ent.fails == 0 && !ent.blocked.IsZero() {
		delete(s.limit, key)
	}
	return 0, false
}

func (s *Service) recordVerifyFail(key string) {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	now := s.now()
	if len(s.limit) > 4096 {
		for k, v := range s.limit {
			if now.After(v.blocked) && v.fails == 0 {
				delete(s.limit, k)
			}
		}
	}
	ent, ok := s.limit[key]
	if !ok {
		ent = &verifyAttempt{}
		s.limit[key] = ent
	}
	ent.fails++
	if ent.fails >= verifyMaxFails {
		ent.fails = 0
		ent.blocked = now.Add(verifyBlockTime)
	}
}

func (s *Service) resetVerify(key string) {
	s.limitMu.Lock()
	defer s.limitMu.Unlock()
	delete(s.limit, key)
}

func (s *Service) audit(ctx context.Context, actorID int64, action string, targetID int64, detail map[string]string) {
	ev := sqlstore.AuditEvent{
		At:         s.now(),
		ActorID:    actorID,
		Action:     action,
		TargetType: "node",
		TargetID:   targetID,
		Result:     "allow",
	}
	if len(detail) > 0 {
		var b strings.Builder
		b.WriteByte('{')
		first := true
		for k, v := range detail {
			if !first {
				b.WriteByte(',')
			}
			first = false
			fmt.Fprintf(&b, "%q:%q", k, v)
		}
		b.WriteByte('}')
		ev.Detail = b.String()
	}
	if err := s.db.InsertAudit(ctx, ev); err != nil {
		s.log.Warn("share audit failed", "action", action, "err", err)
	}
}
