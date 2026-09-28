CREATE OR REPLACE VIEW visible_session_records AS
	SELECT * FROM session_records sr
	WHERE NOT EXISTS (
		SELECT 1 FROM excluded_accounts x WHERE lower(x.login_email) = lower(sr.login_email)
	)
	AND NOT EXISTS (
		SELECT 1 FROM excluded_billing_accounts b
		WHERE b.billing_provider = sr.billing_provider AND b.account_id = sr.account_id
	)
	AND NOT EXISTS (
		SELECT 1 FROM excluded_billing_links l
		WHERE l.billing_provider = sr.billing_provider AND l.account_id = sr.account_id
	)
	AND NOT EXISTS (
		SELECT 1 FROM excluded_sessions es
		WHERE es.session_id = sr.session_id AND sr.session_id <> ''
	)
