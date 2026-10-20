package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cctrace/internal/codexconfig"
	"cctrace/internal/configtxn"
	"cctrace/internal/profile"
	"cctrace/internal/serverpolicy"
)

// HOME, the signing key, the runtime seams and the sync locks are process-wide,
// so none of these tests run in parallel.

// explicitKeptNote is what a run says when it leaves an explicit Codex home's
// block as it found it.
const explicitKeptNote = "explicit Codex home kept"

// explicitHomeRun runs whole policy applications for the default profile, whose
// explicit Codex home is a directory of its own. "home" is a second profile on
// the same server that the policy does not name.
type explicitHomeRun struct {
	*sharedRunFixture
	home string
}

func newExplicitHomeRun(t *testing.T) *explicitHomeRun {
	t.Helper()
	f := unmovedSiblingRun(t)
	// The default folder holds the shared fixture's block. These tests are about
	// the explicit home, so the default folder holds none.
	if err := os.Remove(f.config); err != nil {
		t.Fatal(err)
	}
	home, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	editSibling(t, "", func(p *profile.Profile) { p.CodexHome, p.Options.CodexSyncEnabled = home, true })
	return &explicitHomeRun{f, home}
}

func (e *explicitHomeRun) configPath() string { return filepath.Join(e.home, "config.toml") }

