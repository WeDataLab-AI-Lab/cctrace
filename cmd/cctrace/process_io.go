package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cctrace/internal/processio"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

const processIOQueueFile = "session-process-io.json"

var processIOReportInterval = 15 * time.Second

func processIOQueue(profileName string) processio.Queue {
	return processio.Queue{Path: filepath.Join(syncProfileDir(profileName), processIOQueueFile)}
}

func profileSyncEndpoint(p *profile.Profile) string {
	if p.Server.SyncEndpoint != "" {
		return p.Server.SyncEndpoint
	}
	return p.Server.Endpoint
}

func processIOAccountExcluded(entries []string, provider, accountID string) bool {
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if configuredProvider, configuredID, ok := strings.Cut(entry, ":"); ok {
			if strings.EqualFold(strings.TrimSpace(configuredProvider), provider) && strings.TrimSpace(configuredID) == accountID {
				return true
			}
		} else if entry == accountID {
			return true
		}
	}
	return false
}

// flushProcessIOQueue sends only scalar process counters and their mapping keys.
// A successful HTTP response settles precisely the snapshot that was sent.
func flushProcessIOQueue(ctx context.Context, queue processio.Queue, client *syncer.Client, profileEmail, userID string, excludedAccounts []string, profiles ...*profile.Profile) error {
	if len(profiles) > 0 && profiles[0] != nil && (profiles[0].HomesVersion != 0 || profiles[0].Homes != nil) {
		p := profiles[0]
		return p.WithDefinitionFence(func() error {
			return flushProcessIOQueueFor(ctx, queue, client, profileEmail, userID, excludedAccounts, false, func(report processio.QueuedReport) bool { return processIOHomeAllowed(p, report) })
		})
	}
	return flushProcessIOQueueFor(ctx, queue, client, profileEmail, userID, excludedAccounts, true)
}

// flushProcessIOQueueFor is flushProcessIOQueue with a choice for the reports of
// another identity. They are settled unsent when ackOtherIdentity is true -- a
// report of a profile whose email or user changed is not this one's to send. The
// managed job keeps them for the queue's retention instead.
func flushProcessIOQueueFor(ctx context.Context, queue processio.Queue, client *syncer.Client, profileEmail, userID string, excludedAccounts []string, ackOtherIdentity bool, policies ...func(processio.QueuedReport) bool) error {
	if _, err := os.Stat(queue.Path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	reports, err := queue.List()
	if err != nil {
		return err
	}
	for _, report := range reports {
		if len(policies) > 0 && !policies[0](report) {
			continue
		}
		if report.ProfileEmail != profileEmail || report.UserID != userID {
			if ackOtherIdentity {
				if err := queue.Ack(report); err != nil {
					return err
				}
			}
			continue
		}
		if processIOAccountExcluded(excludedAccounts, report.BillingProvider, report.AccountID) {
			if err := queue.Ack(report); err != nil {
				return err
			}
			continue
		}
		asOf := report.Report.AsOf.UTC()
		status, statusSource, statusChangedAt := report.Status, report.StatusSource, report.StatusChangedAt
		if statusSource == "" {
			if status == "gone" {
				statusSource = "process_io_lifecycle"
			} else {
				status, statusSource, statusChangedAt = "unknown", "process_io_unknown", nil
			}
		}
		item := syncer.SessionLiveStateItem{
			SessionID: report.SessionID, Agent: report.Agent, ProjectHash: report.ProjectHash,
			BillingProvider: report.BillingProvider, AccountID: report.AccountID,
			PID: int64(report.PID), ProcStart: report.ProcStart, Status: status, StatusSource: statusSource,
			HeartbeatAt:     asOf,
			StatusChangedAt: statusChangedAt,
			IOEpoch:         report.IOEpoch, IOAsOf: &asOf, IOSource: "process_stream", IOMode: string(report.Report.Mode),
			IOPartial: report.Report.Partial, IOFailureCode: report.Report.FailureCode,
			InputBytes: int64Pointer(report.Report.Counts.InputBytes), InputLines: int64Pointer(report.Report.Counts.InputLines),
			OutputBytes: int64Pointer(report.Report.Counts.OutputBytes), OutputLines: int64Pointer(report.Report.Counts.OutputLines),
			StdoutBytes: report.Report.Counts.StdoutBytes, StdoutLines: report.Report.Counts.StdoutLines,
			StderrBytes: report.Report.Counts.StderrBytes, StderrLines: report.Report.Counts.StderrLines,
		}
		if status == "gone" {
			item.StatusChangedAt = &asOf
		}
		_, err := client.SendSessionObservations(ctx, syncer.SessionObservationsRequest{
			ProfileEmail: profileEmail, UserID: userID, LiveStates: []syncer.SessionLiveStateItem{item},
		})
		if err != nil {
			return err
		}
		if err := queue.Ack(report); err != nil {
			return err
		}
	}
	return nil
}

func int64Pointer(value int64) *int64 { return &value }

func processIOMappingError(agent string) error {
	return fmt.Errorf("could not safely map the launched %s process to one session; no I/O counters were attributed", agent)
}

// Unattributed legacy reports stay pending under explicit consent. New reports
// carry both their launch home and the definition that authorized observation.
func processIOHomeAllowed(p *profile.Profile, report processio.QueuedReport) bool {
	if p.HomesVersion == 0 && p.Homes == nil {
		return true
	}
	if report.SourceRevision == "" || report.SourceRevision != p.DefinitionRevision() || !p.CollectionEnabled(report.Agent) {
		return false
	}
	for _, h := range p.ProviderHomes(report.Agent, "") {
		if h.Home == report.SourceHome && h.Transmission == "allow" && p.CollectionEnabled(h.Provider) && profile.CheckCodexHome(h.Home) == nil {
			return true
		}
	}
	return false
}
