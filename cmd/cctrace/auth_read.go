package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"cctrace/internal/profile"

	"github.com/spf13/cobra"
)

// errAdminReadToken marks the server's refusal to issue a CLI read token to an
// administrator, whose token would read every user's data from a plaintext file.
var errAdminReadToken = errors.New("administrators cannot create a read token from the CLI; create one in the dashboard under Settings > API Access Tokens and set it with 'cctrace config set server.read_token'")

// errUploadTokenCannotRead is what a read command reports when the Open API
// refuses the upload token init stores.
var errUploadTokenCannotRead = errors.New("this profile has no read token, and its upload token cannot read the API; run 'cctrace auth read' to create one")

// uploadTokenCannotRead keeps the server's own sentence beside the CLI's fix.
func uploadTokenCannotRead(serverMessage string) error {
	if serverMessage == "" {
		return errUploadTokenCannotRead
	}
	return fmt.Errorf("%w (server: %s)", errUploadTokenCannotRead, serverMessage)
}

// authCmd groups credential commands.
func authCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage credentials for reading the Open API",
	}
	cmd.AddCommand(authReadCmd())
	return cmd
}

// authReadCmd issues a read token for analysis commands (ls, usage, insights, ...).
// The upload token `cctrace init` stores is refused by the read API on purpose, so
// reading takes its own token, issued only when someone asks for it here.
func authReadCmd() *cobra.Command {
	var profileName string
	cmd := &cobra.Command{
		Use:          "read",
		Short:        "Create a read token for ls, usage, insights, and other Open API commands",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if profileName == "" {
				profileName = os.Getenv("CCTRACE_PROFILE")
			}
			p, err := loadProfile(profileName)
			if err != nil {
				return fmt.Errorf("load profile: %w", err)
			}
			if profileHTTPAPIEndpoint(p) == "" || p.User.ID == "" {
				return fmt.Errorf("the profile has no server or user ID; run 'cctrace init' first")
			}
			password := newInputReader().PromptPassword(fmt.Sprintf("  Password for %s", p.User.ID))
			if password == "" {
				return fmt.Errorf("password is required")
			}
			oldToken := p.Server.ReadToken
			name, err := issueReadToken(p, password, hostname())
			if err != nil {
				return err
			}
			if err := saveProfile(p, profileName); err != nil {
				return fmt.Errorf("save profile: %w", err)
			}
			fmt.Printf("  [OK] Read token %q saved to server.read_token\n", name)
			updated, failed := propagateReadToken(p, profileName, oldToken)
			reportPropagation(os.Stdout, updated, failed)
			fmt.Println("       Revoke it any time under Settings > API Access Tokens.")
			return nil
		},
	}
	cmd.Flags().StringVar(&profileName, "profile", "", "Named profile to use (overrides CCTRACE_PROFILE env var)")
	return cmd
}

// issueReadToken exchanges the user's password for a read token and stores it on
// p. It does not save the profile.
func issueReadToken(p *profile.Profile, password, device string) (string, error) {
	payload, err := json.Marshal(map[string]string{
		"user_id": p.User.ID, "password": password, "device": device,
		// The server replaces exactly the read token this profile holds.
		"replace": p.Server.ReadToken,
	})
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(profileHTTPAPIEndpoint(p)+"/api/cli/read-token", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return "", fmt.Errorf("server unreachable: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		APIToken string `json:"api_token"`
		Name     string `json:"name"`
		Error    string `json:"error"`
		Code     string `json:"code"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	switch {
	case resp.StatusCode == http.StatusOK && body.APIToken != "":
		p.Server.ReadToken = body.APIToken
		return body.Name, nil
	case body.Code == "admin_read_token_forbidden":
		return "", errAdminReadToken
	case body.Error == "password_change_required":
		return "", fmt.Errorf("please change your password on the dashboard before creating a read token")
	case resp.StatusCode == http.StatusUnauthorized:
		return "", fmt.Errorf("invalid credentials")
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		return "", fmt.Errorf("the server does not support creating read tokens from the CLI yet; create one under Settings > API Access Tokens and set it with 'cctrace config set server.read_token'")
	default:
		return "", fmt.Errorf("server returned %d", resp.StatusCode)
	}
}

// offerReadToken asks, during init, whether to also create a read token with the
// password just entered. A refusal or failure only prints a note: init has
// already succeeded at what it is for, and `cctrace auth read` can retry.
func offerReadToken(p *profile.Profile, password, device string, prompt func(label, def string) string, out io.Writer) {
	answer := strings.ToLower(strings.TrimSpace(prompt("  Create a read token for analysis commands (ls, usage, insights)? [Y/n]", "Y")))
	if answer != "" && answer != "y" && answer != "yes" {
		fmt.Fprintln(out, "  Skipped. Run 'cctrace auth read' later to create one.")
		return
	}
	name, err := issueReadToken(p, password, device)
	if err != nil {
		fmt.Fprintf(out, "  [!] Read token not created: %v\n", err)
		return
	}
	fmt.Fprintf(out, "  [OK] Read token %q created\n", name)
}

// dropStaleReadToken clears a read token carried over from a previous init when
// the user or server changed, so reads never go out with another account's token
// or to another host.
func dropStaleReadToken(existing, p *profile.Profile) {
	if existing == nil {
		return
	}
	if existing.User.ID != p.User.ID || profileHTTPAPIEndpoint(existing) != profileHTTPAPIEndpoint(p) {
		p.Server.ReadToken = ""
	}
}

// propagateReadToken moves every other local profile that still holds oldToken
// for the same user and server onto source's new token.
//
// Profiles init creates for additional Claude homes copy the default profile's
// server block, so one read token can live in several profiles. The server
// revokes the old token when it issues the replacement, which would otherwise
// leave those copies answering 401. sourceName is the profile the command ran
// on ("" for the default); saving it stays with the caller.
func propagateReadToken(source *profile.Profile, sourceName, oldToken string) (updated, failed []string) {
	if oldToken == "" || oldToken == source.Server.ReadToken {
		return nil, nil
	}
	sameTarget := func(p *profile.Profile) bool {
		return p.Server.ReadToken == oldToken && p.User.ID == source.User.ID &&
			profileHTTPAPIEndpoint(p) == profileHTTPAPIEndpoint(source)
	}
	move := func(label string, p *profile.Profile, save func(*profile.Profile) error) {
		if !sameTarget(p) {
			return
		}
		p.Server.ReadToken = source.Server.ReadToken
		if err := save(p); err != nil {
			failed = append(failed, label)
			return
		}
		updated = append(updated, label)
	}

	if sourceName != "" && profile.Exists() {
		if p, err := profile.Load(); err == nil {
			move("default", p, profile.Save)
		}
	}
	names, _ := profile.ListNamed()
	for _, name := range names {
		if name == sourceName {
			continue
		}
		if p, err := profile.LoadNamed(name); err == nil {
			move(name, p, func(p *profile.Profile) error { return profile.SaveNamed(p, name) })
		}
	}
	return updated, failed
}

// reportPropagation tells the user which other profiles moved to the new token.
func reportPropagation(out io.Writer, updated, failed []string) {
	if len(updated) > 0 {
		fmt.Fprintf(out, "  [OK] Also updated profiles sharing the old read token: %s\n", strings.Join(updated, ", "))
	}
	for _, name := range failed {
		fmt.Fprintf(out, "  [!] Could not update profile %q; run 'cctrace auth read --profile %s' there.\n", name, name)
	}
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}