func (e *explicitHomeRun) write(t *testing.T, content string) {
	t.Helper()
	if err := os.WriteFile(e.configPath(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (e *explicitHomeRun) content(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.configPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *explicitHomeRun) managedCA(t *testing.T) string {
	t.Helper()
	ca, err := policyCAPath("", e.pol)
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

// before is the block a profile's explicit home holds while it is on the old
// server; after is where the policy moves it.
func (e *explicitHomeRun) before(t *testing.T, token string) string {
	return e.block(t, e.a.Server.Endpoint, token, "")
}

func (e *explicitHomeRun) after(t *testing.T, token string) string {
	return e.block(t, e.pol.To.OTEL, token, e.managedCA(t))
}

// addFromAddress makes the policy move from a second address on the profile's
// host, and returns it.
func (e *explicitHomeRun) addFromAddress(t *testing.T) string {
	t.Helper()
	u, err := url.Parse(e.a.Server.Endpoint)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	address := fmt.Sprintf("%s://%s:%d", u.Scheme, u.Hostname(), port+1)
	e.pol.From = append(e.pol.From, serverpolicy.Endpoints{Sync: address, OTEL: address, Protocol: "http/protobuf"})
	e.serve(t, e.pol)
	return address
}

// previousPolicy leaves the profile as an earlier policy would have: its certificate
// is a managed file the state records, along with the user's certificate when
// there was one. It returns the two paths; the user's is empty without one.
func (e *explicitHomeRun) previousPolicy(t *testing.T, withUserCA bool) (managed, user string) {
	t.Helper()
	managed, err := canonicalPolicyPath(filepath.Join(profile.DefaultDir(), "server-policy-ca", "earlier.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(managed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(managed, []byte(e.pol.CAPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	if withUserCA {
		user = writeTestCAFile(t, t.TempDir())
	}
	sum := sha256.Sum256([]byte(e.pol.CAPEM))
	raw, err := json.Marshal(serverPolicyState{Sequence: e.pol.Sequence - 1, Digest: "earlier", ManagedCA: managed, ManagedCASHA256: hex.EncodeToString(sum[:]), UserCAFile: user, Status: "applied"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile.DefaultDir(), "server-policy-state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	editSibling(t, "", func(p *profile.Profile) { p.Server.CACertFile = managed })
	return managed, user
}

// withOtherSettings puts the user's own settings around a block, which the move
// must leave as they are.
func withOtherSettings(block string) string {
	return "model = \"custom\"\n\n" + block + "\n# my tools\n[mcp_servers.custom]\ncommand = \"keep\"\n"
}

// apply runs the policy and returns the state it leaves.
func (e *explicitHomeRun) apply(t *testing.T) serverPolicyState {
	t.Helper()
	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run: %v", err)
	}
	state := e.state(t, "")
	if state.Status != "applied" || state.Sequence != e.pol.Sequence || len(state.SharedWrites) != 0 {
		t.Fatalf("state %+v; want the policy applied with nothing left to write", state)
	}
	return state
}

// wantNote checks the note the run left on its applied record and reported.
func (e *explicitHomeRun) wantNote(t *testing.T, state serverPolicyState, note string) {
	t.Helper()
	if state.Reason != note {
		t.Fatalf("note %q, want %q", state.Reason, note)
	}
	wantOKReport(t, note)
}

func (e *explicitHomeRun) wantSettings(t *testing.T, want string) {
	t.Helper()
	if got := e.content(t); got != want {
		t.Fatalf("explicit home config:\n%s\nwant:\n%s", got, want)
	}
}

// The profile's own block, wherever else the user's settings sit, moves to the
// new server with the home's token and the managed CA; nothing is noted.
func TestServerPolicyRunMovesExplicitHomeBlock(t *testing.T) {
	e := newExplicitHomeRun(t)
	e.write(t, withOtherSettings(e.before(t, sharedRunTokenA)))

	state := e.apply(t)

	e.wantSettings(t, withOtherSettings(e.after(t, sharedRunTokenA)))
	e.wantNote(t, state, "")
}

// A block heal left on an address the profile has left since still belongs to it
// when the policy names that address among the ones it moves from.
func TestServerPolicyRunMovesExplicitHomeBlockLeftOnAFromAddress(t *testing.T) {
	e := newExplicitHomeRun(t)
	stale := e.addFromAddress(t)
	e.write(t, withOtherSettings(e.block(t, stale, sharedRunTokenA, "")))

	state := e.apply(t)

	e.wantSettings(t, withOtherSettings(e.after(t, sharedRunTokenA)))
	e.wantNote(t, state, "")
}

// A profile an earlier policy moved has the managed CA as its certificate, and the
// state remembers the one the user supplied, if any. A block that heal left before
// that move names the certificate the profile had then: none, or the user's.
func TestServerPolicyRunMovesExplicitHomeBlockOfAProfileAnEarlierPolicyMoved(t *testing.T) {
	for _, tc := range []struct {
		name   string
		userCA bool
		// block is the explicit home's block, given the managed CA the profile
		// has now, the user's CA and the address the policy moves from.
		block func(e *explicitHomeRun, t *testing.T, managed, user, stale string) string
		want  bool
	}{
		{"left on a From address, no certificate", false, func(e *explicitHomeRun, t *testing.T, _, _, stale string) string {
			return e.block(t, stale, sharedRunTokenA, "")
		}, true},
		{"left on a From address, with the user's certificate", true, func(e *explicitHomeRun, t *testing.T, _, user, stale string) string {
			return e.block(t, stale, sharedRunTokenA, user)
		}, true},
		{"on the profile's address, with the managed certificate", false, func(e *explicitHomeRun, t *testing.T, managed, _, _ string) string {
			return e.block(t, e.a.Server.Endpoint, sharedRunTokenA, managed)
		}, true},
		{"left on a From address, with a certificate the profile never had", false, func(e *explicitHomeRun, t *testing.T, _, _, stale string) string {
			return e.block(t, stale, sharedRunTokenA, "elsewhere.pem")
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newExplicitHomeRun(t)
			stale := e.addFromAddress(t)
			managed, user := e.previousPolicy(t, tc.userCA)
			content := tc.block(e, t, managed, user, stale)
			e.write(t, content)

			state := e.apply(t)

			if tc.want {
				e.wantSettings(t, e.after(t, sharedRunTokenA))
				e.wantNote(t, state, "")
			} else {
				e.wantSettings(t, content)
				e.wantNote(t, state, explicitKeptNote)
			}
		})
	}
}

// The block carries the Codex account header when the home has a login. The move
// keeps the header, as a scanned folder's does.
func TestServerPolicyRunMovesExplicitHomeBlockWithAccountHeader(t *testing.T) {
	e := newExplicitHomeRun(t)
	const auth = `{"tokens":{"account_id":"account-of-the-home"}}`
	withAccount := func(dir, endpoint, ca string) string {
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(auth), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := codexconfig.WriteOtelBlock(dir, endpoint, sharedRunTokenA, ca); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	before := withAccount(e.home, e.a.Server.Endpoint, "")
	if !strings.Contains(before, codexconfig.CodexAccountHeader) {
		t.Fatalf("the fixture block has no account header:\n%s", before)
	}
	want := withAccount(t.TempDir(), e.pol.To.OTEL, e.managedCA(t))

	state := e.apply(t)

	e.wantSettings(t, want)
	e.wantNote(t, state, "")
}

// The home's own token travels with its block: it is what the home sends, and a
// policy moves a server, not credentials.
func TestServerPolicyRunMovesExplicitHomeBlockWithItsOwnToken(t *testing.T) {
	e := newExplicitHomeRun(t)
	e.write(t, e.before(t, "token-of-no-profile"))

	e.apply(t)

	e.wantSettings(t, e.after(t, "token-of-no-profile"))
}

// The block carries another profile's token. When that profile's own policy run
// reaches this home it moves the block, and this run neither writes it nor says
// anything. When that profile never looks at the home nobody will, so the block
// is kept and the run says so.
func TestServerPolicyRunExplicitHomeBlockOfAnotherProfile(t *testing.T) {
	for _, tc := range []struct {
		name    string
		owner   func(p *profile.Profile, home string)
		reaches bool
	}{
		{"owner scans the home", func(p *profile.Profile, home string) { p.Options.CodexDirs = []string{home} }, true},
		{"owner has it as its explicit home", func(p *profile.Profile, home string) {
			p.CodexHome, p.Options.CodexSyncEnabled = home, true
		}, true},
		{"owner scans other folders only", func(p *profile.Profile, home string) {}, false},
		{"owner turned Codex sync off", func(p *profile.Profile, home string) {
			p.Options.CodexDirs, p.Options.CodexSyncDeclined = []string{home}, true
		}, false},
		{"owner has it as its explicit home with Codex sync off", func(p *profile.Profile, home string) {
			p.CodexHome = home
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newExplicitHomeRun(t)
			editSibling(t, "home", func(p *profile.Profile) { tc.owner(p, e.home) })
			other := e.before(t, sharedRunTokenHome)
			e.write(t, other)

			state := e.apply(t)

			e.wantSettings(t, other)
			if tc.reaches {
				e.wantNote(t, state, "")
			} else {
				e.wantNote(t, state, explicitKeptNote)
			}
		})
	}
}

// A block already on the policy's server is nobody's to change.
func TestServerPolicyRunLeavesExplicitHomeBlockAlreadyOnTheNewServer(t *testing.T) {
	e := newExplicitHomeRun(t)
	there := e.after(t, sharedRunTokenA)
	e.write(t, there)

	state := e.apply(t)

	e.wantSettings(t, there)
	e.wantNote(t, state, "")
}

// A directory among the named profiles that holds no profile does not make the
// block's owner unknown: it is no profile, so it is no reason to keep the block.
func TestServerPolicyRunMovesExplicitHomeBlockWithADirectoryWithoutProfile(t *testing.T) {
	e := newExplicitHomeRun(t)
	if err := os.MkdirAll(filepath.Join(profile.ProfilesDir(), "stray"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.write(t, e.before(t, sharedRunTokenA))

	state := e.apply(t)

	e.wantSettings(t, e.after(t, sharedRunTokenA))
	e.wantNote(t, state, "")
}

// A home with no config has nothing to move and nothing to say, as a scanned
// folder without one.
func TestServerPolicyRunExplicitHomeWithoutConfig(t *testing.T) {
	e := newExplicitHomeRun(t)

	state := e.apply(t)

	if _, err := os.Stat(e.configPath()); !os.IsNotExist(err) {
		t.Fatalf("the run created the config: %v", err)
	}
	e.wantNote(t, state, "")
}

// What is not the profile's generated block is not the policy's to rewrite. The
// run does not defer on it, as it does for a scanned folder: the home is not
// shared the way a scanned one is, and its block can be moved by the next sync.
// It says so in the note, which carries no path.
func TestServerPolicyRunKeepsExplicitHomeBlockItDoesNotOwn(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, e *explicitHomeRun)
	}{
		{"no otel section", func(t *testing.T, e *explicitHomeRun) { e.write(t, "model = \"custom\"\n") }},
		{"another collector", func(t *testing.T, e *explicitHomeRun) {
			e.write(t, e.block(t, "http://collector.example.test:4317", sharedRunTokenA, ""))
		}},
		{"hand-edited block", func(t *testing.T, e *explicitHomeRun) {
			e.write(t, strings.Replace(e.before(t, sharedRunTokenA), "[otel]", "[otel]\nlog_user_prompt = true", 1))
		}},
		{"multi-line root key", func(t *testing.T, e *explicitHomeRun) { e.write(t, "otel.exporter = \"\"\"\nx\n\"\"\"\n") }},
		{"legacy table form", func(t *testing.T, e *explicitHomeRun) {
			e.write(t, "[otel.exporter.otlp-http]\nendpoint = "+strconv.Quote(e.a.Server.Endpoint)+"\n")
		}},
		{"unreadable config", func(t *testing.T, e *explicitHomeRun) {
			if err := os.Mkdir(e.configPath(), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"symlinked config", func(t *testing.T, e *explicitHomeRun) {
			target := filepath.Join(t.TempDir(), "real.toml")
			if err := os.WriteFile(target, []byte(e.before(t, sharedRunTokenA)), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, e.configPath()); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newExplicitHomeRun(t)
			tc.setup(t, e)
			before, beforeErr := os.ReadFile(e.configPath())
			infoBefore, _ := os.Lstat(e.configPath())

			state := e.apply(t)

			e.wantNote(t, state, explicitKeptNote)
			after, afterErr := os.ReadFile(e.configPath())
			infoAfter, _ := os.Lstat(e.configPath())
			if !bytes.Equal(before, after) || (beforeErr == nil) != (afterErr == nil) || infoBefore.Mode() != infoAfter.Mode() {
				t.Fatalf("the config changed: %q then %q", before, after)
			}
		})
	}
}

// A home the profile does not sync Codex through, or that is no longer a usable
// home, is not the policy's at all.
func TestServerPolicyRunIgnoresExplicitHomeThatIsNotInUse(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, e *explicitHomeRun)
	}{
		{"codex sync off", func(t *testing.T, e *explicitHomeRun) {
			editSibling(t, "", func(p *profile.Profile) { p.Options.CodexSyncEnabled = false })
		}},
		{"codex sync declined", func(t *testing.T, e *explicitHomeRun) {
			editSibling(t, "", func(p *profile.Profile) { p.Options.CodexSyncDeclined = true })
		}},
		{"home gone", func(t *testing.T, e *explicitHomeRun) {
			if err := os.RemoveAll(e.home); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newExplicitHomeRun(t)
			e.write(t, e.before(t, sharedRunTokenA))
			unchanged := e.content(t)
			tc.setup(t, e)

			state := e.apply(t)

			e.wantNote(t, state, "")
			if got, err := os.ReadFile(e.configPath()); err == nil && string(got) != unchanged {
				t.Fatalf("the config changed:\n%s", got)
			}
		})
	}
}

// A sibling that lists the explicit home among its scanned folders does not make
// the home shared: no new deferral, and the profile's block still moves.
func TestServerPolicyRunExplicitHomeScannedBySiblingDoesNotDefer(t *testing.T) {
	e := newExplicitHomeRun(t)
	editSibling(t, "home", func(p *profile.Profile) {
		p.Options.CodexSyncEnabled, p.Options.CodexDirs = true, []string{e.home}
	})
	e.write(t, e.before(t, sharedRunTokenA))

	state := e.apply(t)

	e.wantSettings(t, e.after(t, sharedRunTokenA))
	e.wantNote(t, state, "")
}

// The explicit home is also the default folder, or another name of it on a disk
// that ignores case: the default folder's rules apply, and the explicit home's
// do not. A block that is nobody's is kept by the default folder's rule, with its
// note; the explicit home's rule would keep it with its own.
func TestServerPolicyRunExplicitHomeThatIsTheDefaultFolder(t *testing.T) {
	t.Run("the folder itself", func(t *testing.T) {
		f := unmovedSiblingRun(t)
		home, err := profile.NormalizeCodexHome(f.codexDir)
		if err != nil {
			t.Fatal(err)
		}
		editSibling(t, "", func(p *profile.Profile) { p.CodexHome, p.Options.CodexSyncEnabled = home, true })
		f.writeConfig(t, f.editedBlock(t))
		edited := f.read(t)

		if _, err := runServerPolicy(context.Background(), "", false); err != nil {
			t.Fatalf("run: %v", err)
		}

		// The folder is the profile's explicit home, so the note names it by the mask.
		note := "default Codex config kept: [explicit Codex home]" + string(filepath.Separator) + "config.toml"
		if got := f.read(t); got != edited {
			t.Fatalf("default folder config changed:\n%s", got)
		}
		if state := f.state(t, ""); state.Status != "applied" || state.Reason != note {
			t.Fatalf("state %q %q; want applied with %q", state.Status, state.Reason, note)
		}
	})
	t.Run("a case alias of the folder", func(t *testing.T) {
		f := unmovedSiblingRun(t)
		alias, err := profile.NormalizeCodexHome(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		original := policyStatFn
		t.Cleanup(func() { policyStatFn = original })
		policyStatFn = func(path string) (os.FileInfo, error) {
			if path == alias {
				path = f.codexDir
			}
			return os.Stat(path)
		}
		editSibling(t, "", func(p *profile.Profile) { p.CodexHome, p.Options.CodexSyncEnabled = alias, true })
		// The alias's own file would be moved if the explicit home's rule applied.
		aliasConfig := filepath.Join(alias, "config.toml")
		if err := os.WriteFile(aliasConfig, []byte(f.oldBlock(t)), 0o600); err != nil {
			t.Fatal(err)
		}
		f.writeConfig(t, f.editedBlock(t))
		note := "default Codex config kept: " + f.config

		if _, err := runServerPolicy(context.Background(), "", false); err != nil {
			t.Fatalf("run: %v", err)
		}

		if got, err := os.ReadFile(aliasConfig); err != nil || string(got) != f.oldBlock(t) {
			t.Fatalf("the alias's config was handled as an explicit home's: %q %v", got, err)
		}
		if state := f.state(t, ""); state.Status != "applied" || state.Reason != note {
			t.Fatalf("state %q %q; want applied with %q", state.Status, state.Reason, note)
		}
	})
}

// The explicit home is also one of the scanned folders: the strict rule that
// applies to those applies to it, as before, and it is handled once.
func TestServerPolicyRunExplicitHomeThatIsAScannedFolder(t *testing.T) {
	scanned := func(t *testing.T, e *explicitHomeRun) {
		editSibling(t, "", func(p *profile.Profile) { p.Options.CodexDirs = []string{e.home} })
	}
	t.Run("generated block moves", func(t *testing.T) {
		e := newExplicitHomeRun(t)
		scanned(t, e)
		e.write(t, e.before(t, sharedRunTokenA))

		state := e.apply(t)

		e.wantSettings(t, e.after(t, sharedRunTokenA))
		e.wantNote(t, state, "")
	})
	t.Run("customized block defers", func(t *testing.T) {
		e := newExplicitHomeRun(t)
		scanned(t, e)
		customized := strings.Replace(e.before(t, sharedRunTokenA), "[otel]", "[otel]\nlog_user_prompt = true", 1)
		e.write(t, customized)

		_, err := runServerPolicy(context.Background(), "", false)

		if err == nil || !strings.HasSuffix(err.Error(), policyDeferredSuffix) {
			t.Fatalf("run ended with %v, want a deferral", err)
		}
		e.wantSettings(t, customized)
		if state := e.state(t, ""); state.Status != "failed" || strings.Contains(state.Reason, e.home) {
			t.Fatalf("state %q %q; want a failure recorded without the home's path", state.Status, state.Reason)
		}
	})
}

// A lock the home's writers hold is waited for like the others, and the report
// that says so does not name the home.
func TestServerPolicyRunWaitsForExplicitHomeLock(t *testing.T) {
	e := newExplicitHomeRun(t)
	block := e.before(t, sharedRunTokenA)
	e.write(t, block)
	shortPolicyLockWait(t)
	lockPath, err := canonicalPolicyPath(e.configPath() + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	release := holdPolicyLock(t, lockPath)

	err = runPolicyWithinBound(t)

	wantPolicyLockBusy(t, err)
	e.wantSettings(t, block)
	report, _, rerr := buildPolicyReport("", err)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if strings.Contains(report.CheckReason, e.home) || !strings.HasPrefix(report.CheckReason, "policy lock busy: [explicit Codex home]") {
		t.Fatalf("report reason %q", report.CheckReason)
	}
	release()
	e.apply(t)
	e.wantSettings(t, e.after(t, sharedRunTokenA))
}

// A lock the explicit home's writers could use but this run cannot take, for a
// reason waiting would not cure, does not stop the policy: the home is not shared
// with anyone the policy must wait for, so the run leaves its block as it is and
// says so. A folder the profile scans is shared, and its lock still stops the run.
func TestServerPolicyRunExplicitHomeLockThatCannotBeTaken(t *testing.T) {
	for _, scanned := range []bool{false, true} {
		name := "explicit home only"
		if scanned {
			name = "scanned folder"
		}
		t.Run(name, func(t *testing.T) {
			e := newExplicitHomeRun(t)
			if scanned {
				editSibling(t, "", func(p *profile.Profile) { p.Options.CodexDirs = []string{e.home} })
			}
			block := e.before(t, sharedRunTokenA)
			e.write(t, block)
			// A symlink where the lock file belongs, which no writer follows.
			if err := os.Symlink(filepath.Join(t.TempDir(), "elsewhere.lock"), e.configPath()+".lock"); err != nil {
				t.Fatal(err)
			}

			if scanned {
				if _, err := runServerPolicy(context.Background(), "", false); err == nil {
					t.Fatal("the run went on without a lock on a scanned folder")
				}
				e.wantSettings(t, block)
				return
			}
			state := e.apply(t)

			e.wantSettings(t, block)
			e.wantNote(t, state, explicitKeptNote)
		})
	}
}

// A config that cannot be locked by name cannot hold up a recovery, or a sync
// starting behind one: the home is left out of the locks, like the policy leaves
// out its block.
func TestExplicitHomeSymlinkedConfigDoesNotBlockRecovery(t *testing.T) {
	usePolicyRuntimeHome(t)
	home, err := profile.NormalizeCodexHome(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "real.toml")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "config.toml")); err != nil {
		t.Fatal(err)
	}
	editSibling(t, "", func(p *profile.Profile) { p.CodexHome, p.Options.CodexSyncEnabled = home, true })
	// An interrupted application waits for the next sync start to roll it back.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal, err := policyPath("", "server-policy-txn")
	if err != nil {
		t.Fatal(err)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected interruption")
			}
		}()
		_ = configtxn.Apply(journal, []configtxn.Change{{Path: path, Before: []byte("old"), BeforeExists: true, After: []byte("new"), Mode: 0o600}}, func() error { panic("interrupted") })
	}()

	release, err := policySyncStartupBarrier(context.Background(), "", "", false)
	if err != nil {
		t.Fatalf("sync start behind the recovery: %v", err)
	}
	defer release()

	if got, _ := os.ReadFile(path); string(got) != "old" {
		t.Fatalf("the recovery left %q", got)
	}
}

func TestPlanExplicitHomeMove(t *testing.T) {
	oldEndpoint, newEndpoint := "http://trace.example.test:4317", "https://trace.example.test:5317"
	const ownToken, otherToken, absentToken, strangerToken = "own-token", "other-token", "absent-token", "stranger-token"
	move := explicitHomeMove{
		olds: []string{oldEndpoint}, oldCAs: []string{"user.pem", ""}, to: newEndpoint, newCA: "root.pem",
		own: ownToken, others: []explicitHomeOwner{{otherToken, true}, {absentToken, false}},
	}
	block := func(endpoint, token, ca string) string {
		dir := t.TempDir()
		if err := codexconfig.WriteOtelBlock(dir, endpoint, token, ca); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	for _, tc := range []struct {
		name    string
		content string
		want    explicitHomeVerdict
		moved   string
	}{
		{"own block", withOtherSettings(block(oldEndpoint, ownToken, "user.pem")), explicitHomeMoved, withOtherSettings(block(newEndpoint, ownToken, "root.pem"))},
		{"block of no profile", block(oldEndpoint, strangerToken, "user.pem"), explicitHomeMoved, block(newEndpoint, strangerToken, "root.pem")},
		{"own block with no CA", block(oldEndpoint, ownToken, ""), explicitHomeMoved, block(newEndpoint, ownToken, "root.pem")},
		{"another profile's block", block(oldEndpoint, otherToken, "user.pem"), explicitHomeSettled, ""},
		{"another profile's block on the new server", block(newEndpoint, otherToken, "root.pem"), explicitHomeSettled, ""},
		{"block of a profile that never looks at the home", block(oldEndpoint, absentToken, "user.pem"), explicitHomeKept, ""},
		{"already on the new server", block(newEndpoint, ownToken, "root.pem"), explicitHomeSettled, ""},
		{"profile's CA differs", block(oldEndpoint, ownToken, "other.pem"), explicitHomeKept, ""},
		{"another collector", block("http://other.example.test:4317", ownToken, "user.pem"), explicitHomeKept, ""},
		{"no token to match", "model = \"custom\"\n", explicitHomeKept, ""},
	} {
		got, verdict := move.plan([]byte(tc.content))
		if verdict != tc.want || (verdict == explicitHomeMoved && string(got) != tc.moved) || (verdict != explicitHomeMoved && got != nil) {
			t.Errorf("%s: verdict %d content %q; want verdict %d content %q", tc.name, verdict, got, tc.want, tc.moved)
		}
	}
}

// The addresses a block may still be on: the profile's, and the ones the policy
// moves from that are on the profile's host. A policy is signed for one host, so
// another's address cannot be among them in practice; one that is must not be
// followed.
func TestExplicitHomeOldEndpoints(t *testing.T) {
	p := profile.NewDefault()
	p.Server.Endpoint = "http://trace.example.test:4317"
	pol := serverpolicy.Policy{From: []serverpolicy.Endpoints{
		{OTEL: "http://trace.example.test:4317"},
		{OTEL: "http://TRACE.example.test:14317"},
		{OTEL: "http://elsewhere.example.test:4317"},
		{OTEL: "http://trace.example.test:14317"},
	}}

	got := explicitHomeOlds(p, pol)

	want := []string{"http://trace.example.test:4317", "http://TRACE.example.test:14317", "http://trace.example.test:14317"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("addresses %v, want %v", got, want)
	}
}

// storeState replaces the default profile's policy state.
func storeState(t *testing.T, state serverPolicyState) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile.DefaultDir(), "server-policy-state.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A write the applied policy left for later may end in the default folder keeping
// another's block. The note that says so adds to the one the policy left on its
// explicit home, and does not replace it.
func TestServerPolicyDrainKeepsTheExplicitHomeNote(t *testing.T) {
	f := newSharedRunFixture(t)
	f.leaveWrite(t)
	f.writeConfig(t, f.editedBlock(t))
	state := f.state(t, "")
	state.Reason = explicitKeptNote
	storeState(t, state)
	want := explicitKeptNote + "; default Codex config kept: " + f.config

	if _, err := runServerPolicy(context.Background(), "", false); err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := f.state(t, ""); got.Reason != want {
		t.Fatalf("note %q, want %q", got.Reason, want)
	}
	wantOKReport(t, want)
}

// The write that waited is what made the run fail, and finishing it turns the
// failure back into the applied record the policy made. That record carried the
// explicit home's note, which the new one keeps, with the default folder's when
// the write ends in keeping another's block.
func TestServerPolicyDrainOfAFailedRunKeepsTheExplicitHomeNote(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit bool
		want func(f *sharedRunFixture) string
	}{
		{"the write is carried out", false, func(*sharedRunFixture) string { return explicitKeptNote }},
		{"the file keeps another's block", true, func(f *sharedRunFixture) string {
			return explicitKeptNote + "; default Codex config kept: " + f.config
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedRunFixture(t)
			f.leaveWrite(t)
			if tc.edit {
				f.writeConfig(t, f.editedBlock(t))
			}
			state := f.state(t, "")
			pending := errSharedPolicyWritePending.Error()
			state.Status, state.Reason = "failed", pending
			state.History = []serverPolicyRecord{
				{Sequence: state.Sequence, Status: "applied", Reason: explicitKeptNote, At: time.Now().UTC()},
				{Sequence: state.Sequence, Status: "failed", Reason: pending, At: time.Now().UTC()},
			}
			storeState(t, state)
			want := tc.want(f)

			if _, err := runServerPolicy(context.Background(), "", false); err != nil {
				t.Fatalf("run: %v", err)
			}

			got := f.state(t, "")
			last := got.History[len(got.History)-1]
			if got.Status != "applied" || got.Reason != want || last.Status != "applied" || last.Reason != want || len(got.SharedWrites) != 0 {
				t.Fatalf("state %q %q, last record %q %q; want applied with %q", got.Status, got.Reason, last.Status, last.Reason, want)
			}
			wantOKReport(t, want)
		})
	}
}

// An interrupted application whose journal holds the explicit home's config rolls
// back on the next sync start, like the journal's other files. A file someone
// changed since, or turned into a symlink, is not the journal's to overwrite:
// recovery refuses, leaves every file as it found it, and the sync start fails
// with that error until a person settles the file. The report of a policy run
// that meets it does not name the home.
func TestExplicitHomeInJournalRecovery(t *testing.T) {
	for _, tc := range []struct {
		name string
		// change is what happens to the config between the interruption and the
		// recovery.
		change  func(t *testing.T, config string)
		recover bool
	}{
		{"untouched", func(*testing.T, string) {}, true},
		{"edited since", func(t *testing.T, config string) {
			if err := os.WriteFile(config, []byte("model = \"edited\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"turned into a symlink", func(t *testing.T, config string) {
			target := filepath.Join(t.TempDir(), "real.toml")
			if err := os.WriteFile(target, []byte("after"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(config); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, config); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			usePolicyRuntimeHome(t)
			home, err := profile.NormalizeCodexHome(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			editSibling(t, "", func(p *profile.Profile) { p.CodexHome, p.Options.CodexSyncEnabled = home, true })
			config, err := canonicalPolicyPath(filepath.Join(home, "config.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(config, []byte("before"), 0o600); err != nil {
				t.Fatal(err)
			}
			journal, err := policyPath("", "server-policy-txn")
			if err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					if recover() == nil {
						t.Fatal("expected interruption")
					}
				}()
				_ = configtxn.Apply(journal, []configtxn.Change{{Path: config, Before: []byte("before"), BeforeExists: true, After: []byte("after"), Mode: 0o600}}, func() error { panic("interrupted") })
			}()
			tc.change(t, config)
			changed, changedErr := os.ReadFile(config)

			release, err := policySyncStartupBarrier(context.Background(), "", "", false)

			if tc.recover {
				if err != nil {
					t.Fatalf("sync start: %v", err)
				}
				release()
				if got, _ := os.ReadFile(config); string(got) != "before" {
					t.Fatalf("the recovery left %q", got)
				}
				return
			}
			if err == nil {
				release()
				t.Fatal("sync started over a config the recovery could not roll back")
			}
			if got, gotErr := os.ReadFile(config); (gotErr == nil) != (changedErr == nil) || !bytes.Equal(got, changed) {
				t.Fatalf("recovery changed the config to %q", got)
			}
			if !serverPolicyRecoveryPending("") {
				t.Fatal("the journal was cleared though recovery failed")
			}
			_, runErr := runServerPolicy(context.Background(), "", false)
			if runErr == nil {
				t.Fatal("a policy run went on over a journal that cannot be rolled back")
			}
			report, _, rerr := buildPolicyReport("", runErr)
			if rerr != nil {
				t.Fatal(rerr)
			}
			if strings.Contains(report.CheckReason, home) {
				t.Fatalf("report reason %q names the home", report.CheckReason)
			}
		})
	}
}
