package main

import (
	"context"
	"net"
	"sync"
	"time"

	"cctrace/internal/localapi"
	"cctrace/internal/profile"
)

type localAnalysisStatus struct {
	Enabled    bool       `json:"enabled"`
	Running    bool       `json:"running"`
	Origin     string     `json:"origin"`
	Address    string     `json:"address"`
	Generation int        `json:"generation"`
	StartedAt  *time.Time `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at"`
	Error      string     `json:"error"`
}

type localAnalysis struct {
	cancel   context.CancelFunc
	listener net.Listener
	done     chan struct{}
	once     sync.Once
}

func (a *localAnalysis) Close() {
	if a == nil {
		return
	}
	a.once.Do(func() { a.cancel(); _ = a.listener.Close() })
	select {
	case <-a.done:
	case <-time.After(time.Second):
	}
}

func startLocalAnalysis(parent context.Context, name string, p *profile.Profile, claudeDir string) *localAnalysis {
	if !p.Options.LocalAnalysisEnabled {
		return nil
	}
	policy, err := readLocalPolicy(name)
	status := localAnalysisStatus{Enabled: policy.Enabled, Origin: policy.ExactOrigin, Generation: policy.Generation}
	save := func() {
		if err := writeLocalJSON(name, "local-api-status.json", status); err != nil {
			diagf("[local] cannot save connection status")
		}
	}
	if err != nil {
		status.Error = "cannot read local API policy"
		save()
		return nil
	}
	if !policy.Enabled {
		return nil
	}
	status.Address = localPolicyAddress(policy)
	if err := validateLocalPolicy(policy); err != nil {
		status.Error = "invalid local API policy"
		save()
		return nil
	}
	listener, err := net.Listen("tcp", status.Address)
	if err != nil {
		status.Error = "cannot bind local API port"
		save()
		return nil
	}
	server, err := localapi.New(localapi.Options{Address: status.Address, AllowedOrigins: []string{policy.ExactOrigin}, TokenHash: policy.TokenHash, ClaudeDirs: []string{resolveClaudeDir(p, claudeDir)}, CodexDirs: resolveCodexScanDirs(diagWriter(), p)})
	if err != nil {
		_ = listener.Close()
		status.Error = "cannot initialize local API roots"
		save()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	a := &localAnalysis{cancel: cancel, listener: listener, done: make(chan struct{})}
	started := time.Now().UTC()
	status.Running = true
	status.StartedAt = &started
	save()
	go func() {
		// Policy rotation and revocation apply to a running watch without exposing
		// either the raw token or the hash through the status file.
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = listener.Close()
				return
			case <-a.done:
				return
			case <-ticker.C:
				current, err := readLocalPolicy(name)
				if err != nil || !current.Enabled || current.Generation != policy.Generation {
					cancel()
					_ = listener.Close()
					return
				}
			}
		}
	}()
	go func() {
		defer close(a.done)
		err := server.Serve(ctx, listener)
		if err != nil && ctx.Err() == nil {
			status.Error = "local API stopped unexpectedly"
		}
		cancel()
		status.Running = false
		ended := time.Now().UTC()
		status.EndedAt = &ended
		save()
	}()
	return a
}
