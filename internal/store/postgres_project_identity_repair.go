package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var projectIdentityRepairSourceColumns = []string{
	"agent",
	"project_hash",
	"project_name",
	"git_remote_url",
	"repository_id",
	"repository_name",
	"repo_subpath",
}

var projectIdentityRepairHistorySourceColumns = []string{
	"agent",
	"project_hash",
	"repository_id",
	"repo_subpath",
	"record_count",
	"first_seen",
	"last_seen",
}

// DryRunProjectIdentityRepair reads and classifies the complete projects table
// inside one repeatable-read, read-only transaction. It never runs migrations
// and exposes no apply path.
func (s *PgStore) DryRunProjectIdentityRepair(ctx context.Context) (*ProjectIdentityRepairReport, error) {
	var report *ProjectIdentityRepairReport
	err := s.withProjectIdentityRepairReadOnlyTx(ctx, func(tx pgx.Tx) error {
		var readOnly string
		if err := tx.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&readOnly); err != nil {
			return fmt.Errorf("verify project identity repair transaction: %w", err)
		}
		if readOnly != "on" {
			return fmt.Errorf("project identity repair transaction is not read-only")
		}
		databaseName, databaseVersion, err := readProjectIdentityRepairProvenance(ctx, tx)
		if err != nil {
			return err
		}

		before, err := loadProjectIdentityRepairEvidence(ctx, tx)
		if err != nil {
			return err
		}
		plan := PlanProjectIdentityRepairWithHistory(before.rows, before.history)
		after, err := loadProjectIdentityRepairEvidence(ctx, tx)
		if err != nil {
			return err
		}
		report = newProjectIdentityRepairReport("dry-run", databaseName, databaseVersion, before, after, plan)
		if report.SnapshotBefore != report.SnapshotAfter || report.HistorySnapshotBefore != report.HistorySnapshotAfter {
			return fmt.Errorf("project identity repair snapshot changed during dry-run")
		}
		report.ReadOnlyVerified = true
		// This proves both reads observed the same repeatable-read snapshot;
		// it does not assert that external writers were absent.
		report.SnapshotConsistent = true
		return nil
	})
	if err != nil {
		return nil, err
	}
	return report, nil
}

// ApplyProjectIdentityRepair writes every recoverable proposal to its projects
// row in one repeatable-read transaction: all rows are repaired or none are.
// Only repository_id and repository_name change; repo_subpath, session_records
// and every other column are left as stored.
//
// Running it again is a no-op. A repaired row is already_canonical on the next
// plan, and the UPDATE itself skips a row that already holds the proposal. A
// sync that rewrites one of the same rows mid-run makes the UPDATE fail with a
// serialization error and the whole run rolls back; running it again is safe.
func (s *PgStore) ApplyProjectIdentityRepair(ctx context.Context) (*ProjectIdentityRepairReport, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadWrite})
	if err != nil {
		return nil, fmt.Errorf("begin project identity repair transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	databaseName, databaseVersion, err := readProjectIdentityRepairProvenance(ctx, tx)
	if err != nil {
		return nil, err
	}
	before, err := loadProjectIdentityRepairEvidence(ctx, tx)
	if err != nil {
		return nil, err
	}
	plan := PlanProjectIdentityRepairWithHistory(before.rows, before.history)
	for _, result := range plan.Results {
		if result.Category != ProjectIdentityRepairRecoverable || result.Proposal == nil {
			continue
		}
		tag, err := tx.Exec(ctx, `UPDATE projects
			SET repository_id = $3, repository_name = $4
			WHERE agent = $1 AND project_hash = $2
			  AND (repository_id <> $3 OR repository_name <> $4)`,
			result.Agent, result.ProjectHash, result.Proposal.RepositoryID, result.Proposal.RepositoryName)
		if err != nil {
			return nil, fmt.Errorf("apply project identity repair: %w", err)
		}
		plan.WriteQueries += int(tag.RowsAffected())
	}

	after, err := loadProjectIdentityRepairEvidence(ctx, tx)
	if err != nil {
		return nil, err
	}
	if err := verifyProjectIdentityRepairApplied(after); err != nil {
		return nil, err
	}
	report := newProjectIdentityRepairReport("apply", databaseName, databaseVersion, before, after, plan)
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit project identity repair: %w", err)
	}
	return report, nil
}

// verifyProjectIdentityRepairApplied re-plans the written state. A scope that
// still has a projects row and is still recoverable means the writes did not
// land as planned, and the transaction must not commit.
func verifyProjectIdentityRepairApplied(after projectIdentityRepairEvidence) error {
	stored := make(map[projectIdentityRepairScope]struct{}, len(after.rows))
	for _, row := range after.rows {
		stored[projectIdentityRepairScope{agent: row.Agent, projectHash: row.ProjectHash}] = struct{}{}
	}
	for _, result := range PlanProjectIdentityRepairWithHistory(after.rows, after.history).Results {
		if result.Category != ProjectIdentityRepairRecoverable {
			continue
		}
		if _, ok := stored[projectIdentityRepairScope{agent: result.Agent, projectHash: result.ProjectHash}]; ok {
			return fmt.Errorf("project identity repair left a recoverable row unrepaired")
		}
	}
	return nil
}

