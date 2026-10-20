package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"cctrace/internal/atomicfile"
	"cctrace/internal/localapi"
)

type localPolicy struct {
	Enabled     bool      `json:"enabled"`
	ExactOrigin string    `json:"exact_origin"`
	StablePort  int       `json:"stable_port"`
	TokenHash   string    `json:"token_hash"`
	Generation  int       `json:"generation"`
	CreatedAt   time.Time `json:"created_at"`
}

func localPolicyCommands() []*cobra.Command {
	var name, origin string
	enable := &cobra.Command{Use: "enable", Short: "Enable automatic local session analysis", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return runLocalEnable(cmd.OutOrStdout(), name, origin) }}
	enable.Flags().StringVar(&origin, "origin", "", "exact web origin of the dashboard")
	_ = enable.MarkFlagRequired("origin")
	status := &cobra.Command{Use: "status", Short: "Show local analysis connection status", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return runLocalStatus(cmd.OutOrStdout(), name) }}
	disable := &cobra.Command{Use: "disable", Short: "Revoke automatic local session analysis", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return runLocalDisable(name) }}
	commands := []*cobra.Command{enable, status, disable}
	for _, cmd := range commands {
		cmd.Flags().StringVar(&name, "profile", "", "profile name")
	}
	return commands
}

func readLocalPolicy(name string) (localPolicy, error) {
	var policy localPolicy
	data, err := os.ReadFile(filepath.Join(syncProfileDir(name), "local-api-policy.json"))
	if errors.Is(err, os.ErrNotExist) {
		return policy, nil
	}
	if err != nil {
		return policy, errors.New("cannot read local API policy")
	}
	if json.Unmarshal(data, &policy) != nil {
		return policy, errors.New("invalid local API policy")
	}
	return policy, nil
}

func writeLocalJSON(name, filename string, value any) error {
	dir := syncProfileDir(name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(dir, filename), data, 0600)
}

func validateLocalPolicy(policy localPolicy) error {
	// The allowed origin is the web page's origin, so its port is not required:
	// an HTTPS dashboard on the default port is a valid one. localapi.New applies
	// the exact-origin rules (scheme, no path/query/user, no wildcard).
	if policy.StablePort < 1 || policy.StablePort > 65535 {
		return errors.New("invalid local API port")
	}
	_, err := localapi.New(localapi.Options{Address: localPolicyAddress(policy), AllowedOrigins: []string{policy.ExactOrigin}, TokenHash: policy.TokenHash})
	return err
}

func localPolicyAddress(policy localPolicy) string {
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(policy.StablePort))
}

func runLocalEnable(out io.Writer, name, origin string) error {
	p, err := loadProfile(name)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	previous, err := readLocalPolicy(name)
	if err != nil {
		return err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	policy := localPolicy{Enabled: true, ExactOrigin: origin, StablePort: 1, TokenHash: hex.EncodeToString(digest[:]), Generation: previous.Generation + 1, CreatedAt: time.Now().UTC()}
	if err := validateLocalPolicy(policy); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return errors.New("cannot allocate local API port")
	}
	policy.StablePort = listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return err
	}
	if err := writeLocalJSON(name, "local-api-policy.json", policy); err != nil {
		return err
	}
	p.Options.LocalAnalysisEnabled = true
	if err := saveProfile(p, name); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		Endpoint string `json:"endpoint"`
		Token    string `json:"token"`
	}{"http://" + localPolicyAddress(policy), base64.RawURLEncoding.EncodeToString(raw)})
}

func runLocalDisable(name string) error {
	p, err := loadProfile(name)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	policy, err := readLocalPolicy(name)
	if err != nil {
		return err
	}
	policy.Enabled = false
	policy.TokenHash = ""
	policy.Generation++
	if err := writeLocalJSON(name, "local-api-policy.json", policy); err != nil {
		return err
	}
	p.Options.LocalAnalysisEnabled = false
	return saveProfile(p, name)
}

func runLocalStatus(out io.Writer, name string) error {
	p, err := loadProfile(name)
	if err != nil {
		return fmt.Errorf("load profile: %w", err)
	}
	policy, err := readLocalPolicy(name)
	if err != nil {
		return err
	}
	var status localAnalysisStatus
	if _, err := readJSONFile(filepath.Join(syncProfileDir(name), "local-api-status.json"), &status); err != nil {
		return errors.New("cannot read local API status")
	}
	status.Enabled = p.Options.LocalAnalysisEnabled && policy.Enabled
	status.Origin = policy.ExactOrigin
	if policy.StablePort != 0 {
		status.Address = localPolicyAddress(policy)
	}
	lockFree, lockErr := isSyncLockFree(name)
	status.Running = status.Running && status.Enabled && status.Generation == policy.Generation && lockErr == nil && !lockFree
	return json.NewEncoder(out).Encode(status)
}
