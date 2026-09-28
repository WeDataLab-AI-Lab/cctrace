// Package commandclass decides what a slash command a Claude session recorded
// actually was: where it came from, and whether it was a command or a skill.
//
// The session log does not say. A record carries only the name:
//
//	<command-name>/copy</command-name>
//	<command-message>copy</command-message>
//	<command-args></command-args>
//
// So the answer has to come from the machine that ran it, while it still has the
// files. The server cannot do this later -- plugins get upgraded and uninstalled,
// and by then the directory that would have answered is gone.
package commandclass

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Source is where a command came from.
type Source string

// Kind is what it was.
type Kind string

const (
	SourceBuiltin   Source = "builtin"
	SourceProject   Source = "project"
	SourceUser      Source = "user"
	SourcePlugin    Source = "plugin"
	SourceAmbiguous Source = "ambiguous"
	SourceUnknown   Source = "unknown"

	KindCommand Kind = "command"
	KindSkill   Kind = "skill"
	KindUnknown Kind = "unknown"
)

// Class is one verdict.
type Class struct {
	Source Source
	Kind   Kind
}

// Index holds what the machine carries, read once per sync run.
type Index struct {
	pluginCommands map[string]bool
	pluginSkills   map[string]bool
	userCommands   map[string]bool
	userSkills     map[string]bool
	projCommands   map[string]bool
	projSkills     map[string]bool
}

// NewIndex reads the command and skill directories under a Claude home and a
// project checkout. A missing directory is not an error: it means that source
// contributes nothing.
func NewIndex(claudeDir, projectDir string) *Index {
	idx := &Index{
		pluginCommands: map[string]bool{}, pluginSkills: map[string]bool{},
		userCommands: map[string]bool{}, userSkills: map[string]bool{},
		projCommands: map[string]bool{}, projSkills: map[string]bool{},
	}
	if claudeDir != "" {
		plugins := filepath.Join(claudeDir, "plugins")
		// Both layouts, and the versioned cache copies of each. Stopping one level
		// short here is what reports a plugin's own command as a builtin.
		for _, root := range []string{
			filepath.Join(plugins, "marketplaces", "*", "plugins", "*"),
			filepath.Join(plugins, "marketplaces", "*"),
			filepath.Join(plugins, "cache", "*", "*", "*"),
			filepath.Join(plugins, "local"),
		} {
			idx.readCommands(root, idx.pluginCommands)
			idx.readSkills(root, idx.pluginSkills)
		}
		idx.readCommands(claudeDir, idx.userCommands)
		idx.readSkills(claudeDir, idx.userSkills)
	}
	if projectDir != "" {
		dotClaude := filepath.Join(projectDir, ".claude")
		idx.readCommands(dotClaude, idx.projCommands)
		idx.readSkills(dotClaude, idx.projSkills)
	}
	return idx
}

// readCommands records every <root>/commands/<name>.md.
func (i *Index) readCommands(rootGlob string, into map[string]bool) {
	for _, p := range globFiles(filepath.Join(rootGlob, "commands", "*.md")) {
		into[strings.TrimSuffix(filepath.Base(p), ".md")] = true
	}
}

// readSkills records every <root>/skills/<name>/SKILL.md.
func (i *Index) readSkills(rootGlob string, into map[string]bool) {
	for _, p := range globFiles(filepath.Join(rootGlob, "skills", "*", "SKILL.md")) {
		into[filepath.Base(filepath.Dir(p))] = true
	}
}

func globFiles(pattern string) []string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && !st.IsDir() {
			out = append(out, m)
		}
	}
	return out
}

