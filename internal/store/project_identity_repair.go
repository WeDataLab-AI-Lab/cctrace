package store

import (
	"sort"
	"strings"
	"time"
	"unicode"

	"cctrace/internal/gitctx"
)

// ProjectIdentityRepairCategory is the read-only classification of one
// (agent, project_hash) scope.
type ProjectIdentityRepairCategory string

const (
	ProjectIdentityRepairRecoverable                ProjectIdentityRepairCategory = "recoverable"
	ProjectIdentityRepairHistoricalIdentityConflict ProjectIdentityRepairCategory = "historical_identity_conflict"
	ProjectIdentityRepairConflictingRemoteCategory  ProjectIdentityRepairCategory = "conflicting_remote"
	ProjectIdentityRepairLocalOnlyCategory          ProjectIdentityRepairCategory = "local_only"
	ProjectIdentityRepairBlankIdentity              ProjectIdentityRepairCategory = "blank_identity"
	ProjectIdentityRepairAlreadyCanonical           ProjectIdentityRepairCategory = "already_canonical"

	// Legacy names remain source-compatible for callers of the first dry-run
	// implementation. Their values use the stable Phase 3 categories above.
	ProjectIdentityRepairConflict = ProjectIdentityRepairConflictingRemoteCategory
	ProjectIdentityRepairLocal    = ProjectIdentityRepairLocalOnlyCategory
	ProjectIdentityRepairBlank    = ProjectIdentityRepairBlankIdentity
)

// ProjectIdentityRepairReason is a stable, sanitized explanation. It does not
// carry remote URLs, hashes, or conflicting identity values.
type ProjectIdentityRepairReason string

const (
	ProjectIdentityRepairUniqueRemote           ProjectIdentityRepairReason = "unique_remote_identity"
	ProjectIdentityRepairConflictingRemote      ProjectIdentityRepairReason = "conflicting_remote"
	ProjectIdentityRepairHistoricalConflict     ProjectIdentityRepairReason = "historical_identity_conflict"
	ProjectIdentityRepairCrossAgentConflict     ProjectIdentityRepairReason = "cross_agent_identity_conflict"
	ProjectIdentityRepairAlreadyCanonicalReason ProjectIdentityRepairReason = "already_canonical"
	ProjectIdentityRepairLocalOnly              ProjectIdentityRepairReason = "local_only"
	ProjectIdentityRepairMissingRemote          ProjectIdentityRepairReason = "blank_identity"
	ProjectIdentityRepairInvalidRemote          ProjectIdentityRepairReason = "invalid_remote_url"
	ProjectIdentityRepairInvalidRepositoryID    ProjectIdentityRepairReason = "invalid_repository_id"
)

// ProjectIdentityRepairRow is the complete projects-table evidence used by the
// classifier. The loader is intentionally separate from the pure classifier.
type ProjectIdentityRepairRow struct {
	Agent          string
	ProjectHash    string
	ProjectName    string
	GitRemoteURL   string
	RepositoryID   string
	RepositoryName string
	RepoSubpath    string
}

// ProjectIdentityRepairHistoryRow is one deterministic identity aggregate
// from session_records. RepoSubpath feeds only the snapshot checksum; the
// classifier ignores it because the repository is the identity.
type ProjectIdentityRepairHistoryRow struct {
	Agent        string
	ProjectHash  string
	RepositoryID string
	RepoSubpath  string
	RecordCount  int64
	FirstSeen    time.Time
	LastSeen     time.Time
}

// ProjectIdentityRepairProposal is emitted only when all remote evidence
// resolves to one identity. It carries no repo_subpath: the repository is the
// unit on every screen (#382, #692), so the subpath is neither evidence for
// nor against the identity, and the repair leaves the stored value alone.
type ProjectIdentityRepairProposal struct {
	RepositoryID   string `json:"repository_id"`
	RepositoryName string `json:"repository_name"`
}

type ProjectIdentityRepairResult struct {
	Agent       string
	ProjectHash string
	Category    ProjectIdentityRepairCategory
	Reason      ProjectIdentityRepairReason
	Proposal    *ProjectIdentityRepairProposal
}

