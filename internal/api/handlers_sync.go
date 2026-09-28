package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"cctrace/internal/projecthash"
	"cctrace/internal/store"
)

type syncRequest struct {
	ProfileEmail       string `json:"profile_email"`
	LoginEmail         string `json:"login_email,omitempty"`
	UserID             string `json:"user_id"`
	Agent              string `json:"agent,omitempty"` // "claude" | "codex" | "gjc" | "omo"; defaults to "claude"
	ProjectHash        string `json:"project_hash"`
	ProjectName        string `json:"project_name,omitempty"`
	GitRemoteURL       string `json:"git_remote_url,omitempty"`
	RepositoryID       string `json:"repository_id,omitempty"`
	RepositoryIDSource string `json:"repository_id_source,omitempty"`
	RepositoryName     string `json:"repository_name,omitempty"`
	RepoSubpath        string `json:"repo_subpath,omitempty"`
	RepoSubpathPresent bool   `json:"repo_subpath_present,omitempty"`
	CommitSHA          string `json:"commit_sha,omitempty"`
	Branch             string `json:"branch,omitempty"`
	Reenrich           bool   `json:"reenrich,omitempty"`
	// UpdateStall is what the client says about its own self-update (#750).
	// Absent means the build predates the field, which is not the same as a
	// client reporting that nothing is wrong -- see store.ClientVersionUpdate.
	UpdateStall *store.ClientUpdateStall `json:"update_stall,omitempty"`
	Records     []*store.SessionRecord   `json:"records"`
}

// defaultBillingProviderByAgent gives the billing provider to assume for an
// agent kind when a record arrives with no billing_provider of its own. It is
// only a fallback: the per-record value, when the client sends one, always
// wins over this map (see handleSync below). That distinction matters most
// for "gjc" and "omo" - a single gjc session can carry records billed
// through "anthropic", "openai-codex", and "amazon-bedrock" depending on
// which backend served that particular turn, and a single omo (senpi)
// session likewise mixes "claude-sdk-oauth" with "openai-codex" (observed
// session logs: 515 claude-sdk-oauth records vs. 264 openai-codex records,
// 37.6M tokens, plus a couple of stragglers). Neither agent has a single true
// default; the entries below are weak fallbacks for the rare record that
// omits the field, not a statement of what gjc or omo actually bill
// through - the per-record billing_provider is authoritative. Unknown or
// empty agent values fall through to the same default as "claude" so that
// older or newer clients reporting an agent kind this server doesn't
// recognize yet are never rejected.
var defaultBillingProviderByAgent = map[string]string{
	"claude": "anthropic",
	"codex":  "openai",
	"omo":    "anthropic", // weak default only; per-record billing_provider is authoritative for omo
	"gjc":    "anthropic", // weak default only; per-record billing_provider is authoritative for gjc
}

func defaultBillingProviderFor(agent string) string {
	if provider, ok := defaultBillingProviderByAgent[agent]; ok {
		return provider
	}
	return "anthropic"
}

func (s *Server) handleSyncCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"reenrich": true})
}

// maxExclusionQueryAccounts caps one exclusion query. A client asks about the
// accounts its own pass saw, which is a handful; the cap keeps the route from
// becoming a bulk lookup over guessed ids.
const maxExclusionQueryAccounts = 100

type exclusionAccount struct {
	BillingProvider string `json:"billing_provider"`
	AccountID       string `json:"account_id"`
}

type exclusionQuery struct {
	Accounts []exclusionAccount `json:"accounts"`
}

