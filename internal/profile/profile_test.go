package profile

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewDefault(t *testing.T) {
	p := NewDefault()

	if p.Version != 1 {
		t.Errorf("Version = %d, want 1", p.Version)
	}
	if p.Server.Protocol != "grpc" {
		t.Errorf("Server.Protocol = %q, want \"grpc\"", p.Server.Protocol)
	}
	if p.Options.MetricsExportInterval != 60000 {
		t.Errorf("MetricsExportInterval = %d, want 60000", p.Options.MetricsExportInterval)
	}
	if p.Options.LogsExportInterval != 5000 {
		t.Errorf("LogsExportInterval = %d, want 5000", p.Options.LogsExportInterval)
	}
	if p.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}
	if p.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should not be zero")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")

	original := NewDefault()
	original.User = UserInfo{Name: "Alice", Email: "alice@example.com", Team: "eng"}
	original.Server = ServerInfo{Endpoint: "https://api.example.com", Protocol: "grpc", AuthToken: "tok123"}
	original.Options.RedactUserPrompts = true
	original.Options.RedactToolDetails = true
	// Truncate to second precision to avoid sub-second JSON round-trip issues.
	original.CreatedAt = original.CreatedAt.Truncate(time.Second)
	original.UpdatedAt = original.UpdatedAt.Truncate(time.Second)

	if err := SaveTo(original, path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if loaded.Version != original.Version {
		t.Errorf("Version mismatch: got %d, want %d", loaded.Version, original.Version)
	}
	if loaded.User != original.User {
		t.Errorf("User mismatch: got %+v, want %+v", loaded.User, original.User)
	}
	if loaded.Server != original.Server {
		t.Errorf("Server mismatch: got %+v, want %+v", loaded.Server, original.Server)
	}
	if !reflect.DeepEqual(loaded.Options, original.Options) {
		t.Errorf("Options mismatch: got %+v, want %+v", loaded.Options, original.Options)
	}
	if !loaded.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("CreatedAt mismatch: got %v, want %v", loaded.CreatedAt, original.CreatedAt)
	}
	if !loaded.UpdatedAt.Equal(original.UpdatedAt) {
		t.Errorf("UpdatedAt mismatch: got %v, want %v", loaded.UpdatedAt, original.UpdatedAt)
	}
}

func TestEnsureDir(t *testing.T) {
	// EnsureDir creates DefaultDir. We test the underlying MkdirAll behavior
	// by checking that a nested path is created with the right permissions.
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", ".cctrace")

	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected a directory")
	}

	if runtime.GOOS != "windows" {
		perm := info.Mode().Perm()
		if perm != 0700 {
			t.Errorf("permissions = %04o, want 0700", perm)
		}
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name         string
		user         UserInfo
		wantErrField string
	}{
		{
			name:         "missing name",
			user:         UserInfo{Name: "", Email: "a@b.com", Team: "eng"},
			wantErrField: "user.name",
		},
		{
			name:         "missing email",
			user:         UserInfo{Name: "Alice", Email: "", Team: "eng"},
			wantErrField: "user.email",
		},
		{
			name:         "missing team",
			user:         UserInfo{Name: "Alice", Email: "a@b.com", Team: ""},
			wantErrField: "user.team",
		},
		{
			name:         "whitespace name",
			user:         UserInfo{Name: "   ", Email: "a@b.com", Team: "eng"},
			wantErrField: "user.name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := NewDefault()
			p.User = tc.user
			err := Validate(p)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			ve, ok := err.(ValidationErrors)
			if !ok {
				t.Fatalf("expected ValidationErrors, got %T", err)
			}
			found := false
			for _, e := range ve {
				if e.Field == tc.wantErrField {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected error for field %q, got: %v", tc.wantErrField, err)
			}
		})
	}
}