type ProjectIdentityRepairCounts struct {
	Recoverable                int `json:"recoverable"`
	HistoricalIdentityConflict int `json:"historical_identity_conflict"`
	ConflictingRemote          int `json:"conflicting_remote"`
	LocalOnly                  int `json:"local_only"`
	BlankIdentity              int `json:"blank_identity"`
	AlreadyCanonical           int `json:"already_canonical"`

	// Deprecated compatibility counters. They are not emitted in the stable
	// JSON schema, but keep existing in-process callers source-compatible.
	Conflict int `json:"-"`
	Local    int `json:"-"`
	Blank    int `json:"-"`
}

// ProjectIdentityRepairPlan is produced only from an in-memory snapshot and
// has no persistence API.
type ProjectIdentityRepairPlan struct {
	Results      []ProjectIdentityRepairResult
	Counts       ProjectIdentityRepairCounts
	WriteQueries int
}

type projectIdentityRepairScope struct {
	agent       string
	projectHash string
}

// PlanProjectIdentityRepair classifies each exact (agent, project_hash) scope
// from the current projects snapshot. Historical evidence can be supplied with
// PlanProjectIdentityRepairWithHistory.
func PlanProjectIdentityRepair(rows []ProjectIdentityRepairRow) ProjectIdentityRepairPlan {
	return PlanProjectIdentityRepairWithHistory(rows, nil)
}

// PlanProjectIdentityRepairWithHistory classifies current projects together
// with identity history carried by session_records. The write key remains
// exactly (agent, project_hash), but a scope is unresolved whenever its
// historical identity cannot be represented by one projects row safely.
func PlanProjectIdentityRepairWithHistory(rows []ProjectIdentityRepairRow, history []ProjectIdentityRepairHistoryRow) ProjectIdentityRepairPlan {
	groups := make(map[projectIdentityRepairScope][]ProjectIdentityRepairRow)
	for _, row := range rows {
		scope := projectIdentityRepairScope{agent: row.Agent, projectHash: row.ProjectHash}
		groups[scope] = append(groups[scope], row)
	}
	historyGroups := make(map[projectIdentityRepairScope][]ProjectIdentityRepairHistoryRow)
	historyHashes := make(map[string]struct{})
	for _, row := range history {
		scope := projectIdentityRepairScope{
			agent:       row.Agent,
			projectHash: row.ProjectHash,
		}
		historyGroups[scope] = append(historyGroups[scope], row)
		historyHashes[row.ProjectHash] = struct{}{}
	}

	scopes := make([]projectIdentityRepairScope, 0, len(groups)+len(historyGroups))
	for scope := range groups {
		scopes = append(scopes, scope)
	}
	for scope := range historyGroups {
		if _, ok := groups[scope]; !ok {
			scopes = append(scopes, scope)
		}
	}
	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i].agent == scopes[j].agent {
			return scopes[i].projectHash < scopes[j].projectHash
		}
		return scopes[i].agent < scopes[j].agent
	})

	plan := ProjectIdentityRepairPlan{Results: make([]ProjectIdentityRepairResult, 0, len(scopes))}
	identitiesByHash := make(map[string]map[string]struct{})
	identitiesByScope := make(map[projectIdentityRepairScope]map[string]struct{})
	agentsByHashIdentity := make(map[string]map[string]map[string]struct{})
	for _, scope := range scopes {
		currentRows := groups[scope]
		historyRows := historyGroups[scope]
		result := classifyProjectIdentityRepairScope(scope, currentRows, historyRows)
		plan.Results = append(plan.Results, result)

		for _, identity := range projectIdentityRepairScopeRemoteIdentities(currentRows, historyRows) {
			hash := scope.projectHash
			if identitiesByHash[hash] == nil {
				identitiesByHash[hash] = make(map[string]struct{})
			}
			identitiesByHash[hash][identity] = struct{}{}
			if identitiesByScope[scope] == nil {
				identitiesByScope[scope] = make(map[string]struct{})
			}
			identitiesByScope[scope][identity] = struct{}{}
			if agentsByHashIdentity[hash] == nil {
				agentsByHashIdentity[hash] = make(map[string]map[string]struct{})
			}
			if agentsByHashIdentity[hash][identity] == nil {
				agentsByHashIdentity[hash][identity] = make(map[string]struct{})
			}
			agentsByHashIdentity[hash][identity][scope.agent] = struct{}{}
		}
	}

	// A hash shared by agents but backed by different remote identities is a
	// separate historical conflict. Do not merge the agents or propose either
	// identity; the projects primary key can only repair within one scope.
	if len(history) > 0 {
		for index := range plan.Results {
			result := &plan.Results[index]
			scope := projectIdentityRepairScope{agent: result.Agent, projectHash: result.ProjectHash}
			hash := result.ProjectHash
			if _, ok := historyHashes[hash]; !ok {
				continue
			}
			hashAgents := make(map[string]struct{})
			for _, agents := range agentsByHashIdentity[hash] {
				for agent := range agents {
					hashAgents[agent] = struct{}{}
				}
			}
			if len(identitiesByHash[hash]) > 1 && len(hashAgents) > 1 && len(identitiesByScope[scope]) > 0 {
				result.Category = ProjectIdentityRepairHistoricalIdentityConflict
				result.Reason = ProjectIdentityRepairCrossAgentConflict
				result.Proposal = nil
			}
		}
	}

	plan.Counts = projectIdentityRepairCounts(plan.Results)
	return plan
}

