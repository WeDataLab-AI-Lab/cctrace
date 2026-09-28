package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *PgStore) IngestProjectRules(ctx context.Context, req *ProjectRuleIngestRequest) (*ProjectRuleIngestResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("project rule ingest request is nil")
	}
	agent := strings.TrimSpace(req.Agent)
	if agent == "" {
		agent = "claude"
	}
	repositoryKey := firstNonEmpty(req.RepositoryKey, req.RepositoryID, req.ProjectHash)
	if repositoryKey == "" {
		return nil, fmt.Errorf("repository_key, repository_id or project_hash required")
	}

	// Ownership gate: the caller must have prior session history for this
	// repository under their own identity. Without this, any API-key holder
	// could inject or overwrite rule versions for another team's repository_key.
	owned, err := s.callerOwnsRepository(ctx, agent, req.UserID, req.ProfileEmail, req.RepositoryID, req.ProjectHash, repositoryKey)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, ErrProjectRuleAccessDenied
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	resp := &ProjectRuleIngestResponse{}
	for _, snap := range req.Rules {
		if snap == nil {
			continue
		}
		rulePath := strings.TrimSpace(snap.RulePath)
		if rulePath == "" {
			return nil, fmt.Errorf("rule_path required")
		}
		status := strings.TrimSpace(snap.Status)
		if status == "" {
			status = "active"
		}
		ruleScope := strings.TrimSpace(snap.RuleScope)
		if ruleScope == "" {
			ruleScope = "repository"
		}
		contentHash := strings.TrimSpace(snap.ContentHash)
		if contentHash == "" && snap.Content != "" {
			sum := sha256.Sum256([]byte(snap.Content))
			contentHash = fmt.Sprintf("sha256:%x", sum[:])
		}

		ruleID, currentHash, inserted, err := upsertProjectRuleRow(ctx, tx, agent, repositoryKey, req, snap, rulePath, ruleScope, status)
		if err != nil {
			return nil, err
		}
		if inserted {
			resp.InsertedRules++
		} else {
			resp.UpdatedRules++
		}

		insertedVersion := false
		if status == "active" && contentHash != "" && contentHash != currentHash {
			version, err := insertProjectRuleVersion(ctx, tx, ruleID, contentHash, req, snap)
			if err != nil {
				return nil, err
			}
			if version != nil {
				insertedVersion = true
				resp.InsertedVersions++
				if _, err := tx.Exec(ctx, `UPDATE project_rules SET
					current_version_id = $1,
					current_content_hash = $2,
					current_status = $3,
					last_seen_at = now(),
					updated_at = now()
					WHERE id = $4`,
					version.ID, contentHash, status, ruleID,
				); err != nil {
					return nil, err
				}
				if err := pruneProjectRuleVersions(ctx, tx, ruleID, version.ID); err != nil {
					return nil, err
				}
			}
		}
		if !insertedVersion {
			if _, err := tx.Exec(ctx, `UPDATE project_rules SET
				current_status = $1,
				current_content_hash = CASE WHEN $2 != '' THEN $2 ELSE current_content_hash END,
				last_seen_at = now(),
				updated_at = now()
				WHERE id = $3`,
				status, contentHash, ruleID,
			); err != nil {
				return nil, err
			}
			if !inserted {
				resp.UnchangedRules++
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return resp, nil
}

func upsertProjectRuleRow(ctx context.Context, tx pgx.Tx, agent, repositoryKey string, req *ProjectRuleIngestRequest, snap *ProjectRuleSnapshot, rulePath, ruleScope, status string) (int64, string, bool, error) {
	var id int64
	var currentHash string
	err := tx.QueryRow(ctx, `SELECT id, current_content_hash
		FROM project_rules
		WHERE agent = $1 AND repository_key = $2 AND rule_path = $3
		FOR UPDATE`, agent, repositoryKey, rulePath).Scan(&id, &currentHash)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE project_rules SET
			project_hash = CASE WHEN $1 != '' THEN $1 ELSE project_hash END,
			project_name = CASE WHEN $2 != '' THEN $2 ELSE project_name END,
			repository_id = CASE WHEN $3 != '' THEN $3 ELSE repository_id END,
			repository_name = CASE WHEN $4 != '' THEN $4 ELSE repository_name END,
			rule_kind = CASE WHEN $5 != '' THEN $5 ELSE rule_kind END,
			rule_scope = CASE WHEN $6 != '' THEN $6 ELSE rule_scope END,
			title = CASE WHEN $7 != '' THEN $7 ELSE title END,
			current_status = $8,
			last_seen_at = now(),
			updated_at = now()
			WHERE id = $9`,
			req.ProjectHash, req.ProjectName, req.RepositoryID, req.RepositoryName,
			snap.RuleKind, ruleScope, snap.Title, status, id,
		)
		return id, currentHash, false, err
	}
	if err != pgx.ErrNoRows {
		return 0, "", false, err
	}

	err = tx.QueryRow(ctx, `INSERT INTO project_rules
		(agent, project_hash, project_name, repository_id, repository_key, repository_name,
		 rule_path, rule_kind, rule_scope, title, current_status, discovered_at, last_seen_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now(),now(),now())
		RETURNING id`,
		agent, req.ProjectHash, req.ProjectName, req.RepositoryID, repositoryKey, req.RepositoryName,
		rulePath, snap.RuleKind, ruleScope, snap.Title, status,
	).Scan(&id)
	if err != nil {
		return 0, "", false, err
	}
	return id, "", true, nil
}

func insertProjectRuleVersion(ctx context.Context, tx pgx.Tx, ruleID int64, contentHash string, req *ProjectRuleIngestRequest, snap *ProjectRuleSnapshot) (*ProjectRuleVersion, error) {
	appliesTo, err := json.Marshal(nonNilStringSlice(snap.AppliesTo))
	if err != nil {
		return nil, err
	}
	frontmatter, err := json.Marshal(nonNilMap(snap.Frontmatter))
	if err != nil {
		return nil, err
	}
	rawMetadata := nonNilMap(snap.RawMetadata)
	if snap.ReadError != "" {
		rawMetadata["read_error"] = snap.ReadError
	}
	raw, err := json.Marshal(rawMetadata)
	if err != nil {
		return nil, err
	}

	var nextVersion int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(version_number), 0) + 1 FROM project_rule_versions WHERE rule_id = $1`,
		ruleID,
	).Scan(&nextVersion); err != nil {
		return nil, err
	}

	version := &ProjectRuleVersion{}
	err = tx.QueryRow(ctx, `INSERT INTO project_rule_versions
		(rule_id, version_number, content_hash, content, size_bytes, commit_sha, branch,
		 applies_to, frontmatter, raw_metadata, created_by_profile_email, created_by_user_id, discovered_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10::jsonb,$11,$12,now())
		ON CONFLICT (rule_id, content_hash) DO NOTHING
		RETURNING id, rule_id, version_number, content_hash, content, size_bytes, change_reason,
			commit_sha, branch, applies_to, frontmatter, raw_metadata,
			created_by_profile_email, created_by_user_id, discovered_at`,
		ruleID, nextVersion, contentHash, snap.Content, snap.SizeBytes, req.CommitSHA, req.Branch,
		string(appliesTo), string(frontmatter), string(raw), req.ProfileEmail, req.UserID,
	).Scan(
		&version.ID, &version.RuleID, &version.VersionNumber, &version.ContentHash, &version.Content,
		&version.SizeBytes, &version.ChangeReason, &version.CommitSHA, &version.Branch,
		&appliesTo, &frontmatter, &raw, &version.CreatedByProfileEmail, &version.CreatedByUserID,
		&version.DiscoveredAt,
	)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decodeProjectRuleVersionJSON(version, appliesTo, frontmatter, raw)
	return version, nil
}

// maxVersionsPerRule caps retained snapshots per rule. Each version stores up to
// ~1MB of content, so unbounded history would grow without limit on every edit.
const maxVersionsPerRule = 100

// pruneProjectRuleVersions trims a rule's history to the newest maxVersionsPerRule
// snapshots, always preserving the current version. Comments referencing a pruned
// version keep their text (version_id is ON DELETE SET NULL).
func pruneProjectRuleVersions(ctx context.Context, tx pgx.Tx, ruleID, currentVersionID int64) error {
	_, err := tx.Exec(ctx, `DELETE FROM project_rule_versions
		WHERE rule_id = $1
		  AND id != $2
		  AND id NOT IN (
			SELECT id FROM project_rule_versions
			WHERE rule_id = $1
			ORDER BY version_number DESC
			LIMIT $3
		  )`, ruleID, currentVersionID, maxVersionsPerRule)
	return err
}
