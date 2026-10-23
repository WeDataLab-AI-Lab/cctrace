package localapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"cctrace/internal/codexlog"
	"cctrace/internal/sessionlog"
)

type Server struct {
	opts        Options
	roots       []root
	origins     map[string]bool
	fingerprint string
	now         func() time.Time
}
type root struct{ path, agent string }
type fileEntry struct {
	path, root, agent, id, revision string
	info                            os.FileInfo
}

func validAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback() && (ip.To4() != nil || ip.Equal(net.IPv6loopback))
}

func New(opts Options) (*Server, error) {
	if !validAddress(opts.Address) {
		return nil, errors.New("address must be a literal loopback IP and port")
	}
	if opts.TokenHash != "" {
		hash, err := hex.DecodeString(opts.TokenHash)
		if err != nil || len(hash) != sha256.Size {
			return nil, errors.New("token hash must encode a SHA-256 digest")
		}
	} else {
		if len(opts.Token) < 32 {
			return nil, errors.New("token must contain at least 32 characters")
		}
		raw, err := base64.RawURLEncoding.DecodeString(opts.Token)
		if err != nil || len(raw) != 32 {
			return nil, errors.New("token must encode 32 random bytes")
		}
	}
	if len(opts.AllowedOrigins) == 0 {
		return nil, errors.New("at least one origin required")
	}
	s := &Server{opts: opts, origins: map[string]bool{}, now: time.Now}
	if opts.SelectionPath != "" && !filepath.IsAbs(opts.SelectionPath) {
		return nil, errors.New("profile selection path must be absolute")
	}
	profileNames := make(map[string]bool, len(opts.Profiles))
	for _, entry := range opts.Profiles {
		key := entry.Agent + ":" + entry.ProfileName
		if !validLocalProfileIdentity(entry) || profileNames[key] {
			return nil, errors.New("invalid local profile catalog")
		}
		profileNames[key] = true
	}
	for _, origin := range opts.AllowedOrigins {
		u, e := url.Parse(origin)
		if e != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.String() != origin || origin == "null" || strings.Contains(u.Host, "*") || strings.HasSuffix(origin, "?") || strings.HasSuffix(origin, "#") {
			return nil, errors.New("invalid origin")
		}
		s.origins[origin] = true
	}
	roots, err := resolveRoots(opts.ClaudeDirs, opts.CodexDirs)
	if err != nil {
		return nil, err
	}
	s.roots = roots
	keys := make([]string, 0, len(s.roots))
	for _, r := range s.roots {
		keys = append(keys, r.agent+":"+r.path)
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\x00")))
	s.fingerprint = hex.EncodeToString(sum[:16])
	if opts.ProfileFingerprint != "" {
		s.fingerprint = opts.ProfileFingerprint
	}
	return s, nil
}

// resolveRoots resolves each configured home once; a missing home is skipped.
func resolveRoots(claudeDirs, codexDirs []string) ([]root, error) {
	var roots []root
	seen := map[string]bool{}
	for _, group := range []struct {
		agent string
		dirs  []string
	}{{"claude", claudeDirs}, {"codex", codexDirs}} {
		for _, dir := range group.dirs {
			abs, e := filepath.Abs(codexlog.ExpandHome(dir))
			if e != nil {
				return nil, e
			}
			abs, e = filepath.EvalSymlinks(abs)
			if errors.Is(e, os.ErrNotExist) {
				continue
			}
			if e != nil {
				return nil, e
			}
			fi, e := os.Stat(abs)
			if e != nil || !fi.IsDir() {
				return nil, fmt.Errorf("invalid local session directory")
			}
			key := group.agent + ":" + abs
			if !seen[key] {
				roots = append(roots, root{abs, group.agent})
				seen[key] = true
			}
		}
	}
	return roots, nil
}
func (s *Server) Fingerprint() string   { return s.fingerprint }
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.handle) }
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if listener == nil || listener.Addr().Network() != "tcp" || listener.Addr().String() != s.opts.Address || !validAddress(listener.Addr().String()) {
		return errors.New("listener must match configured loopback address")
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Shutdown(context.Background())
		case <-done:
		}
	}()
	err := server.Serve(listener)
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func fail(w http.ResponseWriter, status int, code, msg string) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
func send(w http.ResponseWriter, v any) { _ = json.NewEncoder(w).Encode(v) }
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	if r.Host != s.opts.Address {
		fail(w, 403, "forbidden", "invalid host")
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" && !s.origins[origin] {
		fail(w, 403, "forbidden", "invalid origin")
		return
	}
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	if r.Method == http.MethodOptions {
		requested := r.Header.Get("Access-Control-Request-Method")
		if requested != "GET" && (requested != "POST" || r.URL.Path != "/v1/profiles/select") {
			fail(w, 403, "forbidden", "invalid requested method")
			return
		}
		for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
			h := strings.ToLower(strings.TrimSpace(header))
			if h != "" && h != "authorization" && h != "content-type" {
				fail(w, 403, "forbidden", "invalid requested header")
				return
			}
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		if origin != "" && r.Header.Get("Access-Control-Request-Private-Network") == "true" {
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}
		w.WriteHeader(204)
		return
	}
	if r.Method == http.MethodPost && r.URL.Path != "/v1/profiles/select" {
		fail(w, 405, "method_not_allowed", "method not allowed")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		fail(w, 405, "method_not_allowed", "method not allowed")
		return
	}
	if r.Method == http.MethodPost && (origin == "" || !s.origins[origin]) {
		fail(w, 403, "forbidden", "exact origin required")
		return
	}
	auth := r.Header.Get("Authorization")
	expected := "Bearer " + s.opts.Token
	authorized := len(auth) == len(expected) && subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) == 1
	if s.opts.TokenHash != "" {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(auth, "Bearer "))
		digest := sha256.Sum256(raw)
		stored, _ := hex.DecodeString(s.opts.TokenHash)
		authorized = strings.HasPrefix(auth, "Bearer ") && err == nil && len(raw) == 32 && subtle.ConstantTimeCompare(digest[:], stored) == 1
	}
	if !authorized {
		fail(w, 401, "unauthorized", "invalid bearer token")
		return
	}
	switch {
	case r.URL.Path == "/v1/health":
		send(w, map[string]any{"status": "ok", "api_version": 1, "profile_fingerprint": s.fingerprint})
	case r.URL.Path == "/v1/profiles" && r.Method == http.MethodGet:
		s.profileCatalog(w)
	case r.URL.Path == "/v1/profiles/select" && r.Method == http.MethodPost:
		s.selectLocalProfile(w, r)
	case r.URL.Path == "/v1/sessions":
		s.list(w, r)
	case strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
		s.detail(w, r)
	default:
		fail(w, 404, "not_found", "not found")
	}
}
func hash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}
func (s *Server) files() ([]fileEntry, error) { return findEntries(s.roots) }
func findEntries(roots []root) ([]fileEntry, error) {
	var entries []fileEntry
	seenFiles := map[string]bool{}
	for _, root := range roots {
		files, err := findFiles(root.path, root.agent)
		if err != nil {
			return nil, err
		}
		for _, path := range files {
			real, e := filepath.EvalSymlinks(path)
			if e != nil {
				continue
			}
			rel, e := filepath.Rel(root.path, real)
			if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
				continue
			}
			fi, e := os.Stat(real)
			if e != nil || !fi.Mode().IsRegular() {
				continue
			}
			key := root.agent + ":" + real
			if seenFiles[key] {
				continue
			}
			id := hash(root.agent, root.path, rel)
			rev, e := revision(real, fi)
			if e != nil {
				continue
			}
			seenFiles[key] = true
			entries = append(entries, fileEntry{real, root.path, root.agent, id, rev, fi})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].info.ModTime().Equal(entries[j].info.ModTime()) {
			return entries[i].id < entries[j].id
		}
		return entries[i].info.ModTime().After(entries[j].info.ModTime())
	})
	return entries, nil
}
func revision(path string, fi os.FileInfo) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%d:%d:", fi.Size(), fi.ModTime().UnixNano())
	b := make([]byte, 4096)
	n, err := f.Read(b)
	if err != nil && err != io.EOF {
		return "", err
	}
	_, _ = h.Write(b[:n])
	if fi.Size() > 4096 {
		_, err = f.Seek(max(0, fi.Size()-4096), io.SeekStart)
		if err != nil {
			return "", err
		}
		n, err = f.Read(b)
		if err != nil && err != io.EOF {
			return "", err
		}
		_, _ = h.Write(b[:n])
	}
	return hex.EncodeToString(h.Sum(nil)[:16]), nil
}
func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	agent := r.URL.Query().Get("agent")
	if agent != "" && agent != "claude" && agent != "codex" {
		fail(w, 400, "invalid", "invalid agent")
		return
	}
	limit := 100
	if str := r.URL.Query().Get("limit"); str != "" {
		n, e := strconv.Atoi(str)
		if e != nil || n < 1 || n > 500 {
			fail(w, 400, "invalid", "invalid limit")
			return
		}
		limit = n
	}
	entries, err := s.files()
	if err != nil {
		fail(w, 500, "read_error", "session discovery failed")
		return
	}
	filtered := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		if agent == "" || e.agent == agent {
			filtered = append(filtered, e)
		}
	}
	parts := make([]string, 0, len(filtered))
	for _, e := range filtered {
		parts = append(parts, e.id+":"+e.revision)
	}
	rev := hash(parts...)
	index := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		var value struct {
			Revision string `json:"revision"`
			Index    int    `json:"index"`
			ID       string `json:"id"`
		}
		b, e := base64.RawURLEncoding.DecodeString(c)
		if e != nil || json.Unmarshal(b, &value) != nil || value.Index < 0 || value.Index > len(filtered) {
			fail(w, 400, "invalid", "invalid cursor")
			return
		}
		if value.Revision != rev || value.Index > 0 && filtered[value.Index-1].id != value.ID {
			fail(w, 409, "revision_changed", "session listing changed")
			return
		}
		index = value.Index
	}
	result := ListResponse{Sessions: []Session{}, Revision: rev}
	end := min(len(filtered), index+limit)
	for _, e := range filtered[index:end] {
		result.Sessions = append(result.Sessions, s.session(e))
	}
	if end < len(filtered) {
		b, _ := json.Marshal(struct {
			Revision string `json:"revision"`
			Index    int    `json:"index"`
			ID       string `json:"id"`
		}{rev, end, filtered[end-1].id})
		next := base64.RawURLEncoding.EncodeToString(b)
		result.NextCursor = &next
	}
	send(w, result)
}
func (s *Server) session(e fileEntry) Session {
	native := nullable(observedNativeID(e))
	selection := e.id
	if native != nil {
		selection = *native
	}
	project := ""
	if e.agent == "claude" {
		project = sessionlog.ProjectHash(e.root, e.path)
	}
	projectName, entrypoint, model, title, start, end := metadata(e)
	if e.info.Size() > 32768 {
		start, end = nil, nil
	}
	return Session{LocalID: e.id, CanonicalID: e.id, NativeSessionID: native, SessionID: selection, Agent: e.agent, ProfileFingerprint: s.fingerprint, Revision: e.revision, ProjectHash: project, ProjectName: projectName, Entrypoint: entrypoint, Model: model, Title: title, StartTime: start, EndTime: end}
}
func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	part := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
	parts := strings.Split(part, "/")
	if len(parts) != 2 || (parts[1] != "records" && parts[1] != "analysis") || len(parts[0]) != 32 {
		fail(w, 404, "not_found", "session not found")
		return
	}
	entries, err := s.files()
	if err != nil {
		fail(w, 500, "read_error", "session discovery failed")
		return
	}
	for _, e := range entries {
		if e.id == parts[0] {
			s.readDetail(w, r, e, parts[1])
			return
		}
	}
	fail(w, 404, "not_found", "session not found")
}
func fileIdentity(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}
func stable(e fileEntry) error {
	fi, err := os.Stat(e.path)
	if err != nil {
		return err
	}
	if !fileIdentity(e.info, fi) {
		return syscall.ESTALE
	}
	rev, err := revision(e.path, fi)
	if err != nil {
		return err
	}
	if rev != e.revision {
		return syscall.ESTALE
	}
	return nil
}
