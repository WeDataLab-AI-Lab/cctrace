package sessionfacts

import "sort"

// contributor is what one contributing row says about who the session belongs
// to: a JSONL row, an api_request event or a snapshot (p1b-design §3.3).
type contributor struct {
	userID, profileEmail string
	account              AccountKey
}

// identify sets the session's identity lists, its owner columns and its
// account keys from every contributing row (p1b-design §2.1).
//
// The lists keep the empty string: a row with no user_id is a row no user may
// read, since the conversation API serves role=user only the rows that are
// theirs. So an owner column is set only when its list is exactly one non-empty
// value, which is stricter than SessionOwner; a session with a single foreign
// or ownerless row belongs to no one and is an administrator's alone.
func (c *computation) identify(rows []contributor) {
	for i := range c.in.OTEL {
		c.pulse()
		e := &c.in.OTEL[i]
		rows = append(rows, contributor{e.UserID, e.ProfileEmail, AccountKey{LoginEmail: e.LoginEmail, BillingProvider: e.BillingProvider}})
	}
	for i := range c.in.Snapshots {
		c.pulse()
		n := &c.in.Snapshots[i]
		rows = append(rows, contributor{n.UserID, n.ProfileEmail, AccountKey{LoginEmail: n.LoginEmail, BillingProvider: n.BillingProvider, AccountID: n.AccountID}})
	}
	var users, profiles, logins []string
	accounts := make(map[AccountKey]bool)
	for _, r := range rows {
		c.pulse()
		users = append(users, r.userID)
		profiles = append(profiles, r.profileEmail)
		logins = append(logins, r.account.LoginEmail)
		accounts[r.account] = true
	}
	s := &c.session
	s.UserIDs, s.ProfileEmails, s.LoginEmails = sortedSet(users), sortedSet(profiles), sortedSet(logins)
	s.OwnerUserID, s.OwnerProfileEmail = owner(s.UserIDs), owner(s.ProfileEmails)
	for key := range accounts {
		c.pulse()
		s.AccountKeys = append(s.AccountKeys, key)
	}
	sort.Slice(s.AccountKeys, func(i, j int) bool {
		a, b := s.AccountKeys[i], s.AccountKeys[j]
		switch {
		case a.LoginEmail != b.LoginEmail:
			return a.LoginEmail < b.LoginEmail
		case a.BillingProvider != b.BillingProvider:
			return a.BillingProvider < b.BillingProvider
		}
		return a.AccountID < b.AccountID
	})
}

func owner(values []string) *string {
	if len(values) != 1 || values[0] == "" {
		return nil
	}
	v := values[0]
	return &v
}

// inferredLogin reports whether a row's login_email was a guess: the provenance
// values the session list counts as one (loginEmailInferredExpr).
func inferredLogin(source string) bool { return source == "inferred" || source == "quota-inferred" }

// latest keeps the value of the latest row that has one, for the columns a
// session takes from its rows: its project, repository and branch are where it
// last ran.
type latest struct{ project, repository, branch string }

func (v *latest) see(project, repository, branch string) {
	if project != "" {
		v.project = project
	}
	if repository != "" {
		v.repository = repository
	}
	if branch != "" {
		v.branch = branch
	}
}

// entitle sets the session's title (p1b-design §3.18) from the latest snapshot
// and, failing that, from the first request's preview.
//
// The three keys are tried in order: custom_title, ai_title, agent_name. A key
// with a value is the title. An empty one says the session has no such line; a
// marker says a policy hid it; a missing key says the client held it back. With
// no value among them, a hidden key means no title at all -- the first request
// must not show what a strict policy hid, which is the contract the snapshot
// was sent under -- a held-back one means the first request stands in and says
// so, and three empty ones mean the first request is simply the title.
func (c *computation) entitle() {
	s := &c.session
	s.TitleSource, s.TitleState = "none", "none"

	var snap *Snapshot
	for i := range c.in.Snapshots {
		c.pulse()
		if n := &c.in.Snapshots[i]; snap == nil || snapshotAfter(n, snap) {
			snap = n
		}
	}
	// A session with no snapshot is one whose client sent none of the keys.
	keys := [3]*string{}
	if snap != nil {
		keys = [3]*string{snap.CustomTitle, snap.AITitle, snap.AgentName}
	}
	var hidden, withheld bool
	for i, key := range keys {
		c.pulse()
		switch {
		case key == nil:
			withheld = true
		case *key == redactedMarker:
			hidden = true
		case *key != "":
			title := *key
			s.Title, s.TitleSource, s.TitleState = &title, [3]string{"custom", "ai", "agent_name"}[i], "ok"
			return
		}
	}
	if hidden {
		s.TitleState = "redacted"
		return
	}
	for _, t := range c.turns {
		c.pulse()
		if t.out.RequestNo == nil || *t.out.RequestNo != 1 {
			continue
		}
		switch {
		case t.out.PreviewState == "redacted":
			s.TitleState = "redacted"
		case t.out.Preview != nil:
			title := *t.out.Preview
			s.Title, s.TitleSource, s.TitleState = &title, "first_request", "ok"
			if withheld {
				s.TitleState = "withheld"
			}
		}
	}
}

// snapshotAfter orders snapshots for the choice of the one to read: the latest
// observed, and between two observed together the same one every time.
func snapshotAfter(a, b *Snapshot) bool {
	switch {
	case !a.ObservedAt.Equal(b.ObservedAt):
		return a.ObservedAt.After(b.ObservedAt)
	case a.UserID != b.UserID:
		return a.UserID > b.UserID
	}
	return a.ProfileEmail > b.ProfileEmail
}
