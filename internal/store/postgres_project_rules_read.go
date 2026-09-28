package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

func (s *PgStore) ListProjectRules(ctx context.Context, f ProjectRuleFilter) (*ProjectRuleListResponse, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := f.Offset
	if offset < 0 {
		offset = 0
	}

	cte, where, args := buildProjectRuleWhere(f)
	countQuery := fmt.Sprintf("%sSELECT COUNT(*) FROM project_rules pr %s", cte, where)
	var total int64
	if err := s.pool.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, err
	}

	n := len(args)
	n++
	limitArg := n
	args = append(args, limit)
	n++
	offsetArg := n
	args = append(args, offset)

	q := fmt.Sprintf(`%sSELECT
		pr.id, pr.agent, pr.project_hash, pr.project_name, pr.repository_id, pr.repository_key,
		pr.repository_name, pr.rule_path, pr.rule_kind, pr.rule_scope, pr.title,
		COALESCE(pr.current_version_id, 0), pr.current_content_hash, pr.current_status,
		pr.discovered_at, pr.last_seen_at, pr.updated_at,
		COALESCE(vc.version_count, 0), COALESCE(cc.comment_count, 0)
	FROM project_rules pr
	LEFT JOIN (
		SELECT rule_id, COUNT(*) AS version_count
		FROM project_rule_versions
		GROUP BY rule_id
	) vc ON vc.rule_id = pr.id
	LEFT JOIN (
		SELECT rule_id, COUNT(*) AS comment_count
		FROM project_rule_comments
		GROUP BY rule_id
	) cc ON cc.rule_id = pr.id
	%s
	ORDER BY pr.updated_at DESC, pr.rule_path ASC
	LIMIT $%d OFFSET $%d`, cte, where, limitArg, offsetArg)

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]*ProjectRuleListItem, 0)
	for rows.Next() {
		item := &ProjectRuleListItem{}
		if err := scanProjectRuleListItem(rows, item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &ProjectRuleListResponse{Items: items, Total: total}, nil
}

func (s *PgStore) GetProjectRuleDetail(ctx context.Context, id, contentVersionID int64) (*ProjectRuleDetail, error) {
	rule := &ProjectRuleListItem{}
	row := s.pool.QueryRow(ctx, `SELECT
		pr.id, pr.agent, pr.project_hash, pr.project_name, pr.repository_id, pr.repository_key,
		pr.repository_name, pr.rule_path, pr.rule_kind, pr.rule_scope, pr.title,
		COALESCE(pr.current_version_id, 0), pr.current_content_hash, pr.current_status,
		pr.discovered_at, pr.last_seen_at, pr.updated_at,
		COALESCE((SELECT COUNT(*) FROM project_rule_versions v WHERE v.rule_id = pr.id), 0),
		COALESCE((SELECT COUNT(*) FROM project_rule_comments c WHERE c.rule_id = pr.id), 0)
		FROM project_rules pr
		WHERE pr.id = $1`, id)
	if err := scanProjectRuleListItem(row, rule); err != nil {
		return nil, err
	}

	if contentVersionID == 0 {
		contentVersionID = rule.CurrentVersionID
	}
	versions, err := s.listProjectRuleVersions(ctx, id, contentVersionID)
	if err != nil {
		return nil, err
	}
	comments, err := s.listProjectRuleComments(ctx, id)
	if err != nil {
		return nil, err
	}
	return &ProjectRuleDetail{Rule: rule, Versions: versions, Comments: comments}, nil
}

func (s *PgStore) listProjectRuleVersions(ctx context.Context, ruleID, contentVersionID int64) ([]*ProjectRuleVersion, error) {
	rows, err := s.pool.Query(ctx, `SELECT
		id, rule_id, version_number, content_hash,
		CASE WHEN id = $2 THEN content ELSE '' END,
		size_bytes, change_reason,
		commit_sha, branch, applies_to, frontmatter, raw_metadata,
		created_by_profile_email, created_by_user_id, discovered_at
		FROM project_rule_versions
		WHERE rule_id = $1
		ORDER BY version_number DESC`, ruleID, contentVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*ProjectRuleVersion, 0)
	for rows.Next() {
		v := &ProjectRuleVersion{}
		var appliesTo, frontmatter, raw []byte
		if err := rows.Scan(
			&v.ID, &v.RuleID, &v.VersionNumber, &v.ContentHash, &v.Content,
			&v.SizeBytes, &v.ChangeReason, &v.CommitSHA, &v.Branch,
			&appliesTo, &frontmatter, &raw, &v.CreatedByProfileEmail, &v.CreatedByUserID,
			&v.DiscoveredAt,
		); err != nil {
			return nil, err
		}
		decodeProjectRuleVersionJSON(v, appliesTo, frontmatter, raw)
		result = append(result, v)
	}
	return result, rows.Err()
}

func (s *PgStore) listProjectRuleComments(ctx context.Context, ruleID int64) ([]*ProjectRuleComment, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, rule_id, COALESCE(version_id, 0),
		comment_type, author_profile_email, author_user_id, body, created_at, updated_at
		FROM project_rule_comments
		WHERE rule_id = $1
		ORDER BY created_at DESC, id DESC`, ruleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]*ProjectRuleComment, 0)
	for rows.Next() {
		c := &ProjectRuleComment{}
		if err := rows.Scan(&c.ID, &c.RuleID, &c.VersionID, &c.CommentType,
			&c.AuthorProfileEmail, &c.AuthorUserID, &c.Body, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

type projectRuleRow interface {
	Scan(dest ...interface{}) error
}

func scanProjectRuleListItem(row projectRuleRow, item *ProjectRuleListItem) error {
	return row.Scan(
		&item.ID, &item.Agent, &item.ProjectHash, &item.ProjectName, &item.RepositoryID,
		&item.RepositoryKey, &item.RepositoryName, &item.RulePath, &item.RuleKind,
		&item.RuleScope, &item.Title, &item.CurrentVersionID, &item.CurrentContentHash,
		&item.CurrentStatus, &item.DiscoveredAt, &item.LastSeenAt, &item.UpdatedAt,
		&item.VersionCount, &item.CommentCount,
	)
}

// buildProjectRuleWhere builds the optional `WITH owned AS (...)` CTE used for
// user/profile access scoping, the WHERE clause, and the positional args.
//
// The access filter restricts results to rules whose (agent, repository) the user
// actually touched. It was a correlated EXISTS over the session_records TimescaleDB
// hypertable, re-probed per rule row with no ts bound -> Append fan-out across all
// chunks (measured ~127s on a heavy user). It is rewritten to precompute the user's
// ownership tuples once (single scan, DISTINCT) into an `owned` CTE and semijoin
// against that small set. count and list share the same cte+where+args, so the access
// logic stays in one place. Note: a Postgres placeholder $N refers to args[N-1]
// regardless of where $N appears in the SQL text, so the CTE (emitted first) may
// reference an arg appended last.
func buildProjectRuleWhere(f ProjectRuleFilter) (cte string, where string, args []interface{}) {
	var conds []string
	n := 0
	add := func(cond string, v interface{}) {
		n++
		conds = append(conds, fmt.Sprintf(cond, n))
		args = append(args, v)
	}
	if f.Agent != "" {
		add("pr.agent = $%d", f.Agent)
	}
	if f.RepositoryKey != "" {
		add("pr.repository_key = $%d", f.RepositoryKey)
	}
	if f.RepositoryID != "" {
		add("pr.repository_id = $%d", f.RepositoryID)
	}
	if f.ProjectHash != "" {
		add("pr.project_hash = $%d", f.ProjectHash)
	}
	if f.Status != "" && f.Status != "all" {
		add("pr.current_status = $%d", f.Status)
	}
	if f.Query != "" {
		n++
		conds = append(conds, fmt.Sprintf("(pr.rule_path ILIKE $%d OR pr.title ILIKE $%d OR pr.repository_name ILIKE $%d)", n, n, n))
		args = append(args, "%"+f.Query+"%")
	}
	// Access scoping: precompute the user's owned (agent, repository) set once instead
	// of re-probing session_records per rule. UserID takes precedence over ProfileEmail
	// (matches the prior behavior: the UserID branch was checked first).
	if f.UserID != "" || f.ProfileEmail != "" {
		n++
		col, val := "user_id", f.UserID
		if f.UserID == "" {
			col, val = "profile_email", f.ProfileEmail
		}
		// MATERIALIZED: force the ownership set to be computed once (mirrors
		// postgres_plugin_usage.go), so a future planner can't inline this single-
		// reference CTE back into a per-rule correlated scan.
		cte = fmt.Sprintf(`WITH owned AS MATERIALIZED (
			SELECT DISTINCT COALESCE(NULLIF(agent,''), 'claude') AS agent, repository_id, project_hash
			FROM session_records WHERE %s = $%d
		) `, col, n)
		args = append(args, val)
		conds = append(conds, `EXISTS (
			SELECT 1 FROM owned o
			WHERE o.agent = pr.agent
			  AND (
				(pr.repository_id != '' AND o.repository_id = pr.repository_id)
				OR (pr.repository_id = '' AND o.project_hash = pr.project_hash)
				OR (pr.repository_key != '' AND o.repository_id = pr.repository_key)
			  )
		)`)
	}
	if len(conds) == 0 {
		return cte, "", args
	}
	return cte, "WHERE " + strings.Join(conds, " AND "), args
}

func decodeProjectRuleVersionJSON(v *ProjectRuleVersion, appliesTo, frontmatter, raw []byte) {
	_ = json.Unmarshal(appliesTo, &v.AppliesTo)
	_ = json.Unmarshal(frontmatter, &v.Frontmatter)
	_ = json.Unmarshal(raw, &v.RawMetadata)
	if v.AppliesTo == nil {
		v.AppliesTo = []string{}
	}
	if v.Frontmatter == nil {
		v.Frontmatter = map[string]interface{}{}
	}
	if v.RawMetadata == nil {
		v.RawMetadata = map[string]interface{}{}
	}
}

func nonNilStringSlice(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilMap(v map[string]interface{}) map[string]interface{} {
	if v == nil {
		return map[string]interface{}{}
	}
	return v
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
