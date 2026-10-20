package codexconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var policyAccountHeader = regexp.MustCompile(`X-Cctrace-Codex-Account = ("(?:[^"\\]|\\.)*")`)

// PlanServerPolicyConfig plans a transition of a generated OTEL block without
// reading auth files or writing config. Exact generated content proves ownership
// of endpoint, token and CA together. Customized or legacy blocks are deferred
// rather than losing user exporter options. All non-OTEL text is preserved.
func PlanServerPolicyConfig(content []byte, oldEndpoint, newEndpoint, token, oldCA, newCA string) ([]byte, error) {
	existing := string(content)
	if hasMultilineRootOtelKey(existing) {
		return nil, ErrMultilineRootOtelKey
	}
	section := strings.Join(otelSectionLines(existing), "\n")
	account, err := policyAccount(section)
	if err != nil {
		return nil, err
	}
	want := strings.TrimSpace(buildOtelBlock(oldEndpoint, token, account, oldCA))
	if section != want {
		return nil, fmt.Errorf("unmanaged Codex OTEL settings differ from the profile; policy deferred")
	}
	// Replace the verified assignment only. Replacing whole sections would remove
	// comments and blank lines belonging to neighboring user configuration.
	oldLine := strings.Split(want, "\n")[1]
	newLine := strings.Split(strings.TrimSpace(buildOtelBlock(newEndpoint, token, account, newCA)), "\n")[1]
	lines := strings.Split(existing, "\n")
	inOtel := false
	var state tomlStringState
	for i, line := range lines {
		if state.inMultiline() {
			state.update(line)
			continue
		}
		if name, ok := tomlHeaderName(strings.TrimSpace(line)); ok {
			inOtel = isOtelTableName(name)
		}
		if inOtel && strings.TrimSpace(tomlLineBeforeComment(line)) == oldLine {
			lines[i] = strings.Replace(line, oldLine, newLine, 1)
		}
		state.update(line)
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// policyAccount is the Codex account header a generated OTEL section carries.
// Every writer of one Codex home reads the same auth file, so a policy rebuilds
// each candidate block with it rather than with an account of its own.
func policyAccount(section string) (string, error) {
	match := policyAccountHeader.FindStringSubmatch(section)
	if match == nil {
		return "", nil
	}
	account, err := strconv.Unquote(match[1])
	if err != nil {
		return "", fmt.Errorf("invalid Codex account header")
	}
	return account, nil
}

// PolicyBlock is what one profile's generated Codex OTEL block is built from.
type PolicyBlock struct {
	Endpoint string
	Token    string
	CA       string
}

// SharedBlock says whose generated block a shared Codex config holds.
type SharedBlock int

const (
	// SharedBlockOther is a block no profile given would write: edited by hand,
	// written by a local run, or from a profile that is not moving.
	SharedBlockOther SharedBlock = iota
	SharedBlockOld
	SharedBlockNew
	SharedBlockSibling
)

// SharedBlockPlan is the result of PlanSharedServerPolicyConfig.
type SharedBlockPlan struct {
	Holds SharedBlock
	// Content is the config moved onto the new block, set when Holds is
	// SharedBlockOld.
	Content []byte
	// Digests of the old and new blocks, rebuilt with the account header the
	// config holds now. The blocks carry the token; these do not reveal it.
	OldSHA256, NewSHA256 string
}

// PlanSharedServerPolicyConfig works out one profile's move from old to new in a
// Codex config other profiles write too, whose blocks are siblings. It reads
// and writes no files, needs no policy, and changes only a config that holds the
// old block exactly, as PlanServerPolicyConfig does. Old and new carry the same
// token: a policy moves a profile's server, never its credentials.
func PlanSharedServerPolicyConfig(content []byte, old, new PolicyBlock, siblings []PolicyBlock) (SharedBlockPlan, error) {
	var plan SharedBlockPlan
	if old.Token != new.Token {
		return plan, errors.New("policy blocks carry different tokens")
	}
	existing := string(content)
	if hasMultilineRootOtelKey(existing) {
		return plan, ErrMultilineRootOtelKey
	}
	section := strings.Join(otelSectionLines(existing), "\n")
	account, err := policyAccount(section)
	if err != nil {
		return plan, err
	}
	block := func(b PolicyBlock) string {
		return strings.TrimSpace(buildOtelBlock(b.Endpoint, b.Token, account, b.CA))
	}
	digest := func(text string) string {
		sum := sha256.Sum256([]byte(text))
		return hex.EncodeToString(sum[:])
	}
	plan.OldSHA256, plan.NewSHA256 = digest(block(old)), digest(block(new))
	switch section {
	case block(new):
		plan.Holds = SharedBlockNew
	case block(old):
		plan.Holds = SharedBlockOld
		plan.Content, err = PlanServerPolicyConfig(content, old.Endpoint, new.Endpoint, old.Token, old.CA, new.CA)
	default:
		for _, sibling := range siblings {
			if section == block(sibling) {
				plan.Holds = SharedBlockSibling
				break
			}
		}
	}
	return plan, err
}
