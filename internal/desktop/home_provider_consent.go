package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"cctrace/internal/atomicfile"
	"cctrace/internal/codexconfig"
	"cctrace/internal/envgen"
	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
)

type providerConsentEffect struct {
	Provider string `json:"provider"`
	Home     string `json:"home"`
	Effect   string `json:"effect"`
}
type providerFilePlan struct {
	Home, Provider, Identity string
	Before, After            []byte
}
type homeProviderRecovery struct {
	Home     string `json:"home"`
	Provider string `json:"provider"`
	Identity string `json:"directory_identity"`
	Before   string `json:"before"`
	After    string `json:"after"`
}

func providerSettingsFile(provider string) string {
	if provider == "claude" {
		return "settings.json"
	}
	return "config.toml"
}

func (s *Service) quiesceConsentProfiles(ctx context.Context, updates []profile.HomeUpdate) (func(), error) {
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	releases := []func(){}
	release := func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}
	for _, u := range updates {
		entry, err := s.registry.Load(u.Name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			release()
			return nil, err
		}
		if entry.Revision != u.Revision {
			release()
			return nil, profile.ErrConflict
		}
		stop, err := localdaemon.AcquireProfile(bounded, entry.RuntimeDir)
		if err != nil {
			release()
			return nil, err
		}
		releases = append(releases, stop)
	}
	return release, nil
}

// Planning reads no auth files and sends no provider requests. Desired config
// bytes and their exact effects are frozen before the confirmation is shown.
func (s *Service) planHomeProviderConsent(plan *consentPlan) error {
	if plan.kind == "transmission" {
		return nil
	}
	owners := []*profile.Profile{}
	for _, server := range plan.servers {
		p := profile.NewDefault()
		p.Server = server
		owners = append(owners, p)
	}
	seen := map[string]bool{}
	for _, u := range plan.Profiles {
		if u.Retire {
			continue
		}
		p := profile.NewDefault()
		p.Options.SyncEnabled = false
		if entry, err := s.registry.Load(u.Name); err == nil {
			p = entry.Profile
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if u.Definition != nil {
			p = u.Definition
		}
		for _, h := range u.Homes {
			key := h.Provider + "\x00" + h.Home
			if seen[key] || !slices.Contains(plan.homes, h.Home) {
				continue
			}
			seen[key] = true
			if p.CollectionEnabled(h.Provider) {
				continue
			}
			identity, err := profile.DirectoryIdentity(h.Home)
			if err != nil {
				return err
			}
			path := filepath.Join(h.Home, providerSettingsFile(h.Provider))
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Size() > 1<<20 {
				return profile.ErrUnsafe
			}
			before, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			after := before
			if h.Provider == "claude" {
				after, err = envgen.PlanOwnedHomeRemoval(before, owners...)
			} else {
				for _, server := range plan.servers {
					after, err = codexconfig.PlanOwnedOtelRemoval(after, server.Endpoint, server.AuthToken)
					if err != nil {
						break
					}
				}
			}
			if err != nil {
				return err
			}
			if bytes.Equal(before, after) {
				continue
			}
			plan.providerPlans = append(plan.providerPlans, providerFilePlan{Home: h.Home, Provider: h.Provider, Identity: identity, Before: before, After: after})
			plan.ProviderEffects = append(plan.ProviderEffects, providerConsentEffect{Provider: h.Provider, Home: h.Home, Effect: "Remove only proven cctrace-managed hooks/exporter values; retain foreign exporter settings and provider credentials. Existing provider processes require restart to drop inherited telemetry environment."})
		}
	}
	return nil
}
func applyProviderFile(p providerFilePlan) error {
	identity, err := profile.DirectoryIdentity(p.Home)
	if err != nil || identity != p.Identity {
		return profile.ErrConflict
	}
	if p.Provider == "claude" {
		return envgen.ApplyHomeSettingsCAS(p.Home, p.Before, p.After)
	}
	if p.Provider == "codex" {
		return codexconfig.ApplyHomeConfigCAS(p.Home, p.Before, p.After)
	}
	return profile.ErrUnsafe
}

func (s *Service) applyHomeProviderConsent(directory string, plan consentPlan) error {
	records := []homeProviderRecovery{}
	// Both before and after hashes are durable BEFORE the first provider write,
	// including the crash window immediately after any individual CAS succeeds.
	for i, p := range plan.providerPlans {
		if err := atomicfile.Write(filepath.Join(directory, fmt.Sprintf("provider-%d.original", i)), p.Before, 0600); err != nil {
			return err
		}
		records = append(records, homeProviderRecovery{Home: p.Home, Provider: p.Provider, Identity: p.Identity, Before: consentDigest(p.Before), After: consentDigest(p.After)})
	}
	data, _ := json.Marshal(records)
	if err := atomicfile.Write(filepath.Join(directory, "provider-recovery.json"), data, 0600); err != nil {
		return err
	}
	for _, p := range plan.providerPlans {
		identity, err := profile.DirectoryIdentity(p.Home)
		if err != nil || identity != p.Identity {
			return profile.ErrConflict
		}
		current, err := os.ReadFile(filepath.Join(p.Home, providerSettingsFile(p.Provider)))
		if err != nil || !bytes.Equal(current, p.Before) {
			return profile.ErrConflict
		}
	}
	for i, p := range plan.providerPlans {
		if err := applyProviderFile(p); err != nil {
			return err
		}
		if s.providerConsentAfterWrite != nil {
			if err := s.providerConsentAfterWrite(i); err != nil {
				return err
			}
		}
	}
	return nil
}
func recoverHomeProviderConsent(directory string) error {
	data, err := os.ReadFile(filepath.Join(directory, "provider-recovery.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var records []homeProviderRecovery
	if json.Unmarshal(data, &records) != nil || len(records) > 256 {
		return profile.ErrUnsafe
	}
	plans := []providerFilePlan{}
	for i, r := range records {
		if r.Provider != "claude" && r.Provider != "codex" {
			return profile.ErrUnsafe
		}
		identity, err := profile.DirectoryIdentity(r.Home)
		if err != nil || identity != r.Identity {
			return profile.ErrConflict
		}
		current, err := os.ReadFile(filepath.Join(r.Home, providerSettingsFile(r.Provider)))
		if err != nil {
			return err
		}
		digest := consentDigest(current)
		if digest != r.Before && digest != r.After {
			return profile.ErrConflict
		}
		original, err := os.ReadFile(filepath.Join(directory, fmt.Sprintf("provider-%d.original", i)))
		if err != nil || consentDigest(original) != r.Before {
			return profile.ErrUnsafe
		}
		plans = append(plans, providerFilePlan{Home: r.Home, Provider: r.Provider, Identity: r.Identity, Before: current, After: original})
	}
	for _, p := range plans {
		if err := applyProviderFile(p); err != nil {
			return err
		}
	}
	return nil
}