// Classify resolves one recorded command name.
//
// A namespaced name ("plugin:thing") states its own origin, so the source is
// settled even when the plugin is no longer installed; only the kind has to be
// looked up. A bare name has to be resolved against the directories, and when it
// appears in more than one source the honest answer is that we cannot tell:
// "compact" is both a Claude Code builtin and a command one plugin ships, and
// picking either is wrong for everyone who meant the other.
func (i *Index) Classify(name string) Class {
	if name == "" {
		return Class{SourceUnknown, KindUnknown}
	}
	if base, ok := namespaced(name); ok {
		return Class{SourcePlugin, i.pluginKind(base)}
	}

	var found []Class
	if i.projCommands[name] {
		found = append(found, Class{SourceProject, KindCommand})
	}
	if i.projSkills[name] {
		found = append(found, Class{SourceProject, KindSkill})
	}
	if i.userCommands[name] {
		found = append(found, Class{SourceUser, KindCommand})
	}
	if i.userSkills[name] {
		found = append(found, Class{SourceUser, KindSkill})
	}
	if k := i.pluginKind(name); k != KindUnknown {
		found = append(found, Class{SourcePlugin, k})
	}

	if builtins[name] {
		if len(found) > 0 {
			// Shipped with the binary AND provided by something on disk. The record
			// carries only the name, so which one ran is not recoverable.
			return Class{SourceAmbiguous, KindUnknown}
		}
		if builtinSkills[name] {
			return Class{SourceBuiltin, KindSkill}
		}
		return Class{SourceBuiltin, KindCommand}
	}

	switch len(found) {
	case 0:
		// Nothing on disk answers to it and it is not a builtin we know of. It may be
		// a plugin since uninstalled, or a builtin added after this list was written.
		// Both are better left unstated than guessed.
		return Class{SourceUnknown, KindUnknown}
	case 1:
		return found[0]
	default:
		return Class{SourceAmbiguous, KindUnknown}
	}
}

// pluginKind reports how a plugin carries this name, if it carries it at all.
// A plugin that ships both a command and a skill under one name cannot be read
// from the name either.
func (i *Index) pluginKind(base string) Kind {
	cmd, skill := i.pluginCommands[base], i.pluginSkills[base]
	switch {
	case cmd && skill:
		return KindUnknown
	case skill:
		return KindSkill
	case cmd:
		return KindCommand
	default:
		return KindUnknown
	}
}

// builtins are the Claude Code commands that ship with the binary.
//
// They leave no file anywhere, which is the whole difficulty: their existence can
// only be asserted, never observed. The list is therefore used to DETECT collisions,
// not to exclude names -- when a name is both a builtin and something on disk, the
// record cannot tell us which one the person ran, and the classifier says so.
//
// A stale list degrades in the safe direction. A builtin we forgot to list, and that
// nothing on disk answers to, comes back as unknown rather than being mislabelled.
// The names each agent's own binary answers to.
//
// Generated by reading the vendors' command tables, not by recall:
//
//	https://code.claude.com/docs/en/commands.md          (2026-08-21)
//	https://learn.chatgpt.com/docs/developer-commands.md (2026-08-21)
//
// The Claude table labels its own rows, and those labels are used verbatim rather
// than judged here. /review and /security-review read like skills and are marked
// commands; /verify and /simplify read like commands and are marked skills. Where
// the vendor's classification and intuition disagree, the vendor wins -- it is the
// one that decides what the name does.
//
// The split matters because the two kinds are not the same event. A control command
// changes a setting; a bundled skill does work. Neither is a plugin, so plugin
// aggregation subtracts both, but only the controls are "not usage".
var builtinCommands = map[string]bool{
	"add-dir": true, "advisor": true, "agents": true, "autocompact": true,
	"autofix-pr": true, "background": true, "bashes": true, "branch": true,
	"btw": true, "bug": true, "cd": true, "chrome": true,
	"clear": true, "color": true, "compact": true, "config": true,
	"context": true, "copy": true, "cost": true, "design-login": true,
	"desktop": true, "diff": true, "effort": true, "exit": true,
	"export": true, "fast": true, "feedback": true, "focus": true,
	"fork": true, "goal": true, "heapdump": true, "help": true,
	"hooks": true, "ide": true, "import": true, "init": true,
	"insights": true, "install-github-app": true, "install-slack-app": true, "keybindings": true,
	"list-agents": true, "login": true, "logout": true, "mcp": true,
	"memory": true, "migrate-installer": true, "mobile": true, "model": true,
	"output-style": true, "passes": true, "permissions": true, "plan": true,
	"plugin": true, "powerup": true, "pr-comments": true, "privacy-settings": true,
	"radio": true, "recap": true, "release-notes": true, "reload-plugins": true,
	"reload-skills": true, "remote-control": true, "remote-env": true, "rename": true,
	"resume": true, "review": true, "rewind": true, "sandbox": true,
	"schedule": true, "scroll-speed": true, "security-review": true, "setup-bedrock": true,
	"setup-vertex": true, "skills": true, "stats": true, "status": true,
	"statusline": true, "stickers": true, "stop": true, "subtask": true,
	"tasks": true, "team-onboarding": true, "teleport": true, "terminal-setup": true,
	"theme": true, "todos": true, "tui": true, "ultraplan": true,
	"ultrareview": true, "upgrade": true, "usage": true, "usage-credits": true,
	"vim": true, "voice": true, "web-setup": true, "workflows": true,
}

