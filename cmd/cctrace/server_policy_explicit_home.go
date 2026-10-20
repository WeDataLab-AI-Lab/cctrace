package main

import (
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"cctrace/internal/codexconfig"
	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
)

// explicitCodexHomeKeptNote is what an applied policy says when it leaves the
// OTEL block of the profile's explicit Codex home as it found it. It names no
// path: the home is a directory the user chose.
const explicitCodexHomeKeptNote = "explicit Codex home kept"

// explicitCodexHome is a profile's explicit Codex home as the policy handles it:
// a home of its own, written by the profile alone.
type explicitCodexHome struct {
	// config is the canonical path of the home's config.toml, empty when it has
	// none a policy could name: a symlink, which cannot be replaced in place.
	config string
}

// policyExplicitCodexHome says whether p has an explicit Codex home the policy
// handles by itself, given the scanned folders policyCodexConfigPaths names.
//
// A path has one kind, and the first that fits wins: the default folder, then the
// folders in codex_dirs and CODEX_HOME, then the explicit home. The first two are
// scanned: the policy treats them as shared with the profiles that scan them. The
// explicit home is one profile's. A home that is also scanned is the scanned
// folder, with the rules a scanned folder has always had, and a sibling that
// scans the explicit home does not make it shared.
//
// The profile must sync Codex and the home must still be the one that was
// configured, as the sync itself requires before it heals the home's block.
func policyExplicitCodexHome(p *profile.Profile, scanned []string) (explicitCodexHome, bool) {
	if p.Options.CodexSyncDeclined || !p.Options.CodexSyncEnabled || p.CodexHome == "" || profile.CheckCodexHome(p.CodexHome) != nil {
		return explicitCodexHome{}, false
	}
	config := filepath.Join(p.CodexHome, "config.toml")
	if slices.ContainsFunc(scanned, func(path string) bool { return samePolicyEntry(path, config) }) {
		return explicitCodexHome{}, false
	}
	canonical, err := canonicalPolicyPath(config)
	if err != nil {
		canonical = ""
	}
	return explicitCodexHome{config: canonical}, true
}

// explicitHomeVerdict is what a policy does with the OTEL block of an explicit
// Codex home.
type explicitHomeVerdict int

const (
	// explicitHomeKept is a block that is not the profile's generated one, or a
	// config the policy cannot rewrite. It stays as it is, and the run says so.
	explicitHomeKept explicitHomeVerdict = iota
	// explicitHomeMoved is the profile's block, moved to the policy's server.
	explicitHomeMoved
	// explicitHomeSettled needs nothing and says nothing: no config, a block
	// already on the policy's server, or another profile's block, which that
	// profile's own run moves.
	explicitHomeSettled
)

// explicitHomeMove is what decides one explicit home's block. It reads and writes
// nothing.
type explicitHomeMove struct {
	// olds are the OTEL addresses the profile's block may still be on, and oldCAs
	// the CA files it may name there: an empty one stands for a block with no CA.
	olds, oldCAs []string
	// to is the address the policy moves to and newCA the CA file the block names
	// there.
	to, newCA string
	// own is the profile's token, others the other profiles on this machine.
	own    string
	others []explicitHomeOwner
}

// explicitHomeOwner is another profile on this machine, by the token it sends.
type explicitHomeOwner struct {
	token string
	// covers says the profile's own policy run reaches the home, so it moves the
	// profile's block there itself.
	covers bool
}

// plan judges a config. A block is moved only when it is exactly what cctrace
// generates for the profile, as a scanned folder's is, except that the token is
// the one the block carries: the home keeps its own, and a policy moves a server,
// never credentials. A block with another profile's token is that profile's: left
// to its own run when that run reaches the home, and kept when it does not, since
// nobody would move it. The rest of the config is kept as it is.
func (m explicitHomeMove) plan(content []byte) ([]byte, explicitHomeVerdict) {
	token := codexconfig.OtelBearerTokenOf(content)
	if token != m.own {
		known := false
		for _, o := range m.others {
			if o.token != token {
				continue
			}
			if o.covers {
				return nil, explicitHomeSettled
			}
			known = true
		}
		if known {
			return nil, explicitHomeKept
		}
	}
	if _, err := codexconfig.PlanServerPolicyConfig(content, m.to, m.to, token, m.newCA, m.newCA); err == nil {
		return nil, explicitHomeSettled
	}
	for _, old := range m.olds {
		for _, oldCA := range m.oldCAs {
			if updated, err := codexconfig.PlanServerPolicyConfig(content, old, m.to, token, oldCA, m.newCA); err == nil {
				return updated, explicitHomeMoved
			}
		}
	}
	return nil, explicitHomeKept
}

