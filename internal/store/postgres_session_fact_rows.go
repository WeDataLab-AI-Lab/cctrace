package store

import (
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"cctrace/internal/sessionfacts"
)

func scanSessionFactClaudeRow(row pgx.Row) (sessionfacts.ClaudeRow, error) {
	var r sessionfacts.ClaudeRow
	var ts time.Time
	var nullable [26]*string
	err := row.Scan(&r.ID, &ts, &r.RecordType, &r.UUID, &r.ParentUUID, &r.IsSidechain, &r.AgentID,
		&r.SourceFile, &r.ToolUseID, &r.IsCompactSummary, &r.IsMeta, &r.PromptSource, &r.Entrypoint,
		&r.CommandName, &r.CommandInvoke, &r.Model, &r.ForkedFromSession,
		&r.UserID, &r.ProfileEmail, &r.LoginEmail, &r.LoginEmailSource,
		&r.BillingProvider, &r.AccountID, &r.ProjectHash, &r.RepositoryID, &r.RepoSubpath, &r.Branch,
		&r.TaskType, &r.MessageID, &nullable[0],
		&r.CctraceCollect, &r.CctraceDropped, &r.CctraceWithheld,
		&nullable[1], &nullable[2], &nullable[3], &r.TurnPosition,
		&nullable[4], &nullable[5], &nullable[6],
		&r.ImagePasteCount, &r.ToolUseResult, &r.IsAPIErrorMessage, &r.HasError,
		&nullable[7], &nullable[8], &nullable[9], &r.PendingBackgroundAgentCount,
		&r.PendingWorkflowCount, &r.Attachment, &nullable[10], &nullable[11], &r.Blocks)
	if err != nil {
		return r, fmt.Errorf("scan claude slim: %w", err)
	}
	r.TS = ts
	r.CWD = textOrEmpty(nullable[0])
	r.PromptID = textOrEmpty(nullable[1])
	r.OriginKind = textOrEmpty(nullable[2])
	r.TurnOrigin = textOrEmpty(nullable[3])
	r.PermissionMode = textOrEmpty(nullable[4])
	r.ToolDenialKind = textOrEmpty(nullable[5])
	r.InterruptedMessageID = textOrEmpty(nullable[6])
	r.RawRedacted = textOrEmpty(nullable[7])
	r.Subtype = textOrEmpty(nullable[8])
	r.LogicalParentUUID = textOrEmpty(nullable[9])
	r.ContentKind = textOrEmpty(nullable[10])
	r.TextHead = textOrEmpty(nullable[11])
	return r, nil
}

func scanSessionFactCodexRow(row pgx.Row) (sessionfacts.CodexRow, error) {
	var r sessionfacts.CodexRow
	var rawType, role, payloadType, turnID, rootID, responseID, text *string
	err := row.Scan(&r.ID, &r.TS, &r.RecordType, &r.Model, &r.InputTokens, &r.OutputTokens,
		&r.CacheReadTokens, &r.CacheCreateTokens, &r.UserID, &r.ProfileEmail, &r.LoginEmail,
		&r.LoginEmailSource, &r.BillingProvider, &r.AccountID, &r.ProjectHash, &r.RepositoryID,
		&r.Branch, &r.Entrypoint, &rawType, &role, &r.CommandName, &payloadType,
		&turnID, &rootID, &responseID, &text, &r.ContextWindow, &r.UUID,
		&r.SourceFile, &r.ToolName, &r.ToolCallID, &r.ToolArgs, &r.ToolArgsTruncated, &r.ToolOutput, &r.ToolStatus, &r.CWD,
		&r.CodexTurnID, &r.CodexRootTurnID, &r.CodexTurnMetadataKnown)
	if err != nil {
		return r, fmt.Errorf("scan codex slim: %w", err)
	}
	r.RawType = textOrEmpty(rawType)
	r.Role = textOrEmpty(role)
	r.PayloadType = textOrEmpty(payloadType)
	r.TurnID = textOrEmpty(turnID)
	r.RootTurnID = textOrEmpty(rootID)
	r.ResponseID = textOrEmpty(responseID)
	r.TextHead = textOrEmpty(text)
	return r, nil
}