// builtinSkills are the rows the Claude table marks [Skill] or [Workflow].
var builtinSkills = map[string]bool{
	"batch": true, "claude-api": true, "code-review": true, "dataviz": true,
	"debug": true, "deep-research": true, "design-sync": true, "doctor": true,
	"fewer-permission-prompts": true, "loop": true, "run": true, "run-skill-generator": true,
	"simplify": true, "verify": true,
}

// builtins is the union, for the question "did this name come from the binary?"
var builtins = unionKeys(builtinCommands, builtinSkills)

// codexBuiltins is every name Codex's reference writes as a slash command.
//
// Taken from the prose as well as the table: the table under "Available slash
// commands" holds 22, while the document uses /clear, /exit, /rename and others
// throughout. Only /undo was dropped -- it appears nowhere as a slash command.
//
// Its CLI subcommands (codex exec, app-server, completion) are absent by
// construction, since they are never written with a leading slash. That asymmetry
// is deliberate: a name listed here that is not really a builtin silently deletes
// real plugin usage, which is the expensive direction to be wrong in.
var codexBuiltins = map[string]bool{
	"agent": true, "app": true, "approve": true, "apps": true,
	"archive": true, "btw": true, "clean": true, "clear": true,
	"cloud": true, "cloud-environment": true, "compact": true, "copy": true,
	"debug-config": true, "delete": true, "diff": true, "exit": true,
	"experimental": true, "fast": true, "feedback": true, "fork": true,
	"goal": true, "hooks": true, "ide": true, "ide-context": true,
	"import": true, "init": true, "keymap": true, "local": true,
	"logout": true, "mcp": true, "memories": true, "mention": true,
	"model": true, "new": true, "permissions": true, "personality": true,
	"pet": true, "pets": true, "plan": true, "plugins": true,
	"project": true, "ps": true, "quit": true, "raw": true,
	"reasoning": true, "rename": true, "resume": true, "review": true,
	"sandbox-add-read-dir": true, "setup-default-sandbox": true, "side": true, "skills": true,
	"status": true, "statusline": true, "stop": true, "subagents": true,
	"theme": true, "title": true, "usage": true, "vim": true,
	"worktree": true,
}

func unionKeys(maps ...map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, m := range maps {
		for k := range m {
			out[k] = true
		}
	}
	return out
}

// AgentBuiltinKeys returns "agent|name" for every builtin of every agent, the shape
// the store filters on.
//
// One flat list rather than a set per agent: the query needs a single ANY(), and
// the pairing is what keeps Codex's /rename from excusing a Claude plugin of the
// same name. An agent with no list here excludes nothing, which is the safe
// default -- collecting a builtin as plugin usage is a wrong number, dropping a
// real plugin is lost data.
func AgentBuiltinKeys() []string {
	return agentKeys(builtins, codexBuiltins)
}

func agentKeys(claude, codex map[string]bool) []string {
	out := make([]string, 0, len(claude)+len(codex))
	for _, name := range sortedKeys(claude) {
		out = append(out, "claude|"+name)
	}
	for _, name := range sortedKeys(codex) {
		out = append(out, "codex|"+name)
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// namespaced splits "plugin:thing" into its trailing name.
func namespaced(name string) (string, bool) {
	i := strings.LastIndex(name, ":")
	if i <= 0 || i == len(name)-1 {
		return "", false
	}
	return name[i+1:], true
}

// Cache builds one Index per project directory and keeps it for the run.
//
// Reading the plugin tree is the expensive part and it does not change while a sync
// runs, so it is shared; only the project half differs between sessions.
type Cache struct {
	claudeDir string
	byProject map[string]*Index
}

// NewCache returns a cache rooted at one Claude home.
func NewCache(claudeDir string) *Cache {
	return &Cache{claudeDir: claudeDir, byProject: map[string]*Index{}}
}

// Classify resolves a command as it would have been seen from projectDir.
func (c *Cache) Classify(projectDir, name string) Class {
	if c == nil || name == "" {
		return Class{SourceUnknown, KindUnknown}
	}
	idx, ok := c.byProject[projectDir]
	if !ok {
		idx = NewIndex(c.claudeDir, projectDir)
		c.byProject[projectDir] = idx
	}
	return idx.Classify(name)
}