func classifyProjectIdentityRepairScope(scope projectIdentityRepairScope, rows []ProjectIdentityRepairRow, history []ProjectIdentityRepairHistoryRow) ProjectIdentityRepairResult {
	result := ProjectIdentityRepairResult{Agent: scope.agent, ProjectHash: scope.projectHash}
	currentRemoteIDs := make(map[string]struct{})
	historyRemoteIDs := make(map[string]struct{})
	localEvidence := false
	invalidRemoteURL := false
	invalidRepositoryID := false

	for _, row := range rows {
		if repositoryID := strings.TrimSpace(row.RepositoryID); repositoryID != "" {
			if isLocalProjectIdentity(repositoryID) {
				localEvidence = true
			} else if normalized := canonicalProjectIdentity(repositoryID); normalized != "" {
				currentRemoteIDs[normalized] = struct{}{}
			} else {
				invalidRepositoryID = true
			}
		}
		if remoteURL := strings.TrimSpace(row.GitRemoteURL); remoteURL != "" {
			normalized := canonicalProjectRemoteURL(remoteURL)
			if normalized == "" {
				invalidRemoteURL = true
			} else {
				currentRemoteIDs[normalized] = struct{}{}
			}
		}
	}
	for _, row := range history {
		if repositoryID := strings.TrimSpace(row.RepositoryID); repositoryID != "" && !isLocalProjectIdentity(repositoryID) {
			if normalized := canonicalProjectIdentity(repositoryID); normalized != "" {
				historyRemoteIDs[normalized] = struct{}{}
			}
		}
	}

	remoteIDs := make(map[string]struct{}, len(currentRemoteIDs)+len(historyRemoteIDs))
	for identity := range currentRemoteIDs {
		remoteIDs[identity] = struct{}{}
	}
	for identity := range historyRemoteIDs {
		remoteIDs[identity] = struct{}{}
	}
	identities := make([]string, 0, len(remoteIDs))
	for identity := range remoteIDs {
		if identity != "" {
			identities = append(identities, identity)
		}
	}
	sort.Strings(identities)

	switch {
	case len(historyRemoteIDs) > 1:
		result.Category = ProjectIdentityRepairHistoricalIdentityConflict
		result.Reason = ProjectIdentityRepairHistoricalConflict
	case len(currentRemoteIDs) > 1:
		result.Category = ProjectIdentityRepairConflictingRemoteCategory
		result.Reason = ProjectIdentityRepairConflictingRemote
	case len(history) > 0 && len(identities) > 1:
		result.Category = ProjectIdentityRepairHistoricalIdentityConflict
		result.Reason = ProjectIdentityRepairHistoricalConflict
	case len(identities) == 1:
		identity := identities[0]
		name := gitctx.RepositoryNameFromID(identity)
		if projectIdentityRepairRowsAlreadyCanonical(rows, identity, name) {
			result.Category = ProjectIdentityRepairAlreadyCanonical
			result.Reason = ProjectIdentityRepairAlreadyCanonicalReason
			break
		}
		result.Category = ProjectIdentityRepairRecoverable
		result.Reason = ProjectIdentityRepairUniqueRemote
		result.Proposal = &ProjectIdentityRepairProposal{RepositoryID: identity, RepositoryName: name}
	case localEvidence:
		result.Category = ProjectIdentityRepairLocalOnlyCategory
		result.Reason = ProjectIdentityRepairLocalOnly
	case invalidRemoteURL:
		result.Category = ProjectIdentityRepairBlankIdentity
		result.Reason = ProjectIdentityRepairInvalidRemote
	case invalidRepositoryID:
		result.Category = ProjectIdentityRepairBlankIdentity
		result.Reason = ProjectIdentityRepairInvalidRepositoryID
	default:
		result.Category = ProjectIdentityRepairBlankIdentity
		result.Reason = ProjectIdentityRepairMissingRemote
	}
	return result
}

