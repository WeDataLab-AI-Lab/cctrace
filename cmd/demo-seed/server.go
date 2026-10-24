package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	"cctrace/internal/store"
)

// api is one dashboard session against the server. Dashboard writes pass the
// server's CSRF check only with X-Requested-With and an Origin equal to the host.
type api struct {
	base   string
	client *http.Client
}

func newAPI(base string) *api {
	jar, _ := cookiejar.New(nil)
	return &api{base: strings.TrimRight(base, "/"), client: &http.Client{Jar: jar, Timeout: 2 * time.Minute}}
}

func (a *api) do(method, path string, body any, bearer string, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, a.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("Origin", a.base)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func randomPassword() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// provision gives every persona a dashboard account bound to its cctrace user id
// and returns each one's upload token, issued the way `cctrace init` gets it.
func provision(server, adminEmail, adminPassword string) (map[string]string, error) {
	admin := newAPI(server)
	if err := admin.do("POST", "/api/auth/login", map[string]string{"email": adminEmail, "password": adminPassword}, "", nil); err != nil {
		return nil, fmt.Errorf("admin login: %w", err)
	}
	var me struct {
		ID    int64  `json:"id"`
		Email string `json:"email"`
	}
	if err := admin.do("GET", "/api/auth/me", nil, "", &me); err != nil {
		return nil, err
	}
	passwords := map[string]string{}
	for _, p := range team {
		if p.Admin {
			if !strings.EqualFold(me.Email, p.Email) {
				return nil, fmt.Errorf("the admin account is %s, want %s", me.Email, p.Email)
			}
			body := map[string]any{"cctrace_user_id": p.UserID, "team": p.Team, "name": p.Name}
			if err := admin.do("PATCH", fmt.Sprintf("/api/admin/users/%d", me.ID), body, "", nil); err != nil {
				return nil, err
			}
			passwords[p.UserID] = adminPassword
			continue
		}
		var created struct {
			TempPassword string `json:"temp_password"`
		}
		body := map[string]any{"email": p.Email, "name": p.Name, "team": p.Team, "role": "user", "cctrace_user_id": p.UserID}
		if err := admin.do("POST", "/api/admin/users", body, "", &created); err != nil {
			return nil, fmt.Errorf("create %s: %w (the seeder needs an empty server)", p.Email, err)
		}
		user := newAPI(server)
		if err := user.do("POST", "/api/auth/login", map[string]string{"email": p.Email, "password": created.TempPassword}, "", nil); err != nil {
			return nil, err
		}
		pw := randomPassword()
		if err := user.do("POST", "/api/auth/change-password", map[string]string{"current_password": created.TempPassword, "new_password": pw}, "", nil); err != nil {
			return nil, err
		}
		passwords[p.UserID] = pw
	}
	tokens := map[string]string{}
	for _, p := range team {
		var out struct {
			APIToken string `json:"api_token"`
		}
		// /api/cli/auth allows one request a second.
		time.Sleep(1100 * time.Millisecond)
		if err := newAPI(server).do("POST", "/api/cli/auth", map[string]string{"user_id": p.UserID, "password": passwords[p.UserID]}, "", &out); err != nil {
			return nil, fmt.Errorf("upload token for %s: %w", p.UserID, err)
		}
		tokens[p.UserID] = out.APIToken
	}
	return tokens, nil
}

func postQuotaSamples(server, token string, samples []*store.QuotaSample) (int, error) {
	inserted := 0
	for i := 0; i < len(samples); i += 5000 {
		end := min(i+5000, len(samples))
		var out struct {
			Inserted int `json:"inserted"`
		}
		if err := newAPI(server).do("POST", "/api/quota-samples", map[string]any{"samples": samples[i:end]}, token, &out); err != nil {
			return inserted, err
		}
		inserted += out.Inserted
	}
	return inserted, nil
}
