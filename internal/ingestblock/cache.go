// Package ingestblock holds the set of sessions and projects that ingest must
// refuse, in memory, so the hot path can consult it without a database round trip.
//
// It is its own package rather than a field on the API server because two
// unrelated ingest paths need the same answer: the sync REST handler and the OTLP
// receivers. Putting it in either one would make the other import it, and
// otelrecv deliberately knows nothing about the store.
package ingestblock

import (
	"context"
	"strings"
	"sync"
)

// Sets is a point-in-time snapshot of both blocklists.
type Sets struct {
	DeletedSessions map[string]bool
	BlockedProjects map[string]bool
	// ExcludedAccounts holds excluded billing accounts, keyed by AccountKey: the
	// ones an admin excluded by billing id and the ones an excluded address was
	// seen with (#715). Codex rows carry nothing else to recognise an account by.
	ExcludedAccounts map[string]bool
	// ExcludedEmails holds excluded login addresses, lowercased. OTEL carries the
	// address and no billing id.
	ExcludedEmails map[string]bool
}

// AccountKey is how ExcludedAccounts is keyed. The provider is part of it: the
// same id under two providers is two accounts.
func AccountKey(provider, accountID string) string {
	return strings.ToLower(strings.TrimSpace(provider)) + "\x00" + strings.TrimSpace(accountID)
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// Loader reads the current sets. Kept as a function type so this package does not
// import the store, which would pull the whole storage layer into the OTLP
// receivers.
type Loader func(context.Context) (*Sets, error)

// Cache answers ingest's two questions from memory.
//
// A stale cache is safe in one direction only, and the direction matters. Missing
// a just-deleted session means a few records slip in and get swept later. Wrongly
// believing something is blocked would silently drop live data, which nothing
// sweeps back. So the cache only ever grows between refreshes -- deletes push into
// it immediately (Add) while removals wait for a refresh.
type Cache struct {
	mu   sync.RWMutex
	sets Sets
	// loadMu serializes Refresh. The periodic tick and the reload after an
	// exclusion change overlap, and without it the slower one wins even when it
	// read the older state.
	loadMu sync.Mutex
}

func New() *Cache {
	return &Cache{sets: Sets{
		DeletedSessions:  map[string]bool{},
		BlockedProjects:  map[string]bool{},
		ExcludedAccounts: map[string]bool{},
		ExcludedEmails:   map[string]bool{},
	}}
}

// Refresh replaces the snapshot. A failed load leaves the previous one in place:
// an empty set would mean "block nothing", turning a transient database error into
// silent re-collection of deleted sessions.
func (c *Cache) Refresh(ctx context.Context, load Loader) error {
	c.loadMu.Lock()
	defer c.loadMu.Unlock()
	sets, err := load(ctx)
	if err != nil {
		return err
	}
	if sets == nil {
		return nil
	}
	if sets.DeletedSessions == nil {
		sets.DeletedSessions = map[string]bool{}
	}
	if sets.BlockedProjects == nil {
		sets.BlockedProjects = map[string]bool{}
	}
	if sets.ExcludedAccounts == nil {
		sets.ExcludedAccounts = map[string]bool{}
	}
	emails := make(map[string]bool, len(sets.ExcludedEmails))
	for e := range sets.ExcludedEmails {
		emails[normalizeEmail(e)] = true
	}
	sets.ExcludedEmails = emails
	c.mu.Lock()
	c.sets = *sets
	c.mu.Unlock()
	return nil
}

// AddSession marks a session blocked without waiting for the next refresh. The
// delete API calls it so a session cannot come back in the window between the
// delete committing and the ticker firing -- a window a live `sync --watch` at a
// one-second interval will hit.
func (c *Cache) AddSession(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	c.sets.DeletedSessions[sessionID] = true
	c.mu.Unlock()
}

// AddProject is AddSession's counterpart for a project block.
func (c *Cache) AddProject(projectHash string) {
	if projectHash == "" {
		return
	}
	c.mu.Lock()
	c.sets.BlockedProjects[projectHash] = true
	c.mu.Unlock()
}

// SessionDeleted reports whether this session was deleted. The empty id is never
// blocked: it is not a session, it is the absence of one, and treating it as
// blocked would drop every record that carries no session id.
func (c *Cache) SessionDeleted(sessionID string) bool {
	if sessionID == "" {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sets.DeletedSessions[sessionID]
}

// ProjectBlocked reports whether this project refuses new collection.
func (c *Cache) ProjectBlocked(projectHash string) bool {
	if projectHash == "" {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sets.BlockedProjects[projectHash]
}

// AccountExcluded reports whether a billing account is excluded. A record with no
// account is never excluded: that is the absence of an identity, and most
// records have none.
func (c *Cache) AccountExcluded(provider, accountID string) bool {
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(accountID) == "" {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sets.ExcludedAccounts[AccountKey(provider, accountID)]
}

// EmailExcluded reports whether a login address is excluded, case-insensitively:
// ingest stores the address exactly as the client sent it.
func (c *Cache) EmailExcluded(email string) bool {
	e := normalizeEmail(email)
	if e == "" {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sets.ExcludedEmails[e]
}

// AddExcludedAccount and AddExcludedEmail apply an exclusion before the next
// refresh, so what an admin just excluded stops arriving at once.
func (c *Cache) AddExcludedAccount(provider, accountID string) {
	if strings.TrimSpace(provider) == "" || strings.TrimSpace(accountID) == "" {
		return
	}
	c.mu.Lock()
	c.sets.ExcludedAccounts[AccountKey(provider, accountID)] = true
	c.mu.Unlock()
}

func (c *Cache) AddExcludedEmail(email string) {
	e := normalizeEmail(email)
	if e == "" {
		return
	}
	c.mu.Lock()
	c.sets.ExcludedEmails[e] = true
	c.mu.Unlock()
}

// RemoveExcludedAccount and RemoveExcludedEmail undo an exclusion at once. This
// is the one place the cache shrinks between refreshes, because here waiting is
// the unsafe direction: a stale "excluded" drops live data nothing sweeps back.
func (c *Cache) RemoveExcludedAccount(provider, accountID string) {
	c.mu.Lock()
	delete(c.sets.ExcludedAccounts, AccountKey(provider, accountID))
	c.mu.Unlock()
}

func (c *Cache) RemoveExcludedEmail(email string) {
	c.mu.Lock()
	delete(c.sets.ExcludedEmails, normalizeEmail(email))
	c.mu.Unlock()
}

// Len reports both set sizes, for the startup log line.
func (c *Cache) Len() (sessions int, projects int) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.sets.DeletedSessions), len(c.sets.BlockedProjects)
}
