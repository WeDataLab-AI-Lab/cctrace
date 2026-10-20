package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"cctrace/internal/aireport"
)

// adminAIRunPolicyJSON is how report runs are admitted, already resolved
// against the defaults, with the layer each part came from.
type adminAIRunPolicyJSON struct {
	ConsentRequired       bool   `json:"consent_required"`
	ConsentRequiredSource string `json:"consent_required_source"`
	MaxConcurrent         int    `json:"max_concurrent"`
	MaxConcurrentSource   string `json:"max_concurrent_source"`
	// MaxConcurrentDefault is the server's value, shown beside an admin's;
	// MaxConcurrentMax is the top of the range the screen offers.
	MaxConcurrentDefault int `json:"max_concurrent_default"`
	MaxConcurrentMax     int `json:"max_concurrent_max"`
}

func runPolicyJSON(p aireport.RunPolicy) adminAIRunPolicyJSON {
	return adminAIRunPolicyJSON{
		ConsentRequired: p.ConsentRequired, ConsentRequiredSource: p.ConsentRequiredSource,
		MaxConcurrent: p.MaxConcurrent, MaxConcurrentSource: p.MaxConcurrentSource,
		MaxConcurrentDefault: p.DefaultMaxConcurrent, MaxConcurrentMax: aireport.MaxConcurrentLimit,
	}
}

// handleSetAdminAIRunPolicy saves the admin's run policy. The screen sends the
// whole policy; a null or missing field hands that part back to the default.
func (s *Server) handleSetAdminAIRunPolicy(w http.ResponseWriter, r *http.Request) {
	user, ok := adminAISession(w, r, true)
	if !ok {
		return
	}
	if s.aiReports == nil {
		writeAIError(w, http.StatusServiceUnavailable, "runtime_unconfigured", "AI 런타임이 설정되지 않았습니다")
		return
	}
	var body struct {
		ConsentRequired *bool `json:"consent_required"`
		MaxConcurrent   *int  `json:"max_concurrent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		writeAIError(w, http.StatusBadRequest, "invalid_request", "요청 본문을 읽을 수 없습니다")
		return
	}
	p, err := s.aiReports.SetRunPolicy(r.Context(), body.ConsentRequired, body.MaxConcurrent, user.Email)
	if errors.Is(err, aireport.ErrInvalidRunPolicy) {
		writeAIError(w, http.StatusBadRequest, "invalid_run_policy", fmt.Sprintf("동시 실행 수는 1~%d 여야 합니다", aireport.MaxConcurrentLimit))
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_policy": runPolicyJSON(p)})
}