// handleSyncExclusions tells a client which of the accounts it names are
// excluded from collection (#715), so it can stop sending them rather than have
// ingest discard them after the conversation text already crossed the network.
// Only the asked subset is answered: the exclusion list itself names other
// people's accounts and never leaves the server.
func (s *Server) handleSyncExclusions(w http.ResponseWriter, r *http.Request) {
	var q exclusionQuery
	if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if len(q.Accounts) > maxExclusionQueryAccounts {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": fmt.Sprintf("at most %d accounts per query", maxExclusionQueryAccounts)})
		return
	}
	excluded := []exclusionAccount{}
	if s.ingestBlock != nil {
		for _, a := range q.Accounts {
			if s.ingestBlock.AccountExcluded(a.BillingProvider, a.AccountID) {
				excluded = append(excluded, a)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": excluded})
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	clientVersion := r.Header.Get("X-Cctrace-Version")
	clientOS := r.Header.Get("X-Cctrace-Os")
	clientArch := r.Header.Get("X-Cctrace-Arch")
	var req syncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, fmt.Errorf("invalid request: %w", err))
		return
	}
	// Normalize envelope email via aliases
	req.ProfileEmail = s.aliases.Resolve(req.ProfileEmail)
	agent := req.Agent
	if agent == "" {
		agent = "claude"
	}
	// Fill in profile_email, login_email, user_id, agent and project_hash from request envelope if not set per-record
	for _, rec := range req.Records {
		if rec.ProfileEmail == "" {
			rec.ProfileEmail = req.ProfileEmail
		} else {
			rec.ProfileEmail = s.aliases.Resolve(rec.ProfileEmail)
		}
		if rec.LoginEmail == "" {
			rec.LoginEmail = req.LoginEmail
		}
		if rec.UserID == "" {
			rec.UserID = req.UserID
		}
		if rec.ProjectHash == "" {
			rec.ProjectHash = req.ProjectHash
		}
		// Normalise here rather than trusting the client: every agent and every
		// client version arrives at this handler, and clients older than v0.7.21
		// send a spelling that splits one directory across several keys (#303).
		rec.ProjectHash = projecthash.Repair(rec.ProjectHash)
		if rec.Agent == "" {
			rec.Agent = agent
		}
		if rec.BillingProvider == "" {
			rec.BillingProvider = defaultBillingProviderFor(rec.Agent)
		}
		if rec.RepositoryID == "" {
			rec.RepositoryID = req.RepositoryID
		}
		if rec.RepositoryIDSource == "" {
			rec.RepositoryIDSource = req.RepositoryIDSource
		}
		if rec.RepositoryName == "" {
			rec.RepositoryName = req.RepositoryName
		}
		if rec.RepoSubpath == "" {
			rec.RepoSubpath = req.RepoSubpath
		}
		if !rec.RepoSubpathPresent && req.RepoSubpathPresent {
			rec.RepoSubpathPresent = true
		}
		if rec.CommitSHA == "" {
			rec.CommitSHA = req.CommitSHA
		}
		if rec.Branch == "" {
			rec.Branch = req.Branch
		}
		if rec.CctraceVersion == "" {
			rec.CctraceVersion = clientVersion
		}
	}
	// Refuse what the dashboard deleted or blocked.
	//
	// This has to happen server-side. The client re-reads the same local JSONL on
	// every scan, so a session deleted here comes straight back on the next sync
	// unless ingest itself says no -- and the client is the half we cannot rely on
	// reaching, since an install built without main.updatePublicKeyBase64 can never
	// self-update to learn a new rule.
	//
	// Dropped records are reported as a successful sync with a skipped count, not as
	// an error. A 4xx here would put `sync --watch` into a retry loop against a
	// decision that is never going to change.
	skipped := 0
	if s.ingestBlock != nil {
		blockedProject := s.ingestBlock.ProjectBlocked(projecthash.Repair(req.ProjectHash))
		kept := req.Records[:0]
		excluded := 0
		for _, rec := range req.Records {
			if blockedProject || s.ingestBlock.ProjectBlocked(rec.ProjectHash) || s.ingestBlock.SessionDeleted(rec.SessionID) {
				skipped++
				continue
			}
			// An account excluded from collection (#715): its records are not
			// stored at all, not merely hidden. Codex records name the account by
			// billing id only; a record carrying a login address is judged by that
			// too. Data already stored stays, hidden -- this is the forward half.
			if s.ingestBlock.AccountExcluded(rec.BillingProvider, rec.AccountID) || s.ingestBlock.EmailExcluded(rec.LoginEmail) {
				skipped++
				excluded++
				continue
			}
			kept = append(kept, rec)
		}
		req.Records = kept
		// Nothing but an excluded account's records: its project would otherwise
		// appear in the selector, named, with nothing under it.
		if len(kept) == 0 && excluded > 0 {
			req.ProjectHash = ""
		}
		if blockedProject {
			// Skip UpsertProject too: recreating the projects row would put the project
			// back in the selector with no sessions behind it.
			req.ProjectHash = ""
		}
	}

	// Upsert project metadata (non-fatal)
	if req.ProjectHash != "" {
		var lastSessionAt time.Time
		for _, rec := range req.Records {
			if rec.Ts.After(lastSessionAt) {
				lastSessionAt = rec.Ts
			}
		}
		if err := s.store.UpsertProjectWithMetadata(r.Context(), agent, projecthash.Repair(req.ProjectHash), req.ProjectName, store.ProjectIdentityMetadata{
			GitRemoteURL:       req.GitRemoteURL,
			RepositoryID:       req.RepositoryID,
			RepositoryName:     req.RepositoryName,
			RepoSubpath:        req.RepoSubpath,
			RepositoryIDSource: req.RepositoryIDSource,
			RepoSubpathPresent: req.RepoSubpathPresent,
		}, lastSessionAt); err != nil {
			log.Printf("[api] upsert project: %v", err)
		}
	}
	// Record that this client synced, and the version it reported if any (non-fatal).
	//
	// Unconditional: a row must exist for every syncing client. Gating on a
	// non-empty header made a missing row mean either "never synced" or "synced
	// without a version header", and reading it as the former was wrong for six
	// prod accounts, one of them actively collecting (#455). An empty version now
	// means the client sent no header; only a missing row means no sync. Empty
	// values do not overwrite what the account last reported -- see
	// PgStore.UpsertClientVersion.
	//
	// The email guard stays: profile_email is the row's key, so a request without
	// one would write a single shared row that every such client overwrites. The
	// header check used to keep those out by accident.
	if req.ProfileEmail != "" {
		if err := s.store.UpsertClientVersion(r.Context(), store.ClientVersionUpdate{
			UserID:        req.UserID,
			ProfileEmail:  req.ProfileEmail,
			ClientVersion: clientVersion,
			ClientOS:      clientOS,
			ClientArch:    clientArch,
			UpdateStall:   cappedUpdateStall(req.UpdateStall),
		}); err != nil {
			log.Printf("[api] upsert client version: %v", err)
		}
	}
	if len(req.Records) == 0 {
		if req.Reenrich {
			writeJSON(w, http.StatusOK, map[string]int{"inserted": 0, "updated": 0, "skipped": skipped})
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"inserted": 0, "skipped": skipped})
		return
	}
	if req.Reenrich {
		updated, err := s.store.ReenrichSessionRecords(r.Context(), req.Records)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]int{"inserted": 0, "updated": updated, "skipped": skipped})
		return
	}
	if err := s.store.InsertSessionRecords(r.Context(), req.Records); err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"inserted": len(req.Records), "skipped": skipped})
}

func (s *Server) handleCheckUserID(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "user_id required"})
		return
	}
	exists, _, err := s.store.CheckUserID(r.Context(), userID)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"exists": exists})
}

// cappedUpdateStall trims the one field whose length the sender chooses. The
// store caps it again; this is the edge, where a request that is too long stops
// being the store's problem.
func cappedUpdateStall(st *store.ClientUpdateStall) *store.ClientUpdateStall {
	if st == nil {
		return nil
	}
	trimmed := *st
	if r := []rune(trimmed.Reason); len(r) > store.ClientUpdateStallReasonMax {
		trimmed.Reason = string(r[:store.ClientUpdateStallReasonMax])
	}
	return &trimmed
}
