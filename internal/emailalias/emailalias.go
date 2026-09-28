// Package emailalias resolves alias emails to canonical emails.
// Configure via EMAIL_ALIASES env var:
//
//	EMAIL_ALIASES=alias@example.com=canonical@example.com,other@example.com=canonical@example.com
package emailalias

import (
	"strings"
	"sync"
)

// Resolver resolves an alias email to its canonical form.
type Resolver interface {
	Resolve(email string) string
}

// Aliases maps alias email -> canonical email.
// Implements Resolver.
type Aliases map[string]string

// Parse parses the EMAIL_ALIASES env var value.
// Format: alias1=canonical1,alias2=canonical2
func Parse(envVal string) Aliases {
	a := make(Aliases)
	for _, pair := range strings.Split(envVal, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}
		alias := strings.TrimSpace(strings.ToLower(parts[0]))
		canonical := strings.TrimSpace(strings.ToLower(parts[1]))
		if alias != "" && canonical != "" {
			a[alias] = canonical
		}
	}
	return a
}

// Resolve returns the canonical email for the given alias, or the input unchanged.
func (a Aliases) Resolve(email string) string {
	if len(a) == 0 || email == "" {
		return email
	}
	if canonical, ok := a[strings.ToLower(email)]; ok {
		return canonical
	}
	return email
}

// DBLoader is a thread-safe, refreshable alias resolver backed by a DB-loaded map.
// It merges static env-var aliases with DB-persisted merge rules.
type DBLoader struct {
	mu sync.RWMutex
	m  map[string]string
}

// NewDBLoader creates an empty DBLoader.
func NewDBLoader() *DBLoader {
	return &DBLoader{m: make(map[string]string)}
}

// Refresh replaces the internal map atomically.
func (d *DBLoader) Refresh(m map[string]string) {
	d.mu.Lock()
	d.m = m
	d.mu.Unlock()
}

// Resolve returns the canonical email, or the input if no mapping exists.
func (d *DBLoader) Resolve(email string) string {
	if email == "" {
		return email
	}
	d.mu.RLock()
	canonical, ok := d.m[strings.ToLower(email)]
	d.mu.RUnlock()
	if ok {
		return canonical
	}
	return email
}