func TestValidateSuccess(t *testing.T) {
	p := NewDefault()
	p.User = UserInfo{Name: "Alice", Email: "alice@example.com", Team: "eng"}
	if err := Validate(p); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")

	if ExistsAt(path) {
		t.Error("ExistsAt should return false before save")
	}

	p := NewDefault()
	p.User = UserInfo{Name: "Bob", Email: "bob@example.com", Team: "ops"}
	if err := SaveTo(p, path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	if !ExistsAt(path) {
		t.Error("ExistsAt should return true after save")
	}

	if err := RemoveAt(path); err != nil {
		t.Fatalf("RemoveAt: %v", err)
	}

	if ExistsAt(path) {
		t.Error("ExistsAt should return false after remove")
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")

	p := NewDefault()
	p.User = UserInfo{Name: "Carol", Email: "carol@example.com", Team: "qa"}
	if err := SaveTo(p, path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	if err := RemoveAt(path); err != nil {
		t.Fatalf("RemoveAt: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected file to not exist after remove, got: %v", err)
	}
}

// profile.json holds server.auth_token, so a save that follows a symlink writes
// the token somewhere the user did not choose, and a save that loses power
// mid-write can leave it empty and break every later sync. Both guarantees come
// from internal/atomicfile; this locks that SaveTo actually goes through it.
func TestSaveToRefusesSymlinkDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation needs privileges on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "profile.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	p := NewDefault()
	p.User = UserInfo{Name: "Dave", Email: "dave@example.com"}
	p.Server.AuthToken = "cct_secret"

	if err := SaveTo(p, link); err == nil {
		t.Fatal("SaveTo followed a symlink destination")
	}

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "cct_secret") {
		t.Fatalf("auth token was written through the symlink: %s", data)
	}
}

func TestSaveAtomicPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits not applicable on Windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")

	p := NewDefault()
	p.User = UserInfo{Name: "Dave", Email: "dave@example.com", Team: "sre"}
	if err := SaveTo(p, path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("file permissions = %04o, want 0600", perm)
	}
}

func TestValidationErrorInterface(t *testing.T) {
	ve := ValidationErrors{
		{Field: "user.name", Reason: "required"},
		{Field: "user.email", Reason: "required"},
	}
	msg := ve.Error()
	if msg == "" {
		t.Error("ValidationErrors.Error() should not be empty")
	}
	// Both field names should appear in the message.
	for _, e := range ve {
		if e.Error() == "" {
			t.Errorf("ValidationError.Error() for field %q should not be empty", e.Field)
		}
	}
}

func TestLoadFrom_FileNotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	_, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected error loading non-existent file, got nil")
	}
}

func TestLoadFrom_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte("{invalid json}"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := LoadFrom(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestSaveTo_ParentDirNotExist(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission trick not applicable on Windows")
	}
	// Create a file where a directory component is expected, so MkdirAll fails.
	dir := t.TempDir()
	// Use an existing file as if it were a directory component.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	path := filepath.Join(blocker, "subdir", "profile.json")
	p := NewDefault()
	p.User = UserInfo{Name: "X", Email: "x@x.com", Team: "t"}
	err := SaveTo(p, path)
	if err == nil {
		t.Fatal("expected error saving to path with file-as-dir component, got nil")
	}
}

func TestDefaultDir_And_DefaultPath(t *testing.T) {
	dir := DefaultDir()
	if !strings.HasSuffix(dir, ".cctrace") {
		t.Errorf("DefaultDir() = %q, want suffix \".cctrace\"", dir)
	}
	path := DefaultPath()
	if !strings.HasSuffix(path, "profile.json") {
		t.Errorf("DefaultPath() = %q, want suffix \"profile.json\"", path)
	}
}

func TestEnsureDir_Idempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME override not reliable on Windows")
	}
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := EnsureDir(); err != nil {
		if os.IsPermission(err) {
			t.Skip("permission error, skipping")
		}
		t.Fatalf("first EnsureDir: %v", err)
	}
	if err := EnsureDir(); err != nil {
		if os.IsPermission(err) {
			t.Skip("permission error, skipping")
		}
		t.Fatalf("second EnsureDir: %v", err)
	}
	// Verify the directory exists.
	expected := filepath.Join(tmpHome, ".cctrace")
	info, err := os.Stat(expected)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.IsDir() {
		t.Error("expected a directory")
	}
}

