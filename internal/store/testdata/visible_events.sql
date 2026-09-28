CREATE OR REPLACE VIEW visible_events AS
	SELECT * FROM unified_events e
	WHERE NOT EXISTS (
		SELECT 1 FROM excluded_accounts x WHERE lower(x.login_email) = lower(e.login_email)
	)
	AND NOT EXISTS (
		SELECT 1 FROM excluded_billing_accounts b
		WHERE b.billing_provider = e.billing_provider AND b.account_id = e.account_id
	)
	AND NOT EXISTS (
		SELECT 1 FROM excluded_billing_links l
		WHERE l.billing_provider = e.billing_provider AND l.account_id = e.account_id
	)
