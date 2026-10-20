package profile

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"cctrace/internal/atomicfile"
)

// UpdateExcludedAccounts changes only the raw session-upload exclusion field.
// It shares the definition lock and revision fence with every CLI profile writer.
func (r Registry) UpdateExcludedAccounts(name, expected string, entries []string) (updated Entry, err error) {
	err = withProfileLock(r.Root, func() error {
		path, e := r.Path(name)
		if e != nil {
			return e
		}
		if e = r.safePath(path); e != nil {
			return e
		}
		data, e := readExclusionDefinition(path)
		if e != nil {
			return e
		}
		if expected == "" || revision(data) != expected {
			return ErrConflict
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) != nil {
			return ErrUnsafe
		}
		var options map[string]json.RawMessage
		if json.Unmarshal(raw["options"], &options) != nil || options == nil {
			return ErrUnsafe
		}
		var previous []string
		if value, ok := options["exclude_accounts"]; ok {
			if json.Unmarshal(value, &previous) != nil {
				return ErrUnsafe
			}
		}
		for _, value := range entries {
			unchanged := false
			for _, old := range previous {
				if value == old {
					unchanged = true
					break
				}
			}
			if !unchanged && (value == "" || len(value) > 256 || strings.ContainsAny(value, "@/\\\n\r") || strings.TrimSpace(value) != value) {
				return ErrUnsafe
			}
		}
		encoded, e := json.Marshal(entries)
		if e != nil {
			return e
		}
		options["exclude_accounts"] = encoded
		raw["options"], e = json.Marshal(options)
		if e != nil {
			return e
		}
		after, e := json.MarshalIndent(raw, "", "  ")
		if e != nil {
			return e
		}
		// Reject replacement by a non-cooperating writer before the atomic rename.
		again, e := readExclusionDefinition(path)
		if e != nil || revision(again) != expected {
			return ErrConflict
		}
		if e = atomicfile.Write(path, after, 0600); e != nil {
			return e
		}
		var p Profile
		if e = json.Unmarshal(after, &p); e != nil {
			return e
		}
		complete := p.Options.LocalAnalysisEnabled || ((p.User.ID != "" || p.User.Email != "") && (p.Server.Endpoint != "" || p.Server.SyncEndpoint != ""))
		state := "disabled"
		if p.RegistrationOnly || !complete {
			state = "registered"
		} else if p.CollectionEnabled("claude") || p.CollectionEnabled("codex") || p.Options.GjcSyncEnabled || p.Options.OmoSyncEnabled || p.Options.LocalAnalysisEnabled {
			state = "enabled"
		}
		updated = Entry{ID: ProfileID(name), Name: name, Revision: revision(after), CollectionState: state, ConfigurationComplete: complete, RuntimeDir: filepath.Dir(path), Profile: &p}
		return nil
	})
	return
}

func readExclusionDefinition(path string) ([]byte, error) {
	const limit = 4 << 20
	before, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm() != 0600 || before.Size() > limit {
		return nil, ErrUnsafe
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !os.SameFile(before, opened) {
		return nil, ErrConflict
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, e
	}
	if len(b) > limit {
		return nil, ErrUnsafe
	}
	after, e := os.Lstat(path)
	if e != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() || !opened.ModTime().Equal(after.ModTime()) {
		return nil, ErrConflict
	}
	return b, nil
}