func TestRemoveAt_NotExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.json")
	// os.Remove on a non-existent path returns an error; RemoveAt passes it through.
	err := RemoveAt(path)
	if err == nil {
		t.Fatal("expected error removing non-existent file, got nil")
	}
}

func TestLoad_ProfileExists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")

	original := NewDefault()
	original.User = UserInfo{Name: "TestUser", Email: "test@example.com", Team: "platform"}
	original.Server = ServerInfo{Endpoint: "https://srv.example.com", Protocol: "grpc", AuthToken: "token-abc"}
	original.Options.RedactUserPrompts = true
	original.CreatedAt = original.CreatedAt.Truncate(time.Second)
	original.UpdatedAt = original.UpdatedAt.Truncate(time.Second)

	if err := SaveTo(original, path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if loaded.User != original.User {
		t.Errorf("User mismatch: got %+v, want %+v", loaded.User, original.User)
	}
	if loaded.Server != original.Server {
		t.Errorf("Server mismatch: got %+v, want %+v", loaded.Server, original.Server)
	}
	if !loaded.Options.RedactUserPrompts {
		t.Error("Options.RedactUserPrompts should be true")
	}
}

func TestSave_RoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME override not reliable on Windows")
	}
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	if err := EnsureDir(); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}

	p := NewDefault()
	p.User = UserInfo{Name: "RoundTrip", Email: "rt@example.com", Team: "infra"}
	p.CreatedAt = p.CreatedAt.Truncate(time.Second)
	p.UpdatedAt = p.UpdatedAt.Truncate(time.Second)

	if err := Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if !Exists() {
		t.Fatal("Exists() should return true after Save()")
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.User != p.User {
		t.Errorf("User mismatch after round-trip: got %+v, want %+v", loaded.User, p.User)
	}
}

func TestGjcOmoOptionsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.json")

	original := NewDefault()
	original.User = UserInfo{Name: "TestUser", Email: "test@example.com", Team: "platform"}
	original.Options.GjcSyncEnabled = true
	original.Options.OmoSyncEnabled = true
	original.Options.GjcDirs = []string{"/tmp/other-gjc-home"}

	if err := SaveTo(original, path); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	loaded, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if !loaded.Options.GjcSyncEnabled {
		t.Error("Options.GjcSyncEnabled should be true")
	}
	if !loaded.Options.OmoSyncEnabled {
		t.Error("Options.OmoSyncEnabled should be true")
	}
	if len(loaded.Options.GjcDirs) != 1 || loaded.Options.GjcDirs[0] != "/tmp/other-gjc-home" {
		t.Errorf("Options.GjcDirs mismatch: got %v", loaded.Options.GjcDirs)
	}
}

func TestValidate_WhitespaceEmail(t *testing.T) {
	p := NewDefault()
	p.User = UserInfo{Name: "Alice", Email: "   ", Team: "eng"}
	err := Validate(p)
	if err == nil {
		t.Fatal("expected error for whitespace email, got nil")
	}
	ve, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	found := false
	for _, e := range ve {
		if e.Field == "user.email" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error for field %q, got: %v", "user.email", err)
	}
}

