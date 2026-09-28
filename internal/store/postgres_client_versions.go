package store

import (
	"context"
	"time"
)

// UpsertClientVersion records that this account synced, and what it said about
// itself while doing so.
//
// 세 컬럼(client_version/client_os/client_arch)만 빈 값으로 덮지 않는다. 이 값들은
// 헤더에서 오고, 헤더가 없다는 것은 "이 클라이언트가 무엇인지 모른다"이지 "값이
// 빈 클라이언트다"가 아니다. 실제로 헤더 없는 요청은 v0.6.1 미만뿐 아니라 버전
// 문자열이 비어 있는 빌드(ldflags 미주입)에서도 온다 -- internal/syncer/client.go 는
// 그때 헤더 자체를 붙이지 않는다. 그런 요청 한 건으로 최신 클라이언트를 'v0.6.1
// 미만'으로 확정 표기하면 #455 가 없애려던 오진을 그대로 되풀이하게 된다. 반면
// 마지막 비어있지 않은 값을 유지하면, 실제로 버전이 바뀌었을 때는 새 값이 비어있지
// 않으므로 정상적으로 덮인다.
//
// user_id 는 본문 필드이고 sync 하는 클라이언트는 버전과 무관하게 모두 보내므로
// 같은 규칙을 적용하지 않는다. INSERT 경로도 손대지 않는다 -- 신규 계정은 빈 버전
// 그대로 행이 생겨야 "행 없음"이 "sync 없음"을 뜻하게 된다.
//
// 자기 갱신 상태(update_*)는 위 세 컬럼과 규칙이 다르다. 빈 값이 "모름"인 쪽은
// 헤더였고, 여기서는 보고 자체가 있었는지를 update_reported 가 따로 들고 있으므로
// 보고가 왔으면 빈 값도 그대로 쓴다 -- 그 빈 값이 "갱신에 성공했다"는 뜻이라 덮지
// 않으면 끝난 실패가 화면에 남는다. 보고가 없으면(구버전) 여섯 컬럼을 건드리지
// 않는다.
func (s *PgStore) UpsertClientVersion(ctx context.Context, update ClientVersionUpdate) error {
	reported, target, count, first, last, reason := updateStallColumns(update.UpdateStall)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO client_versions (
			profile_email, user_id, client_version, client_os, client_arch, last_seen_at,
			update_reported, update_target_version, update_fail_count,
			update_first_failed_at, update_last_failed_at, update_fail_reason
		)
		VALUES ($1,$2,$3,$4,$5,now(),$6,$7,$8,$9,$10,$11)
		ON CONFLICT (profile_email) DO UPDATE SET
			user_id        = EXCLUDED.user_id,
			client_version = COALESCE(NULLIF(EXCLUDED.client_version, ''), client_versions.client_version),
			client_os      = COALESCE(NULLIF(EXCLUDED.client_os, ''),      client_versions.client_os),
			client_arch    = COALESCE(NULLIF(EXCLUDED.client_arch, ''),    client_versions.client_arch),
			last_seen_at   = now(),
			update_reported        = client_versions.update_reported OR EXCLUDED.update_reported,
			update_target_version  = CASE WHEN EXCLUDED.update_reported THEN EXCLUDED.update_target_version  ELSE client_versions.update_target_version  END,
			update_fail_count      = CASE WHEN EXCLUDED.update_reported THEN EXCLUDED.update_fail_count      ELSE client_versions.update_fail_count      END,
			update_first_failed_at = CASE WHEN EXCLUDED.update_reported THEN EXCLUDED.update_first_failed_at ELSE client_versions.update_first_failed_at END,
			update_last_failed_at  = CASE WHEN EXCLUDED.update_reported THEN EXCLUDED.update_last_failed_at  ELSE client_versions.update_last_failed_at  END,
			update_fail_reason     = CASE WHEN EXCLUDED.update_reported THEN EXCLUDED.update_fail_reason     ELSE client_versions.update_fail_reason     END`,
		update.ProfileEmail, update.UserID, update.ClientVersion, update.ClientOS, update.ClientArch,
		reported, target, count, first, last, reason,
	)
	return err
}

// ClientUpdateStallReasonMax bounds the client's error text. It is the one field
// a client chooses the length of, and it is written on every sync, so the API
// edge cuts it there too rather than trusting the sender.
const ClientUpdateStallReasonMax = 500

// updateStallColumns flattens the reported state into what the row stores. A nil
// report is not an empty one: reported stays false and the CASE arms above then
// leave every stall column as it was.
func updateStallColumns(st *ClientUpdateStall) (bool, string, int, *time.Time, *time.Time, string) {
	if st == nil {
		return false, "", 0, nil, nil, ""
	}
	// Cut on runes: an error message can carry non-ASCII, and a byte cut would
	// store a broken one.
	reason := st.Reason
	if r := []rune(reason); len(r) > ClientUpdateStallReasonMax {
		reason = string(r[:ClientUpdateStallReasonMax])
	}
	var first, last *time.Time
	if !st.FirstFailedAt.IsZero() {
		t := st.FirstFailedAt
		first = &t
	}
	if !st.LastFailedAt.IsZero() {
		t := st.LastFailedAt
		last = &t
	}
	return true, st.TargetVersion, st.Consecutive, first, last, reason
}

// clientVersionActivityWindowDays bounds the "recently active" set that
// ListClientVersions reports against. 30 days matches the account counts
// measured directly in prod (session_records: 16 accounts, otel_events: 17
// accounts, vs. 11 in client_versions) that motivated this query.
//
// What absence from client_versions means changed with #455. It used to be read
// as "a pre-v0.6.1 client that cannot send the header", and that reading was
// wrong for six prod accounts. Now that every sync writes a row, absence means
// the account has not synced since that change shipped -- and for one that has,
// an empty client_version means the header was missing, which a build without
// ldflags produces just as readily as an old client.
const clientVersionActivityWindowDays = 30

// clientVersionConcurrencyWindowHours bounds the window the concurrent-version
// signal looks at. A day is long enough that an ordinary upgrade -- old version
// in the morning, new one after -- still shows two, so the number alone is not a
// fault: the fault is the same two persisting day after day. The screen says
// which it is by showing the count beside the last-seen time.
const clientVersionConcurrencyWindowHours = 24

// ListClientVersions reports every account active in the last
// clientVersionActivityWindowDays days, left-joined with its reported client
// version and with the display name of the dashboard account that owns the
// same address.
//
// The name join is a LEFT JOIN for the same reason the version one is: an
// account can be active without being registered in dashboard_users, and the
// accounts furthest behind are the likeliest to be exactly that. Matching is
// case-insensitive because the two columns are filled by different routes --
// profile_email arrives from a client, email from a signup form -- and neither
// normalises the other's casing. Accounts that never reported a version (pre-v0.6.1 clients) are
// included with an empty ClientVersion rather than omitted — that was the bug
// this replaced: sourcing the list from client_versions alone silently
// dropped the accounts furthest behind.
//
// session_records (~4.3M rows) and otel_events are both hypertables
// chunked on ts, and both carry a (profile_email, ts DESC) index
// (idx_srec_user / idx_events_user), so the ts >= cutoff predicate first
// prunes to the last month of chunks and the aggregation then only touches
// that slice, not the full table.
func (s *PgStore) ListClientVersions(ctx context.Context) ([]*ClientVersionRecord, error) {
	rows, err := s.pool.Query(ctx, `
		WITH activity AS (
			SELECT profile_email, MAX(ts) AS last_activity_at
			FROM session_records
			WHERE ts >= now() - make_interval(days => $1) AND profile_email <> ''
			GROUP BY profile_email
			UNION ALL
			SELECT profile_email, MAX(ts) AS last_activity_at
			FROM otel_events
			WHERE ts >= now() - make_interval(days => $1) AND profile_email <> ''
			GROUP BY profile_email
		),
		active_accounts AS (
			SELECT profile_email, MAX(last_activity_at) AS last_activity_at
			FROM activity
			GROUP BY profile_email
		),
		-- Distinct versions seen over the last day, from the record history rather
		-- than client_versions (which keeps one string per account and therefore
		-- cannot show two at once). Measured at 124ms on production.
		concurrent AS (
			SELECT profile_email, count(DISTINCT cctrace_version) AS versions
			FROM session_records
			WHERE ts >= now() - make_interval(hours => $2)
			  AND profile_email <> '' AND cctrace_version <> ''
			GROUP BY profile_email
		)
		SELECT a.profile_email,
		       COALESCE(du.name, ''),
		       COALESCE(cv.user_id, ''),
		       COALESCE(cv.client_version, ''),
		       COALESCE(cv.client_os, ''),
		       COALESCE(cv.client_arch, ''),
		       COALESCE(cv.last_seen_at, a.last_activity_at) AS last_seen_at,
		       COALESCE(cc.versions, 0),
		       COALESCE(cv.update_reported, false),
		       COALESCE(cv.update_target_version, ''),
		       COALESCE(cv.update_fail_count, 0),
		       cv.update_first_failed_at,
		       cv.update_last_failed_at,
		       COALESCE(cv.update_fail_reason, '')
		FROM active_accounts a
		LEFT JOIN client_versions cv ON cv.profile_email = a.profile_email
		LEFT JOIN dashboard_users du ON lower(du.email) = lower(a.profile_email)
		LEFT JOIN concurrent cc ON cc.profile_email = a.profile_email
		ORDER BY last_seen_at DESC`,
		clientVersionActivityWindowDays,
		clientVersionConcurrencyWindowHours,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*ClientVersionRecord
	for rows.Next() {
		c := &ClientVersionRecord{}
		if err := rows.Scan(
			&c.ProfileEmail, &c.Name, &c.UserID, &c.ClientVersion, &c.ClientOS, &c.ClientArch, &c.LastSeenAt,
			&c.ConcurrentVersions,
			&c.UpdateReported, &c.UpdateTargetVersion, &c.UpdateFailCount,
			&c.UpdateFirstFailedAt, &c.UpdateLastFailedAt, &c.UpdateFailReason,
		); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}
