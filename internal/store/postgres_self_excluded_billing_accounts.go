package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ObservedBillingAccount is a billing account the caller's own data was billed
// to, with whether it is excluded and whether that exclusion is theirs (#716).
//
// A user can self-exclude only from this list: an account that never billed
// their own rows is someone else's to decide about.
type ObservedBillingAccount struct {
	BillingProvider string `json:"billing_provider"`
	AccountID       string `json:"account_id"`
	// Shared is an account quota readings saw with more than one login address,
	// or reported by a profile that is not the caller's: it bills several people,
	// so excluding it would hide everyone on it. Session records of other users
	// are checked only on exclusion -- see BillingAccountUsedByOthers.
	Shared   bool `json:"shared"`
	Excluded bool `json:"excluded"`
	// SelfRegistered is an exclusion this caller made, the only kind they may remove.
	SelfRegistered bool `json:"self_registered"`
}

// ListObservedBillingAccounts returns the billing accounts on the caller's own
// session records and quota readings, matched the way resolveUserAccessParams
// scopes reads: by user_id when given, else by profile_email. Both empty
// matches nothing.
//
// session_records is read through its (user_id, ts) and (profile_email, ts)
// indexes, so the cost is one user's rows, not the hypertable. quota_samples has
// no profile index but is a plain table of readings, not events. It is read only
// when scoping by profile: it carries no user_id, and a profile name is whatever
// the client sent, so in user-id mode another user's readings under the same
// name would pass for the caller's.
//
// Exclusion is read from excluded_billing_accounts and excluded_billing_links,
// the same two sources the visible_* views hide by.
func (s *PgStore) ListObservedBillingAccounts(ctx context.Context, profileEmail, userID, actor string) ([]ObservedBillingAccount, error) {
	rows, err := s.pool.Query(ctx, `
		WITH own AS (
			SELECT DISTINCT billing_provider, account_id, profile_email
			FROM session_records
			WHERE ($2 <> '' AND user_id = $2) OR ($1 <> '' AND profile_email = $1)
		),
		observed AS (
			SELECT billing_provider, account_id FROM own
			UNION
			SELECT billing_provider, account_id FROM quota_samples
			WHERE $1 <> '' AND profile_email = $1
		)
		SELECT o.billing_provider, o.account_id,
			(SELECT count(DISTINCT lower(q.login_email)) FROM quota_samples q
			 WHERE q.billing_provider = o.billing_provider AND q.account_id = o.account_id
			   AND q.login_email <> '') > 1
			-- Another profile reporting the account's quota: most Codex readings carry
			-- no login address, so the count above alone misses a team account.
			OR EXISTS (SELECT 1 FROM quota_samples q
			           WHERE q.billing_provider = o.billing_provider AND q.account_id = o.account_id
			             AND q.profile_email <> '' AND q.profile_email <> $1
			             AND q.profile_email NOT IN (SELECT profile_email FROM own)),
			EXISTS (SELECT 1 FROM excluded_billing_accounts b
			        WHERE b.billing_provider = o.billing_provider AND b.account_id = o.account_id)
			OR EXISTS (SELECT 1 FROM excluded_billing_links l
			           WHERE l.billing_provider = o.billing_provider AND l.account_id = o.account_id),
			EXISTS (SELECT 1 FROM excluded_billing_accounts b
			        WHERE b.billing_provider = o.billing_provider AND b.account_id = o.account_id
			          AND b.registered_by_self AND lower(b.created_by) = lower($3))
		FROM observed o
		WHERE o.billing_provider <> '' AND o.account_id <> ''
		ORDER BY o.billing_provider, o.account_id`, profileEmail, userID, actor)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]ObservedBillingAccount, 0)
	for rows.Next() {
		var a ObservedBillingAccount
		if err := rows.Scan(&a.BillingProvider, &a.AccountID, &a.Shared, &a.Excluded, &a.SelfRegistered); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// BillingAccountUsedByOthers reports whether session records of anyone but the
// caller carry this account. It is the other half of Shared, checked when a user
// asks to exclude rather than on every list: session_records has no index on the
// billing key, so this is a scan of the hypertable, and one per exclusion request
// is affordable where one per Settings visit is not.
//
// It is also what makes "seen in your data" mean ownership. /api/sync stores the
// identity the client sends, so anyone can plant a record carrying someone
// else's account id; the real owner's records are already there and answer yes.
//
// A row with the field empty was synced before it existed and is no one's -- the
// rule SessionOwner applies.
func (s *PgStore) BillingAccountUsedByOthers(ctx context.Context, provider, accountID, profileEmail, userID string) (bool, error) {
	provider, accountID = normalizeBillingKey(provider, accountID)
	var used bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM session_records
			WHERE billing_provider = $1 AND account_id = $2
			  AND (($4 <> '' AND user_id <> '' AND user_id <> $4)
			    OR ($3 <> '' AND profile_email <> '' AND profile_email <> $3)))`,
		provider, accountID, profileEmail, userID).Scan(&used)
	return used, err
}

// SelfExcludeBillingAccount records the owner's own exclusion of an account and
// applies it exactly as ExcludeBillingAccount does. An existing entry is left
// alone: taking over an admin's entry would make it one the owner could remove.
// Leaving it alone changes nothing, so the recompute and rollup rebuild -- which
// rewrite derived tables for everyone -- are skipped.
func (s *PgStore) SelfExcludeBillingAccount(ctx context.Context, provider, accountID, reason, actor string) error {
	provider, accountID = normalizeBillingKey(provider, accountID)
	ref := BillingAccountRef{BillingProvider: provider, AccountID: accountID}
	err := s.applyExclusionChange(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO excluded_billing_accounts (billing_provider, account_id, reason, created_by, registered_by_self)
			VALUES ($1, $2, $3, $4, true)
			ON CONFLICT (billing_provider, account_id) DO NOTHING`,
			provider, accountID, reason, actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNothingChanged
		}
		return nil
	}, nil, []BillingAccountRef{ref}, false)
	if errors.Is(err, errNothingChanged) {
		return nil
	}
	return err
}

// errNothingChanged aborts applyExclusionChange before its recompute when the
// mutation touched no row.
var errNothingChanged = errors.New("exclusion unchanged")

// RemoveSelfExcludedBillingAccount removes an exclusion only when actor
// registered it themselves, and reports whether it did. An admin's entry, or
// another user's, is left in place and reported as not removed.
func (s *PgStore) RemoveSelfExcludedBillingAccount(ctx context.Context, provider, accountID, actor string) (bool, error) {
	provider, accountID = normalizeBillingKey(provider, accountID)
	ref := BillingAccountRef{BillingProvider: provider, AccountID: accountID}
	err := s.applyExclusionChange(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM excluded_billing_accounts
			WHERE billing_provider = $1 AND account_id = $2 AND registered_by_self AND lower(created_by) = lower($3)`,
			provider, accountID, actor)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Nothing to remove is success, not refusal: a second request that
			// queued behind the first finds the entry already gone.
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS (SELECT 1 FROM excluded_billing_accounts WHERE billing_provider = $1 AND account_id = $2)`,
				provider, accountID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return errAlreadyGone
			}
			return errNothingChanged
		}
		return nil
	}, nil, []BillingAccountRef{ref}, false)
	if errors.Is(err, errAlreadyGone) {
		return true, nil
	}
	if errors.Is(err, errNothingChanged) {
		return false, nil
	}
	return err == nil, err
}

// errAlreadyGone aborts a removal that has nothing to remove, without the
// recompute a real change would trigger.
var errAlreadyGone = errors.New("exclusion already removed")