func projectIdentityRepairCounts(results []ProjectIdentityRepairResult) ProjectIdentityRepairCounts {
	var counts ProjectIdentityRepairCounts
	for _, result := range results {
		switch result.Category {
		case ProjectIdentityRepairRecoverable:
			counts.Recoverable++
		case ProjectIdentityRepairHistoricalIdentityConflict:
			counts.HistoricalIdentityConflict++
			counts.Conflict++
		case ProjectIdentityRepairConflictingRemoteCategory:
			counts.ConflictingRemote++
			counts.Conflict++
		case ProjectIdentityRepairLocalOnlyCategory:
			counts.LocalOnly++
			counts.Local++
		case ProjectIdentityRepairBlankIdentity:
			counts.BlankIdentity++
			counts.Blank++
		case ProjectIdentityRepairAlreadyCanonical:
			counts.AlreadyCanonical++
		}
	}
	return counts
}

func projectIdentityRepairScopeRemoteIdentities(rows []ProjectIdentityRepairRow, history []ProjectIdentityRepairHistoryRow) []string {
	identities := make(map[string]struct{})
	for _, row := range rows {
		if raw := strings.TrimSpace(row.RepositoryID); raw != "" && !isLocalProjectIdentity(raw) {
			if identity := canonicalProjectIdentity(raw); identity != "" {
				identities[identity] = struct{}{}
			}
		}
		if identity := canonicalProjectRemoteURL(row.GitRemoteURL); identity != "" {
			identities[identity] = struct{}{}
		}
	}
	for _, row := range history {
		if raw := strings.TrimSpace(row.RepositoryID); raw != "" && !isLocalProjectIdentity(raw) {
			if identity := canonicalProjectIdentity(raw); identity != "" {
				identities[identity] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(identities))
	for identity := range identities {
		result = append(result, identity)
	}
	sort.Strings(result)
	return result
}

// projectIdentityRepairRowsAlreadyCanonical reports whether every current row
// already stores exactly what a repair would write. The stored text is compared,
// not its normalized form: the aggregates group on the stored repository_id, so
// a URL-shaped id that normalizes to the identity still splits the repository.
func projectIdentityRepairRowsAlreadyCanonical(rows []ProjectIdentityRepairRow, identity, name string) bool {
	if len(rows) == 0 {
		return false
	}
	for _, row := range rows {
		if strings.TrimSpace(row.RepositoryID) != identity || strings.TrimSpace(row.RepositoryName) != name {
			return false
		}
	}
	return true
}

func canonicalProjectIdentity(raw string) string {
	raw = strings.TrimSpace(raw)
	if normalized := canonicalProjectRemoteURL(raw); normalized != "" {
		return normalized
	}
	if strings.Contains(raw, "://") || strings.Contains(raw, "@") || strings.ContainsAny(raw, "\x00\r\n") {
		return ""
	}
	return raw
}

func canonicalProjectRemoteURL(raw string) string {
	normalized := gitctx.NormalizeRemoteURL(gitctx.SanitizeRemoteURL(raw))
	authority, _, _ := strings.Cut(normalized, "/")
	if strings.Contains(authority, "@") || strings.IndexFunc(normalized, unicode.IsControl) >= 0 {
		return ""
	}
	return normalized
}

func isLocalProjectIdentity(raw string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(raw)), "local:")
}
