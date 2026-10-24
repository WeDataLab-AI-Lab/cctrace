package main

import (
	"path/filepath"
	"strings"
	"time"
)

// plugin is a fictitious Claude Code plugin the team installs from its own
// marketplace. Its commands and skills are written into every ~/.claude/plugins,
// so the client classifies a use of one as plugin usage, which is what the
// Plugins & Skills page counts.
type plugin struct {
	Name     string
	Commands map[string]string // command -> the prompt it expands to
	Skills   map[string]string // skill -> its description
}

const marketplace = "nebula-tools"

var plugins = []plugin{
	{Name: "review-kit",
		Commands: map[string]string{"review": "Review the current changes against the team checklist and report problems by severity."},
		Skills:   map[string]string{"pr-checklist": "Checks a change against the team's pull request checklist before it is pushed."}},
	{Name: "test-forge",
		Commands: map[string]string{"coverage": "Write tests for the untested branches of the given file and run them."},
		Skills:   map[string]string{"flaky-hunter": "Reproduces a failing test, isolates its cause and confirms the fix."}},
	{Name: "release-desk",
		Commands: map[string]string{"notes": "Draft release notes for the changes since the last tag."}},
	{Name: "doc-smith",
		Skills: map[string]string{"doc-style": "Applies the docs style guide: one idea per paragraph, runnable examples."}},
}

// pluginFor is what a task of each kind reaches for: a command the person types,
// or a skill. Claude Code applies a skill on its own; Codex has no plugin
// commands and gets a skill when the person names it, without the plugin prefix.
var pluginFor = map[string]struct{ command, skill string }{
	"review":        {command: "review-kit:review"},
	"testing":       {command: "test-forge:coverage"},
	"drafting":      {command: "release-desk:notes"},
	"debugging":     {skill: "test-forge:flaky-hunter"},
	"documentation": {skill: "doc-smith:doc-style"},
	"operations":    {skill: "review-kit:pr-checklist"},
}

// usePlugins lays plugin use over a finished session. It draws from a stream of
// its own, so the sessions, their timing and their tokens are what they would be
// without it.
func usePlugins(s *session) {
	r := sessionRand(s.ID, 2)
	for _, tr := range s.Turns {
		use, ok := pluginFor[tr.Task.Kind]
		if !ok || r.Float64() >= s.P.PluginUse {
			continue
		}
		switch {
		case s.Agent == "codex":
			if use.skill != "" {
				tr.Skill = baseName(use.skill)
			}
		case use.command != "":
			tr.Command = use.command
		case len(tr.Requests) > 1:
			// The first response loads the skill; the ones after it run under it.
			req := tr.Requests[0]
			tr.Skill = use.skill
			req.Actions = append([]action{{Kind: "skill", File: use.skill}}, req.Actions...)
			req.ToolTook = append([]time.Duration{between(r, 40*time.Millisecond, 160*time.Millisecond)}, req.ToolTook...)
			req.ToolIDs = append([]string{"toolu_01" + token(r, 22)}, req.ToolIDs...)
			req.Approval = append([]string{""}, req.Approval...)
		}
	}
}

func baseName(name string) string { return name[strings.LastIndex(name, ":")+1:] }

func pluginOf(name string) plugin {
	base := baseName(name)
	for _, p := range plugins {
		if _, ok := p.Commands[base]; ok {
			return p
		}
		if _, ok := p.Skills[base]; ok {
			return p
		}
	}
	panic("unknown plugin command or skill " + name)
}

func pluginDir(claudeDir string, p plugin) string {
	return filepath.Join(claudeDir, "plugins", "marketplaces", marketplace, "plugins", p.Name)
}

func skillFile(name string) string {
	desc := pluginOf(name).Skills[baseName(name)]
	return "---\nname: " + baseName(name) + "\ndescription: " + desc + "\n---\n\n# " + baseName(name) + "\n\n" + desc + "\n"
}

// commandRecord is the line Claude Code writes for a typed slash command, and
// commandExpansion the hidden line after it carrying the prompt it expands to.
func commandRecord(name, args string) string {
	return "<command-message>" + name + "</command-message>\n<command-name>/" + name + "</command-name>\n<command-args>" + args + "</command-args>"
}

func commandExpansion(name, args string) string {
	return pluginOf(name).Commands[baseName(name)] + "\n\nARGUMENTS: " + args
}

// skillLoaded is the hidden line Claude Code adds after the Skill tool loads one.
func skillLoaded(home, name string) string {
	dir := filepath.Join(pluginDir(filepath.Join(home, ".claude"), pluginOf(name)), "skills", baseName(name))
	return "Base directory for this skill: " + dir + "\n\n" + skillFile(name)
}

// skillInjection is the message Codex adds to a turn whose prompt names a skill.
func skillInjection(home, name string) string {
	path := filepath.Join(home, ".codex", "skills", name, "SKILL.md")
	return "<skill>\n<name>" + name + "</name>\n<path>" + path + "</path>\n" + skillFile(name) + "</skill>"
}

// writePlugins installs the team's plugins under a home: the marketplace copy
// Claude Code reads, and the skills again under ~/.codex/skills for Codex.
func writePlugins(home string) error {
	for _, p := range plugins {
		dir := pluginDir(filepath.Join(home, ".claude"), p)
		for name, prompt := range p.Commands {
			if err := writeFile(filepath.Join(dir, "commands", name+".md"), []byte(prompt+"\n\n$ARGUMENTS\n")); err != nil {
				return err
			}
		}
		for name := range p.Skills {
			body := []byte(skillFile(p.Name + ":" + name))
			if err := writeFile(filepath.Join(dir, "skills", name, "SKILL.md"), body); err != nil {
				return err
			}
			if err := writeFile(filepath.Join(home, ".codex", "skills", name, "SKILL.md"), body); err != nil {
				return err
			}
		}
	}
	return nil
}
