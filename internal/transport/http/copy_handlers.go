package httpx

import (
	"net/http"

	"rapig/internal/domain/access"
)

// copyNodeRequest is the body of POST /api/v1/nodes/{id}/copy. All fields are
// optional: by default the copy lands next to the source with an automatic
// "X - 副本" name.
type copyNodeRequest struct {
	Parent *int64  `json:"parent"`
	Name   *string `json:"name"`
}

// handleCopyNode duplicates one file or directory subtree.
func (s *Server) handleCopyNode(w http.ResponseWriter, r *http.Request) {
	p, err := requireUser(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	src, err := s.loadNode(r, p)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.authz.Require(p, access.ActionWrite, resourceFor(src)); err != nil {
		s.writeError(w, r, err)
		return
	}
	var req copyNodeRequest
	// All fields are optional, so an empty body means "copy next to the
	// source with an automatic name".
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &req); err != nil {
			s.writeError(w, r, err)
			return
		}
	}
	node, err := s.files.Copy(r.Context(), p, src.ID, req.Parent, req.Name)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.audit(r, p, "node.copy", "node", node.ID, mustJSON(map[string]any{"name": node.Name}))
	s.writeJSON(w, http.StatusCreated, s.nodeForAPI(r, p, node))
}