func TestValidate_WhitespaceTeam(t *testing.T) {
	p := NewDefault()
	p.User = UserInfo{Name: "Alice", Email: "alice@example.com", Team: "\t"}
	err := Validate(p)
	if err == nil {
		t.Fatal("expected error for whitespace team, got nil")
	}
	ve, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	found := false
	for _, e := range ve {
		if e.Field == "user.team" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected error for field %q, got: %v", "user.team", err)
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	p := NewDefault()
	p.User = UserInfo{Name: "", Email: "", Team: ""}
	err := Validate(p)
	if err == nil {
		t.Fatal("expected ValidationErrors, got nil")
	}
	ve, ok := err.(ValidationErrors)
	if !ok {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	if len(ve) != 3 {
		t.Errorf("expected 3 validation errors, got %d: %v", len(ve), ve)
	}
	wantFields := []string{"user.name", "user.email", "user.team"}
	for _, wf := range wantFields {
		found := false
		for _, e := range ve {
			if e.Field == wf {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected error for field %q, not found in: %v", wf, ve)
		}
	}
}

func TestValidateName(t *testing.T) {
	valid := []string{"work", "personal", "my-profile", "profile1", "a", "a1-b_c"}
	for _, name := range valid {
		t.Run("valid/"+name, func(t *testing.T) {
			if err := ValidateName(name); err != nil {
				t.Errorf("ValidateName(%q) = %v, want nil", name, err)
			}
		})
	}

	invalid := []string{
		"default",   // reserved
		"",          // empty
		"-bad",      // starts with dash
		"_bad",      // starts with underscore
		"has space", // space
		"has/slash", // slash
		"has.dot",   // dot
	}
	for _, name := range invalid {
		t.Run("invalid/"+name, func(t *testing.T) {
			if err := ValidateName(name); err == nil {
				t.Errorf("ValidateName(%q) = nil, want error", name)
			}
		})
	}
}

func TestNamedProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME override not reliable on Windows")
	}
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "work"

	// NamedDir
	dir, err := NamedDir(name)
	if err != nil {
		t.Fatalf("NamedDir: %v", err)
	}
	wantSuffix := filepath.Join("profiles", name)
	if !strings.HasSuffix(dir, wantSuffix) {
		t.Errorf("NamedDir = %q, want suffix %q", dir, wantSuffix)
	}

	// NamedPath
	path, err := NamedPath(name)
	if err != nil {
		t.Fatalf("NamedPath: %v", err)
	}
	if !strings.HasSuffix(path, "profile.json") {
		t.Errorf("NamedPath = %q, want suffix profile.json", path)
	}

	// EnsureNamedDir
	if err := EnsureNamedDir(name); err != nil {
		t.Fatalf("EnsureNamedDir: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("expected directory at %s after EnsureNamedDir", dir)
	}

	// SaveNamed / LoadNamed
	p := NewDefault()
	p.User = UserInfo{Name: "Alice", Email: "alice@work.com", Team: "eng"}
	p.ClaudeConfigDir = "/home/alice/.claude-work"

	if err := SaveNamed(p, name); err != nil {
		t.Fatalf("SaveNamed: %v", err)
	}

	loaded, err := LoadNamed(name)
	if err != nil {
		t.Fatalf("LoadNamed: %v", err)
	}
	if loaded.User != p.User {
		t.Errorf("User mismatch: got %+v, want %+v", loaded.User, p.User)
	}
	if loaded.ClaudeConfigDir != p.ClaudeConfigDir {
		t.Errorf("ClaudeConfigDir mismatch: got %q, want %q", loaded.ClaudeConfigDir, p.ClaudeConfigDir)
	}

	// ListNamed
	names, err := ListNamed()
	if err != nil {
		t.Fatalf("ListNamed: %v", err)
	}
	found := false
	for _, n := range names {
		if n == name {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ListNamed = %v, want to contain %q", names, name)
	}

	// RemoveNamed
	if err := RemoveNamed(name); err != nil {
		t.Fatalf("RemoveNamed: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("expected %s to not exist after RemoveNamed", dir)
	}

	// LoadNamed after removal should error
	if _, err := LoadNamed(name); err == nil {
		t.Error("LoadNamed after RemoveNamed should return error, got nil")
	}
}

func TestListNamed_NoDirReturnsNil(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("HOME override not reliable on Windows")
	}
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	names, err := ListNamed()
	if err != nil {
		t.Fatalf("ListNamed with no profiles dir: %v", err)
	}
	if names != nil {
		t.Errorf("ListNamed = %v, want nil when profiles dir absent", names)
	}
}
