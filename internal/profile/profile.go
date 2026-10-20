package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cctrace/internal/atomicfile"
)

// Profile holds user configuration for cctrace.
type Profile struct {
	HomeCollection   *HomeCollection  `json:"home_collection,omitempty"`
	HomesVersion     int              `json:"homes_version,omitempty"`
	Homes            []HomeMembership `json:"homes,omitempty"`
	ID               string           `json:"profile_id,omitempty"`
	RegistrationOnly bool             `json:"registration_only,omitempty"`
	Version          int              `json:"version"`
	User             UserInfo         `json:"user"`
	Server           ServerInfo       `json:"server"`
	Options          ProfileOptions   `json:"options"`
	CreatedAt        time.Time        `json:"created_at"`
	UpdatedAt        time.Time        `json:"updated_at"`
	ClaudeConfigDir  string           `json:"claude_config_dir,omitempty"`
	// CodexHome is an explicitly chosen execution home, separate from additional scan roots.
	CodexHome      string `json:"codex_home,omitempty"`
	sourcePath     string
	sourceRevision string
	sourceJSON     []byte
}

// UserInfo holds identity fields.
type UserInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Team  string `json:"team"`
}

// ServerInfo holds remote endpoint configuration.
type ServerInfo struct {
	Endpoint     string `json:"endpoint,omitempty"`      // OTEL exporter endpoint (gRPC/HTTP OTLP)
	SyncEndpoint string `json:"sync_endpoint,omitempty"` // HTTP REST endpoint for cctrace sync
	Protocol     string `json:"protocol,omitempty"`
	AuthToken    string `json:"auth_token,omitempty"`
	// ReadToken reads the Open API. The upload token in AuthToken is refused
	// there by design, so read commands use this one when it is set.
	ReadToken string `json:"read_token,omitempty"`
	// CACertFile is a PEM file of a private CA (Caddy's `tls internal` root, for
	// example) that every channel to this server trusts. One file serves all of
	// them because each client takes a CA from a different place: cctrace adds it
	// to a copy of the system roots, Claude Code's http/protobuf exporter takes
	// it from NODE_EXTRA_CA_CERTS, and Codex from its own [otel] tls table.
	// Empty means the system roots only.
	CACertFile string `json:"ca_cert_file,omitempty"`
}