// explicitHomeOlds are the addresses a block of p's may still be on: the one p
// sends to now, and the ones the policy moves from that are on the same host,
// where a block that sync healed before an earlier move may have stayed.
func explicitHomeOlds(p *profile.Profile, pol serverpolicy.Policy) []string {
	olds := []string{p.Server.Endpoint}
	for _, from := range pol.From {
		if !slices.Contains(olds, from.OTEL) && sameEndpointHost(from.OTEL, p.Server.Endpoint) {
			olds = append(olds, from.OTEL)
		}
	}
	return olds
}

func sameEndpointHost(a, b string) bool {
	hostA, hostB := endpointHost(a), endpointHost(b)
	if ipA, ipB := net.ParseIP(hostA), net.ParseIP(hostB); ipA != nil && ipB != nil {
		return ipA.Equal(ipB)
	}
	return hostA != "" && strings.EqualFold(hostA, hostB)
}

// endpointHost is the host of an endpoint, one without a scheme read as http.
func endpointHost(endpoint string) string {
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return strings.TrimSuffix(u.Hostname(), ".")
}

// planExplicitCodexHome works out the policy's move of p's explicit home, whose
// block newCA names the managed CA of. userCA is the certificate the user supplied
// before any policy, which the state remembers. It returns the config as it is and
// as it becomes when the verdict is explicitHomeMoved.
//
// The CAs a block may name are the profile's now and the user's: a block that sync
// healed before an earlier policy moved the profile names the one the profile had
// then, or none.
//
// What cannot be told is kept: a config that cannot be read, named or locked
// (locked says which locks the caller holds; nil makes no claim), and a block that
// might be another profile's when some profile cannot be read.
func planExplicitCodexHome(p *profile.Profile, name string, pol serverpolicy.Policy, userCA string, home explicitCodexHome, newCA string, locked map[string]bool) (before, after []byte, verdict explicitHomeVerdict) {
	if home.config == "" || (locked != nil && !locked[home.config]) {
		return nil, nil, explicitHomeKept
	}
	content, err := os.ReadFile(home.config)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, explicitHomeSettled
	}
	if err != nil {
		return nil, nil, explicitHomeKept
	}
	others, ok := otherProfiles(name, home.config)
	if !ok {
		return nil, nil, explicitHomeKept
	}
	oldCAs := []string{p.Server.CACertFile}
	if userCA != p.Server.CACertFile {
		oldCAs = append(oldCAs, userCA)
	}
	move := explicitHomeMove{olds: explicitHomeOlds(p, pol), oldCAs: oldCAs, to: pol.To.OTEL, newCA: newCA, own: p.Server.AuthToken, others: others}
	after, verdict = move.plan(content)
	return content, after, verdict
}

// otherProfiles are the profiles on this machine but name that send a token, and
// whether each reaches config. ok is false when some profile could not be read: its
// token is then unknown.
func otherProfiles(name, config string) (others []explicitHomeOwner, ok bool) {
	err := eachMachineProfile(func(other string, p *profile.Profile) {
		if other != name && p.Server.AuthToken != "" {
			others = append(others, explicitHomeOwner{p.Server.AuthToken, policyReachesCodexConfig(p, config)})
		}
	})
	return others, err == nil
}

// policyReachesCodexConfig reports whether p's own policy run writes the Codex
// config at path: it is one of the folders p scans, or p's explicit home.
func policyReachesCodexConfig(p *profile.Profile, path string) bool {
	scanned, err := policyCodexConfigPaths(p)
	if err != nil {
		return false
	}
	if slices.ContainsFunc(scanned, func(q string) bool { return samePolicyEntry(q, path) }) {
		return true
	}
	home, ok := policyExplicitCodexHome(p, scanned)
	return ok && home.config != "" && samePolicyEntry(home.config, path)
}
