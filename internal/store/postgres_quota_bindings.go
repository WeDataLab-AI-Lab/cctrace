package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	MaxQuotaOwnerSamplesPerRequest = 1000
	quotaOwnerSampleInsertChunk    = 1000
)

type quotaOwnerAccountKey struct{ provider, accountID string }

func validQuotaAccount(billingProvider, accountID string) bool {
	if (billingProvider != "anthropic" && billingProvider != "openai") || accountID == "" ||
		len(accountID) > 256 || !utf8.ValidString(accountID) || strings.ContainsAny(accountID, "\x00\r\n") || strings.Contains(accountID, "@") {
		return false
	}
	return strings.TrimSpace(accountID) == accountID
}

func validQuotaVerification(method, reference string) bool {
	if method != "provider_console" || reference == "" || len(reference) > 128 || !utf8.ValidString(reference) {
		return false
	}
	for _, r := range reference {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("._:-", r) {
			return false
		}
	}
	return true
}

const quotaBindingColumns = `id, dashboard_user_id, billing_provider, account_id, status,
	verified_at, approved_by, verification_method, verification_reference, requested_at, updated_at`

const quotaAdminBindingColumns = `b.id, b.dashboard_user_id, COALESCE(u.email,''), b.billing_provider, b.account_id, b.status,
	b.verified_at, b.approved_by, b.verification_method, b.verification_reference, b.requested_at, b.updated_at`

type quotaRow interface{ Scan(...any) error }

func scanQuotaAccountBinding(row quotaRow, ownerEmail bool) (*QuotaAccountBinding, error) {
	binding := &QuotaAccountBinding{}
	var verifiedAt pgtype.Timestamptz
	var approvedBy pgtype.Int8
	var err error
	if ownerEmail {
		err = row.Scan(&binding.ID, &binding.DashboardUserID, &binding.OwnerEmail, &binding.BillingProvider, &binding.AccountID,
			&binding.Status, &verifiedAt, &approvedBy, &binding.VerificationMethod, &binding.VerificationReference,
			&binding.RequestedAt, &binding.UpdatedAt)
	} else {
		err = row.Scan(&binding.ID, &binding.DashboardUserID, &binding.BillingProvider, &binding.AccountID,
			&binding.Status, &verifiedAt, &approvedBy, &binding.VerificationMethod, &binding.VerificationReference,
			&binding.RequestedAt, &binding.UpdatedAt)
	}
	if err != nil {
		return nil, err
	}
	if verifiedAt.Valid {
		value := verifiedAt.Time
		binding.VerifiedAt = &value
	}
	if approvedBy.Valid {
		value := approvedBy.Int64
		binding.ApprovedBy = &value
	}
	return binding, nil
}