// ProfileOptions holds behavioral toggles and intervals.
type ProfileOptions struct {
	LocalAnalysisEnabled bool `json:"local_analysis_enabled,omitempty"`
	SyncEnabled          bool `json:"sync_enabled"`
	// RedactUserPrompts drops the text a person typed before a record leaves this
	// machine. RedactToolDetails does the same for tool arguments and results.
	// Both default to false: cctrace exists to show conversations, so collecting
	// them is the point and turning it off is the deliberate act.
	//
	// They are phrased as "redact", not "log", because zero is the common case and
	// a bool that means "do the usual thing" reads correctly when unset.
	//
	// They replaced log_user_prompts / log_tool_details, which were read by nothing
	// -- present in `cctrace config`, settable, and connected to no code path since
	// the first commit. Reusing those names was not an option: profiles in the field
	// carry `"log_user_prompts": false`, and an implementation could not tell that
	// stored zero from a person choosing it. Honouring it would have silently
	// stopped prompt collection for every existing install; ignoring it would have
	// left the same lie in place. New names make the old value inert, which is what
	// it always was.
	RedactUserPrompts     bool `json:"redact_user_prompts,omitempty"`
	RedactToolDetails     bool `json:"redact_tool_details,omitempty"`
	MetricsExportInterval int  `json:"metrics_export_interval"`
	LogsExportInterval    int  `json:"logs_export_interval"`
	CodexSyncEnabled      bool `json:"codex_sync_enabled"`
	// CodexSyncDeclined records that the owner turned Codex sync off (init
	// answered n, or config set it false). A false CodexSyncEnabled alone cannot
	// tell that apart from a profile that predates Codex support, which the sync
	// opts in on its own (#765).
	CodexSyncDeclined bool `json:"codex_sync_declined,omitempty"`
	GjcSyncEnabled    bool `json:"gjc_sync_enabled"`
	OmoSyncEnabled    bool `json:"omo_sync_enabled"`
	// CollectRepositoryPrefixes restricts syncing to repositories whose
	// normalized id (host/org/repo) starts with one of these prefixes. Empty
	// means collect every repository (default). Set it to avoid collecting
	// personal repos, e.g. ["github.com/ExampleOrg/"].
	CollectRepositoryPrefixes []string `json:"collect_repository_prefixes,omitempty"`
	// ExcludeAccounts lists billing accounts whose session records are never
	// sent (#716): a personal subscription used on the same machine. An entry is
	// an account id, or provider:account_id to name one provider's account only.
	// Login addresses are not accepted: session records carry the account id and
	// no address, so an address would match nothing. OTEL is sent by Claude Code
	// and Codex themselves and does not pass through here.
	ExcludeAccounts []string `json:"exclude_accounts,omitempty"`
	// CodexDirs lists additional Codex home directories to scan for session
	// JSONL files, on top of the default home. Useful when a launcher sets a
	// different CODEX_HOME. Empty means scan only the default home.
	CodexDirs []string `json:"codex_dirs,omitempty"`
	// GjcDirs lists additional gjc home directories to scan for session
	// JSONL files, on top of the default home (~/.gjc). Gjc, like Codex,
	// supports multiple homes. Empty means scan only the default home.
	GjcDirs []string `json:"gjc_dirs,omitempty"`
	// OmoDirs lists additional omo home directories to scan for session
	// files, beyond the default ~/.omo. omo keeps sessions under two roots
	// already, and the one time a root went unscanned it hid 56 records and
	// 7.26M tokens while every screen still looked healthy (#247, #250).
	OmoDirs []string `json:"omo_dirs,omitempty"`
}

// ValidationError represents a single field validation failure.
type ValidationError struct {
	Field  string
	Reason string
}

func (e ValidationError) Error() string {
	return fmt.Sprintf("field %q: %s", e.Field, e.Reason)
}

// ValidationErrors is a slice of ValidationError that implements error.
type ValidationErrors []ValidationError

func (ve ValidationErrors) Error() string {
	msgs := make([]string, len(ve))
	for i, e := range ve {
		msgs[i] = e.Error()
	}
	return strings.Join(msgs, "; ")
}

// DefaultDir returns the default cctrace configuration directory (~/.cctrace).
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cctrace"
	}
	return filepath.Join(home, ".cctrace")
}

// DefaultPath returns the default profile.json path.
func DefaultPath() string {
	return filepath.Join(DefaultDir(), "profile.json")
}

// EnsureDir creates the default directory with 0700 permissions if it does not exist.
func EnsureDir() error {
	return os.MkdirAll(DefaultDir(), 0700)
}

// Load loads a profile from the default path.
func Load() (*Profile, error) {
	return LoadFrom(DefaultPath())
}

// LoadFrom loads a profile from the given path.
func LoadFrom(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("profile: unmarshal: %w", err)
	}
	if err := p.ValidateHomes(); err != nil {
		return nil, err
	}
	p.sourcePath = filepath.Clean(path)
	p.sourceRevision = revision(data)
	p.sourceJSON = data
	return &p, nil
}

// Save saves the profile to the default path atomically with 0600 permissions.
func Save(p *Profile) error {
	p.ID = "default"
	return SaveTo(p, DefaultPath())
}

// SaveTo saves the profile to the given path atomically with 0600 permissions,
// delegating the durable replace to internal/atomicfile.
func SaveTo(p *Profile, path string) error {
	return withProfileLock(profileRootForPath(path), func() error { return saveToUnlocked(p, path) })
}

