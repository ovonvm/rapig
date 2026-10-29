package httpx

import (
	"log/slog"

	"rapig/internal/domain/access"
	"rapig/internal/platform/apperr"
)

// Authorizer maps policy decisions onto API errors.
type Authorizer struct {
	log *slog.Logger
}

// NewAuthorizer builds the transport level authorizer.
func NewAuthorizer(log *slog.Logger) *Authorizer { return &Authorizer{log: log} }

// Require enforces the policy matrix. Every denial becomes 404: the API never
// leaks whether a foreign resource exists (there is no admin bypass).
func (a *Authorizer) Require(p access.Principal, action access.Action, res access.Resource) error {
	d := access.Check(p, action, res)
	if d.Allow {
		return nil
	}
	a.log.Info("authz denied",
		"actor_id", p.UserID,
		"role", p.Role,
		"action", string(action),
		"resource_kind", string(res.Kind),
		"resource_id", res.ID,
		"reason", d.Reason,
	)
	return apperr.ErrNotFound
}
