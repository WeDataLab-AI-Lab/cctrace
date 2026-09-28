package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type authResponse struct {
	Name               string `json:"name"`
	Email              string `json:"email"`
	Team               string `json:"team"`
	MustChangePassword bool   `json:"must_change_password"`
	ApiToken           string `json:"api_token"`
}

func authenticateUser(syncEndpoint, userID, password, authToken string) (*authResponse, error) {
	u, err := url.Parse(strings.TrimRight(syncEndpoint, "/") + "/api/cli/auth")
	if err != nil {
		return nil, err
	}

	payload := fmt.Sprintf(`{"user_id":%q,"password":%q}`, userID, password)
	req, err := http.NewRequest("POST", u.String(), strings.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("server unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("invalid credentials")
	}
	if resp.StatusCode == http.StatusForbidden {
		var body struct {
			Error string `json:"error"`
		}
		if json.NewDecoder(resp.Body).Decode(&body) == nil && body.Error == "password_change_required" {
			return nil, fmt.Errorf("please change your password on the dashboard before authenticating")
		}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned %d", resp.StatusCode)
	}

	var info authResponse
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}