func saveToUnlocked(p *Profile, path string) error {
	p.FenceLegacyCollector()
	if p.HomesVersion == 1 && p.RegistrationOnly && (p.CollectionEnabled("claude") || p.CollectionEnabled("codex")) && (p.User.ID != "" || p.User.Email != "") && (p.Server.Endpoint != "" || p.Server.SyncEndpoint != "") {
		p.RegistrationOnly = false
	}
	if err := p.ValidateHomes(); err != nil {
		return err
	}
	// Ensure parent directory exists.
	if err := ensureProfileParent(path); err != nil {
		return fmt.Errorf("profile: mkdir: %w", err)
	}

	// Default user.id to the local-part of user.email if not set.
	if strings.TrimSpace(p.User.ID) == "" && strings.Contains(p.User.Email, "@") {
		p.User.ID = strings.SplitN(p.User.Email, "@", 2)[0]
	}

	current, readErr := os.ReadFile(path)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if p.sourcePath == filepath.Clean(path) && p.sourceRevision != "" {
		if readErr != nil || revision(current) != p.sourceRevision {
			return ErrConflict
		}
	} else if readErr == nil {
		return ErrConflict
	}
	data, err := marshalPreservingUnknown(p)
	if err != nil {
		return fmt.Errorf("profile: marshal: %w", err)
	}

	// profile.json holds server.auth_token: losing it to a half-written file
	// breaks every later sync. atomicfile fsyncs both the file and its directory,
	// refuses a symlink or non-regular destination, never widens permissions, and
	// replaces durably on Windows — none of which the hand-rolled temp+rename
	// this used to carry did.
	if err := atomicfile.Write(path, data, 0600); err != nil {
		return fmt.Errorf("profile: write: %w", err)
	}
	p.sourcePath = filepath.Clean(path)
	p.sourceRevision = revision(data)
	p.sourceJSON = data
	return nil
}

// Exists returns true if the profile file exists at the default path.
func Exists() bool {
	return ExistsAt(DefaultPath())
}

