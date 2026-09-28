package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// callerOwnsRepository reports whether the given identity has at least one
// session_record for the target repository (matched by repository_id,
// project_hash, or repository_key). Empty identity never matches.
func (s *PgStore) callerOwnsRepository(ctx context.Context, agent, userID, profileEmail, repositoryID, projectHash, repositoryKey string) (bool, error) {
	if strings.TrimSpace(userID) == "" && strings.TrimSpace(profileEmail) == "" {
		return false, nil
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM session_records sr
		WHERE COALESCE(NULLIF(sr.agent,''), 'claude') = $1
		  AND (
			($2 != '' AND sr.user_id = $2)
			OR ($3 != '' AND sr.profile_email = $3)
		  )
		  AND (
			($4 != '' AND sr.repository_id = $4)
			OR ($5 != '' AND sr.project_hash = $5)
			OR ($6 != '' AND sr.repository_id = $6)
		  )
	)`, agent, userID, profileEmail, repositoryID, projectHash, repositoryKey).Scan(&exists)
	return exists, err
}

// ProjectRuleVisibleTo reports whether the given identity has session history
// for the rule's repository, mirroring the list visibility gate so detail and
// comment access cannot bypass it via direct id enumeration.
func (s *PgStore) ProjectRuleVisibleTo(ctx context.Context, ruleID int64, userID, profileEmail string) (bool, error) {
	if strings.TrimSpace(userID) == "" && strings.TrimSpace(profileEmail) == "" {
		return false, nil
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1
		FROM project_rules pr
		JOIN session_records sr
		  ON COALESCE(NULLIF(sr.agent,''), 'claude') = pr.agent
		 AND (
			(pr.repository_id != '' AND sr.repository_id = pr.repository_id)
			OR (pr.repository_id = '' AND sr.project_hash = pr.project_hash)
			OR (pr.repository_key != '' AND sr.repository_id = pr.repository_key)
		 )
		WHERE pr.id = $1
		  AND (
			($2 != '' AND sr.user_id = $2)
			OR ($3 != '' AND sr.profile_email = $3)
		  )
	)`, ruleID, userID, profileEmail).Scan(&exists)
	return exists, err
}

func (s *PgStore) CreateProjectRuleComment(ctx context.Context, ruleID int64, req *CreateProjectRuleCommentRequest, authorProfileEmail, authorUserID string) (*ProjectRuleComment, error) {
	if req == nil {
		return nil, fmt.Errorf("comment request is nil")
	}
	body := strings.TrimSpace(req.Body)
	if body == "" {
		return nil, fmt.Errorf("body required")
	}
	commentType := strings.TrimSpace(req.CommentType)
	if commentType == "" {
		commentType = "comment"
	}
	c := &ProjectRuleComment{}
	err := s.pool.QueryRow(ctx, `INSERT INTO project_rule_comments
		(rule_id, version_id, comment_type, author_profile_email, author_user_id, body, created_at, updated_at)
		VALUES ($1, NULLIF($2, 0), $3, $4, $5, $6, now(), now())
		RETURNING id, rule_id, COALESCE(version_id, 0), comment_type, author_profile_email, author_user_id, body, created_at, updated_at`,
		ruleID, req.VersionID, commentType, authorProfileEmail, authorUserID, body,
	).Scan(&c.ID, &c.RuleID, &c.VersionID, &c.CommentType, &c.AuthorProfileEmail, &c.AuthorUserID, &c.Body, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *PgStore) UpdateProjectRuleChangeReason(ctx context.Context, ruleID, versionID int64, changeReason, authorProfileEmail, authorUserID string) (*ProjectRuleVersion, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	version, err := updateProjectRuleVersionChangeReason(ctx, tx, ruleID, versionID, changeReason)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(changeReason) != "" {
		if _, err := tx.Exec(ctx, `INSERT INTO project_rule_comments
			(rule_id, version_id, comment_type, author_profile_email, author_user_id, body, created_at, updated_at)
			VALUES ($1, $2, 'change_reason', $3, $4, $5, now(), now())`,
			ruleID, versionID, authorProfileEmail, authorUserID, strings.TrimSpace(changeReason),
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return version, nil
}

func updateProjectRuleVersionChangeReason(ctx context.Context, tx pgx.Tx, ruleID, versionID int64, changeReason string) (*ProjectRuleVersion, error) {
	version := &ProjectRuleVersion{}
	var appliesTo, frontmatter, raw []byte
	err := tx.QueryRow(ctx, `UPDATE project_rule_versions
		SET change_reason = $1
		WHERE id = $2 AND rule_id = $3
		RETURNING id, rule_id, version_number, content_hash, content, size_bytes, change_reason,
			commit_sha, branch, applies_to, frontmatter, raw_metadata,
			created_by_profile_email, created_by_user_id, discovered_at`,
		strings.TrimSpace(changeReason), versionID, ruleID,
	).Scan(
		&version.ID, &version.RuleID, &version.VersionNumber, &version.ContentHash, &version.Content,
		&version.SizeBytes, &version.ChangeReason, &version.CommitSHA, &version.Branch,
		&appliesTo, &frontmatter, &raw, &version.CreatedByProfileEmail, &version.CreatedByUserID,
		&version.DiscoveredAt,
	)
	if err != nil {
		return nil, err
	}
	decodeProjectRuleVersionJSON(version, appliesTo, frontmatter, raw)
	return version, nil
}
