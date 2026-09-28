package store

import "strings"

// excludedAccountPredicateSQL is the account exclusion for a row named alias:
// three NOT EXISTS anti-joins, ANDed, ready to follow a WHERE or an AND. alias
// must carry login_email, billing_provider and account_id; the subqueries use
// the aliases x, b and l.
//
// Three keys, because an account can be known by any of them. excluded_accounts
// is keyed on login_email, which Codex rows never carry; excluded_billing_accounts
// is keyed on the billing id, which rows without one report empty -- the table
// CHECKs its key non-empty, so the second anti-join cannot match them.
// excluded_billing_links is the third: the billing accounts an excluded address
// was seen with. Without it, excluding the address never reached that account's
// Codex rows, which carry the billing id and not the address (#715).
//
// Two readers do not use it:
//   - visible_metrics judges Codex rows by billing account and never by
//     login_email, and judges rows naming no account by their minute; see the
//     comment above it in dependentViews.
//   - postgres_session_delete.go reads the same three tables into a Go blocklist
//     instead of filtering in SQL.
func excludedAccountPredicateSQL(alias string) string {
	return strings.ReplaceAll(`NOT EXISTS (
		SELECT 1 FROM excluded_accounts x WHERE lower(x.login_email) = lower(@.login_email)
	)
	AND NOT EXISTS (
		SELECT 1 FROM excluded_billing_accounts b
		WHERE b.billing_provider = @.billing_provider AND b.account_id = @.account_id
	)
	AND NOT EXISTS (
		SELECT 1 FROM excluded_billing_links l
		WHERE l.billing_provider = @.billing_provider AND l.account_id = @.account_id
	)`, "@", alias)
}