// ExistsAt returns true if the profile file exists at the given path.
func ExistsAt(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Remove removes the profile file at the default path.
func Remove() error {
	return RemoveAt(DefaultPath())
}

// RemoveAt removes the profile file at the given path.
func RemoveAt(path string) error {
	return withProfileLock(profileRootForPath(path), func() error { return os.Remove(path) })
}

// Validate checks that all required fields are present. Returns ValidationErrors
// (implements error) if any fields are missing.
func Validate(p *Profile) error {
	var errs ValidationErrors
	if strings.TrimSpace(p.User.Name) == "" {
		errs = append(errs, ValidationError{Field: "user.name", Reason: "required"})
	}
	if strings.TrimSpace(p.User.Email) == "" {
		errs = append(errs, ValidationError{Field: "user.email", Reason: "required"})
	}
	if strings.TrimSpace(p.User.Team) == "" {
		errs = append(errs, ValidationError{Field: "user.team", Reason: "required"})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// NewDefault creates a new Profile with sensible defaults and current timestamps.
func NewDefault() *Profile {
	now := time.Now().UTC()
	return &Profile{
		Version: 1,
		Server: ServerInfo{
			Protocol: "grpc",
		},
		Options: ProfileOptions{
			SyncEnabled:           true,
			MetricsExportInterval: 60000,
			LogsExportInterval:    5000,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// validNameRe matches valid named profile names.
var validNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

// ValidateName returns an error if name is not a valid profile name.
// The name "default" is reserved and rejected.
func ValidateName(name string) error {
	if name == "default" {
		return fmt.Errorf("profile name %q is reserved", name)
	}
	if !validNameRe.MatchString(name) {
		return fmt.Errorf("profile name %q is invalid: must match ^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$", name)
	}
	return nil
}

// ProfilesDir returns the ~/.cctrace/profiles directory path.
func ProfilesDir() string {
	return filepath.Join(DefaultDir(), "profiles")
}

// NamedDir returns ~/.cctrace/profiles/{name} after validating the name.
func NamedDir(name string) (string, error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	return filepath.Join(ProfilesDir(), name), nil
}

// NamedPath returns ~/.cctrace/profiles/{name}/profile.json after validating the name.
func NamedPath(name string) (string, error) {
	dir, err := NamedDir(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "profile.json"), nil
}

// EnsureNamedDir creates ~/.cctrace/profiles/{name} with 0700 permissions.
func EnsureNamedDir(name string) error {
	dir, err := NamedDir(name)
	if err != nil {
		return err
	}
	return os.MkdirAll(dir, 0700)
}

// LoadNamed loads a profile from ~/.cctrace/profiles/{name}/profile.json.
func LoadNamed(name string) (*Profile, error) {
	path, err := NamedPath(name)
	if err != nil {
		return nil, err
	}
	p, err := LoadFrom(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, &notFoundError{name}
		}
		return nil, err
	}
	return p, nil
}

// notFoundError says no profile has the name: its profile.json does not exist. It
// still matches os.ErrNotExist, as the missing file it stands for does, so a
// caller that scans the named profiles can tell a directory that holds no profile
// from a profile that cannot be read.
type notFoundError struct{ name string }

func (e *notFoundError) Error() string {
	return fmt.Sprintf("profile %q not found; run 'cctrace init --profile %s' first", e.name, e.name)
}

func (e *notFoundError) Unwrap() error { return os.ErrNotExist }

// SaveNamed saves a profile to ~/.cctrace/profiles/{name}/profile.json.
func SaveNamed(p *Profile, name string) error {
	path, err := NamedPath(name)
	if err != nil {
		return err
	}
	p.ID = "named:" + name
	return SaveTo(p, path)
}

// RemoveNamed removes the entire ~/.cctrace/profiles/{name} directory.
func RemoveNamed(name string) error {
	dir, err := NamedDir(name)
	if err != nil {
		return err
	}
	return withProfileLock(DefaultDir(), func() error {
		p, err := LoadNamed(name)
		if err != nil {
			if _, statErr := os.Lstat(dir); errors.Is(statErr, os.ErrNotExist) {
				return nil
			}
			return ErrUnsafe
		}
		resolvedDir, err := resolveRemovalPath(dir)
		if err != nil {
			return ErrUnsafe
		}
		homes := append([]string{p.ClaudeConfigDir, p.CodexHome}, p.Options.CodexDirs...)
		homes = append(homes, p.Options.GjcDirs...)
		homes = append(homes, p.Options.OmoDirs...)
		for _, home := range homes {
			if home == "" {
				continue
			}
			resolvedHome, err := resolveRemovalPath(home)
			if err != nil || removalWithin(resolvedDir, resolvedHome) || removalWithin(resolvedHome, resolvedDir) {
				return ErrUnsafe
			}
		}
		return os.RemoveAll(dir)
	})
}

// ResolveByClaudeConfigDir scans named profiles and returns the name whose
// ClaudeConfigDir matches dir. Returns ("", nil) if no match is found.
func ResolveByClaudeConfigDir(dir string) (string, error) {
	names, err := ListNamed()
	if err != nil {
		return "", err
	}
	names = append([]string{"default"}, names...)
	canonical, e := filepath.EvalSymlinks(dir)
	if e == nil {
		dir = canonical
	}
	clean := filepath.Clean(dir)
	matches := []string{}
	explicit := false
	for _, name := range names {
		var p *Profile
		if name == "default" {
			p, err = Load()
		} else {
			p, err = LoadNamed(name)
		}
		if err != nil {
			continue
		}
		if p.HomesVersion != 0 || p.Homes != nil {
			explicit = true
			for _, h := range p.ProviderHomes("claude", "") {
				if h.Home == clean {
					matches = append(matches, name)
				}
			}
		} else if name != "default" && p.ClaudeConfigDir != "" && filepath.Clean(p.ClaudeConfigDir) == clean {
			matches = append(matches, name)
		}
	}
	if len(matches) > 1 && explicit {
		return "", fmt.Errorf("provider home belongs to multiple profiles")
	}
	if len(matches) > 0 {
		if matches[0] == "default" {
			return "", nil
		}
		return matches[0], nil
	}
	if explicit {
		return "", fmt.Errorf("provider home is not enrolled; complete onboarding before collection")
	}
	return "", nil
}

// ListNamed returns the names of all profiles in ~/.cctrace/profiles/.
// Returns nil, nil if the profiles directory does not exist.
func ListNamed() ([]string, error) {
	dir := ProfilesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("profile: list named: %w", err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
