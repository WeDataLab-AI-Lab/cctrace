package api

import (
	"net/http"

	"cctrace/internal/store"
)

func (s *Server) handleListClientVersions(w http.ResponseWriter, r *http.Request) {
	if _, ok := adminFromRequest(w, r); !ok {
		return
	}

	versions, err := s.store.ListClientVersions(r.Context())
	if err != nil {
		writeErr(w, err)
		return
	}
	// A nil slice encodes as null, and the Clients tab spreads the list.
	if versions == nil {
		versions = []*store.ClientVersionRecord{}
	}
	writeJSON(w, http.StatusOK, versions)
}