// projectIdentityRepairEvidence is one read of both sources the classifier uses.
type projectIdentityRepairEvidence struct {
	rows    []ProjectIdentityRepairRow
	history []ProjectIdentityRepairHistoryRow
}

func loadProjectIdentityRepairEvidence(ctx context.Context, tx pgx.Tx) (projectIdentityRepairEvidence, error) {
	rows, err := loadProjectIdentityRepairSnapshot(ctx, tx)
	if err != nil {
		return projectIdentityRepairEvidence{}, err
	}
	history, err := loadProjectIdentityRepairHistory(ctx, tx)
	if err != nil {
		return projectIdentityRepairEvidence{}, err
	}
	return projectIdentityRepairEvidence{rows: rows, history: history}, nil
}

func readProjectIdentityRepairProvenance(ctx context.Context, tx pgx.Tx) (string, string, error) {
	var databaseName, databaseVersion string
	if err := tx.QueryRow(ctx, `SELECT current_database(), current_setting('server_version')`).Scan(&databaseName, &databaseVersion); err != nil {
		return "", "", fmt.Errorf("read project identity repair database provenance: %w", err)
	}
	return databaseName, databaseVersion, nil
}

func newProjectIdentityRepairReport(mode, databaseName, databaseVersion string, before, after projectIdentityRepairEvidence, plan ProjectIdentityRepairPlan) *ProjectIdentityRepairReport {
	return &ProjectIdentityRepairReport{
		SchemaVersion:         3,
		Mode:                  mode,
		DatabaseName:          databaseName,
		DatabaseVersion:       databaseVersion,
		SourceRelation:        "projects",
		SourceColumns:         append([]string(nil), projectIdentityRepairSourceColumns...),
		HistorySourceRelation: "session_records",
		HistorySourceColumns:  append([]string(nil), projectIdentityRepairHistorySourceColumns...),
		SnapshotBefore:        projectIdentityRepairSnapshot(before.rows),
		SnapshotAfter:         projectIdentityRepairSnapshot(after.rows),
		HistorySnapshotBefore: projectIdentityRepairHistorySnapshot(before.history),
		HistorySnapshotAfter:  projectIdentityRepairHistorySnapshot(after.history),
		WriteQueries:          plan.WriteQueries,
		Counts:                plan.Counts,
		Results:               projectIdentityRepairReportResults(plan.Results),
	}
}

func (s *PgStore) withProjectIdentityRepairReadOnlyTx(ctx context.Context, run func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fmt.Errorf("begin project identity repair transaction: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	return run(tx)
}

func loadProjectIdentityRepairSnapshot(ctx context.Context, tx pgx.Tx) ([]ProjectIdentityRepairRow, error) {
	rows, err := tx.Query(ctx, `SELECT agent, project_hash, project_name, git_remote_url,
		repository_id, repository_name, repo_subpath
		FROM projects
		ORDER BY agent, project_hash`)
	if err != nil {
		return nil, fmt.Errorf("load project identity repair snapshot: %w", err)
	}
	defer rows.Close()

	result := make([]ProjectIdentityRepairRow, 0)
	for rows.Next() {
		var row ProjectIdentityRepairRow
		if err := rows.Scan(
			&row.Agent,
			&row.ProjectHash,
			&row.ProjectName,
			&row.GitRemoteURL,
			&row.RepositoryID,
			&row.RepositoryName,
			&row.RepoSubpath,
		); err != nil {
			return nil, fmt.Errorf("scan project identity repair snapshot: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project identity repair snapshot: %w", err)
	}
	return result, nil
}

func loadProjectIdentityRepairHistory(ctx context.Context, tx pgx.Tx) ([]ProjectIdentityRepairHistoryRow, error) {
	rows, err := tx.Query(ctx, `SELECT agent, project_hash, btrim(repository_id),
		btrim(repo_subpath), COUNT(*) AS record_count, MIN(ts) AS first_seen, MAX(ts) AS last_seen
		FROM session_records
		WHERE btrim(project_hash) <> ''
		GROUP BY agent, project_hash, btrim(repository_id), btrim(repo_subpath)
		ORDER BY 1, 2, 3, 4`)
	if err != nil {
		return nil, fmt.Errorf("load project identity repair session history: %w", err)
	}
	defer rows.Close()

	result := make([]ProjectIdentityRepairHistoryRow, 0)
	for rows.Next() {
		var row ProjectIdentityRepairHistoryRow
		if err := rows.Scan(
			&row.Agent,
			&row.ProjectHash,
			&row.RepositoryID,
			&row.RepoSubpath,
			&row.RecordCount,
			&row.FirstSeen,
			&row.LastSeen,
		); err != nil {
			return nil, fmt.Errorf("scan project identity repair session history: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project identity repair session history: %w", err)
	}
	return result, nil
}