func (s *PgStore) ApplyQuotaAccountBinding(ctx context.Context, dashboardUserID int64, billingProvider, accountID string) (*QuotaAccountBinding, error) {
	if dashboardUserID <= 0 || !validQuotaAccount(billingProvider, accountID) {
		return nil, ErrQuotaBindingInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit

	var insertedID int64
	err = tx.QueryRow(ctx, `INSERT INTO quota_account_bindings (dashboard_user_id,billing_provider,account_id,status)
		VALUES ($1,$2,$3,'pending') ON CONFLICT (dashboard_user_id,billing_provider,account_id) DO NOTHING RETURNING id`,
		dashboardUserID, billingProvider, accountID).Scan(&insertedID)
	created := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	row, err := scanQuotaAccountBinding(tx.QueryRow(ctx, `SELECT `+quotaBindingColumns+`
		FROM quota_account_bindings WHERE dashboard_user_id=$1 AND billing_provider=$2 AND account_id=$3 FOR UPDATE`,
		dashboardUserID, billingProvider, accountID), false)
	if err != nil {
		return nil, fmt.Errorf("read quota binding: %w", err)
	}
	if !created && row.Status == "revoked" {
		row, err = scanQuotaAccountBinding(tx.QueryRow(ctx, `UPDATE quota_account_bindings SET
			status='pending', verified_at=NULL, approved_by=NULL, verification_method='', verification_reference='',
			requested_at=clock_timestamp(), updated_at=clock_timestamp()
			WHERE id=$1 RETURNING `+quotaBindingColumns, row.ID), false)
		if err != nil {
			return nil, fmt.Errorf("reopen quota binding: %w", err)
		}
		created = true
	}
	if created {
		if err := insertQuotaBindingEvent(ctx, tx, row.ID, dashboardUserID, dashboardUserID, "requested", "", ""); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *PgStore) ListQuotaAccountBindings(ctx context.Context, dashboardUserID int64) ([]*QuotaAccountBinding, error) {
	if dashboardUserID <= 0 {
		return nil, ErrQuotaBindingInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT `+quotaBindingColumns+` FROM quota_account_bindings
		WHERE dashboard_user_id=$1 ORDER BY requested_at DESC, id DESC`, dashboardUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []*QuotaAccountBinding
	for rows.Next() {
		binding, err := scanQuotaAccountBinding(rows, false)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

func (s *PgStore) ListAdminQuotaAccountBindings(ctx context.Context, status string) ([]*QuotaAccountBinding, error) {
	if status != "" && status != "pending" && status != "approved" && status != "revoked" {
		return nil, ErrQuotaBindingInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT `+quotaAdminBindingColumns+` FROM quota_account_bindings b
		JOIN dashboard_users u ON u.id=b.dashboard_user_id
		WHERE ($1='' OR b.status=$1) ORDER BY CASE b.status WHEN 'pending' THEN 0 WHEN 'approved' THEN 1 ELSE 2 END,
		b.requested_at DESC, b.id DESC`, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var bindings []*QuotaAccountBinding
	for rows.Next() {
		binding, err := scanQuotaAccountBinding(rows, true)
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, binding)
	}
	return bindings, rows.Err()
}

func (s *PgStore) ApproveQuotaAccountBinding(ctx context.Context, bindingID, adminUserID int64, method, reference string) (*QuotaAccountBinding, error) {
	if bindingID <= 0 || adminUserID <= 0 || !validQuotaVerification(method, reference) {
		return nil, ErrQuotaBindingInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	current, err := scanQuotaAccountBinding(tx.QueryRow(ctx, `SELECT `+quotaBindingColumns+`
		FROM quota_account_bindings WHERE id=$1 FOR UPDATE`, bindingID), false)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrQuotaBindingNotFound
	}
	if err != nil {
		return nil, err
	}
	approved, err := scanQuotaAccountBinding(tx.QueryRow(ctx, `UPDATE quota_account_bindings SET
		status='approved', verified_at=GREATEST(clock_timestamp(), COALESCE(verified_at + interval '1 microsecond', '-infinity'::timestamptz)),
		approved_by=$2, verification_method=$3, verification_reference=$4, updated_at=clock_timestamp()
		WHERE id=$1 RETURNING `+quotaBindingColumns, bindingID, adminUserID, method, reference), false)
	if err != nil {
		return nil, err
	}
	if err := insertQuotaBindingEvent(ctx, tx, bindingID, current.DashboardUserID, adminUserID, "approved", method, reference); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return approved, nil
}

func (s *PgStore) RevokeQuotaAccountBinding(ctx context.Context, bindingID, adminUserID int64) (*QuotaAccountBinding, error) {
	if bindingID <= 0 || adminUserID <= 0 {
		return nil, ErrQuotaBindingInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	current, err := scanQuotaAccountBinding(tx.QueryRow(ctx, `SELECT `+quotaBindingColumns+`
		FROM quota_account_bindings WHERE id=$1 FOR UPDATE`, bindingID), false)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrQuotaBindingNotFound
	}
	if err != nil {
		return nil, err
	}
	if current.Status != "revoked" {
		current, err = scanQuotaAccountBinding(tx.QueryRow(ctx, `UPDATE quota_account_bindings
			SET status='revoked', updated_at=clock_timestamp() WHERE id=$1 RETURNING `+quotaBindingColumns, bindingID), false)
		if err != nil {
			return nil, err
		}
		if err := insertQuotaBindingEvent(ctx, tx, bindingID, current.DashboardUserID, adminUserID, "revoked", "", ""); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return current, nil
}

func insertQuotaBindingEvent(ctx context.Context, tx pgx.Tx, bindingID, ownerUserID, actorUserID int64, action, method, reference string) error {
	_, err := tx.Exec(ctx, `INSERT INTO quota_account_binding_events
		(binding_id,owner_dashboard_user_id,actor_dashboard_user_id,action,verification_method,verification_reference)
		VALUES ($1,$2,$3,$4,$5,$6)`, bindingID, ownerUserID, actorUserID, action, method, reference)
	if err != nil {
		return fmt.Errorf("record quota binding event: %w", err)
	}
	return nil
}

func (s *PgStore) InsertQuotaOwnerSamples(ctx context.Context, dashboardUserID int64, samples []*QuotaOwnerSample) (int, error) {
	if dashboardUserID <= 0 || len(samples) > MaxQuotaOwnerSamplesPerRequest {
		return 0, ErrQuotaBindingInvalid
	}
	if len(samples) == 0 {
		return 0, nil
	}
	now := time.Now().UTC()
	keys := make([]quotaOwnerAccountKey, 0)
	seen := make(map[quotaOwnerAccountKey]bool)
	for _, sample := range samples {
		if err := validateQuotaOwnerSample(sample, now); err != nil {
			return 0, err
		}
		key := quotaOwnerAccountKey{sample.BillingProvider, sample.AccountID}
		if !seen[key] {
			seen[key] = true
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].provider == keys[j].provider {
			return keys[i].accountID < keys[j].accountID
		}
		return keys[i].provider < keys[j].provider
	})

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op after commit
	epochs := make(map[quotaOwnerAccountKey]time.Time, len(keys))
	for _, key := range keys {
		var epoch pgtype.Timestamptz
		err := tx.QueryRow(ctx, `SELECT verified_at FROM quota_account_bindings
			WHERE dashboard_user_id=$1 AND billing_provider=$2 AND account_id=$3 AND status='approved'
			FOR UPDATE`, dashboardUserID, key.provider, key.accountID).Scan(&epoch)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !epoch.Valid {
			return 0, ErrQuotaBindingUnverified
		}
		if err != nil {
			return 0, err
		}
		excluded, err := quotaOwnerAccountExcluded(ctx, tx, key.provider, key.accountID)
		if err != nil {
			return 0, err
		}
		if excluded {
			return 0, ErrQuotaAccountExcluded
		}
		epochs[key] = epoch.Time
	}
	for _, sample := range samples {
		key := quotaOwnerAccountKey{sample.BillingProvider, sample.AccountID}
		if sample.SampledAt.Before(epochs[key]) {
			return 0, ErrQuotaBindingUnverified
		}
	}

	inserted := 0
	for start := 0; start < len(samples); start += quotaOwnerSampleInsertChunk {
		end := min(len(samples), start+quotaOwnerSampleInsertChunk)
		n, err := insertQuotaOwnerSampleRows(ctx, tx, dashboardUserID, epochs, samples[start:end])
		if err != nil {
			return 0, err
		}
		inserted += n
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return inserted, nil
}

func validateQuotaOwnerSample(sample *QuotaOwnerSample, now time.Time) error {
	if sample == nil || !validQuotaAccount(sample.BillingProvider, sample.AccountID) || sample.WindowKey == "" ||
		len(sample.WindowKey) > 128 || !utf8.ValidString(sample.WindowKey) || strings.ContainsAny(sample.WindowKey, "\x00\r\n") ||
		sample.SampledAt.IsZero() || sample.SampledAt.After(now.Add(5*time.Minute)) ||
		math.IsNaN(sample.UsedPct) || math.IsInf(sample.UsedPct, 0) || sample.UsedPct < 0 || sample.UsedPct > 100 ||
		(sample.WindowMinutes != nil && *sample.WindowMinutes <= 0) ||
		(sample.Attribution != AttributionObserved && sample.Attribution != AttributionInferred) {
		return ErrQuotaBindingInvalid
	}
	return nil
}

type quotaOwnerQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func quotaOwnerAccountExcluded(ctx context.Context, db quotaOwnerQuerier, provider, accountID string) (bool, error) {
	var included bool
	err := db.QueryRow(ctx, `SELECT `+quotaOwnerExclusionClause("a")+
		` FROM (SELECT $1::text AS billing_provider,$2::text AS account_id,''::text AS login_email) a`, provider, accountID).Scan(&included)
	return !included, err
}

func quotaOwnerExclusionClause(alias string) string {
	return `NOT EXISTS (
		SELECT 1 FROM excluded_billing_accounts b
		WHERE b.billing_provider=` + alias + `.billing_provider AND b.account_id=` + alias + `.account_id
	) AND NOT EXISTS (
		SELECT 1 FROM excluded_billing_links l
		WHERE l.billing_provider=` + alias + `.billing_provider AND l.account_id=` + alias + `.account_id
	) AND NOT EXISTS (
		SELECT 1 FROM quota_samples legacy
		JOIN excluded_accounts x ON lower(x.login_email)=lower(legacy.login_email)
		WHERE legacy.billing_provider=` + alias + `.billing_provider
		AND legacy.account_id=` + alias + `.account_id AND legacy.login_email <> ''
	)`
}

func insertQuotaOwnerSampleRows(ctx context.Context, tx pgx.Tx, dashboardUserID int64, epochs map[quotaOwnerAccountKey]time.Time, rows []*QuotaOwnerSample) (int, error) {
	const columns = 15
	var b strings.Builder
	args := make([]any, 0, len(rows)*columns)
	b.WriteString(`INSERT INTO quota_owner_samples
		(dashboard_user_id,billing_provider,account_id,window_key,sampled_at,used_pct,resets_at,window_minutes,
		 plan,scope_label,severity,is_active,attribution,source,binding_verified_at) VALUES `)
	for i, sample := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(placeholders(len(args), columns))
		var windowMinutes any
		if sample.WindowMinutes != nil {
			windowMinutes = *sample.WindowMinutes
		}
		key := quotaOwnerAccountKey{sample.BillingProvider, sample.AccountID}
		args = append(args, dashboardUserID, sample.BillingProvider, sample.AccountID, sample.WindowKey,
			sample.SampledAt, sample.UsedPct, sample.ResetsAt, windowMinutes, sample.Plan, sample.ScopeLabel,
			sample.Severity, sample.IsActive, sample.Attribution, "client_reported", epochs[key])
	}
	b.WriteString(` ON CONFLICT (dashboard_user_id,billing_provider,account_id,window_key,sampled_at) DO NOTHING`)
	tag, err := tx.Exec(ctx, b.String(), args...)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
