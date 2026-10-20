package store

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

type analysisTaskTouch struct {
	TurnKey string    `json:"turn_key"`
	Action  string    `json:"a"`
	At      time.Time `json:"ts"`
}

type sessionAnalysisP4State struct {
	CommitsState     string
	CommitCount      *int
	CommitsTruncated bool
	TasksState       string
}

func readSessionAnalysisP4State(ctx context.Context, tx pgx.Tx, id string) (sessionAnalysisP4State, error) {
	var state sessionAnalysisP4State
	err := tx.QueryRow(ctx, `SELECT commits_state,commit_count,commits_truncated,tasks_state
		FROM session_analysis_facts f WHERE f.session_id=$1 AND `+sessionAnalysisVisibleSQL, id).
		Scan(&state.CommitsState, &state.CommitCount, &state.CommitsTruncated, &state.TasksState)
	return state, err
}

func analysisSessionP4(ctx context.Context, tx pgx.Tx, id string, state sessionAnalysisP4State) (map[string]interface{}, map[string]interface{}, map[string][]interface{}, map[string][]string, error) {
	tasks := make([]interface{}, 0, 100)
	byTurnTasks := make(map[string]map[string]bool)
	taskRows, err := tx.Query(ctx, `SELECT item_key,kind,display_id,title,title_state,summary,url,added,completed,edited,commit_linked,touches
		FROM session_task_facts WHERE session_id=$1 ORDER BY first_ts,item_key COLLATE "C" LIMIT 101`, id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for taskRows.Next() {
		var key, kind, displayID, title, titleState, summary, itemURL string
		var added, completed, edited, linked bool
		var rawTouches []byte
		if err := taskRows.Scan(&key, &kind, &displayID, &title, &titleState, &summary, &itemURL, &added, &completed, &edited, &linked, &rawTouches); err != nil {
			taskRows.Close()
			return nil, nil, nil, nil, err
		}
		if len(tasks) == 100 {
			break
		}
		if title == "" {
			title = displayID
		}
		tasks = append(tasks, map[string]interface{}{
			"id": displayID, "kind": kind, "title": title, "title_state": titleState,
			"summary": summary, "url": optionalAnalysisText(itemURL),
			"actions": map[string]interface{}{"added": added, "completed": completed, "edited": edited, "commit_linked": linked},
		})
		var touches []analysisTaskTouch
		if err := json.Unmarshal(rawTouches, &touches); err != nil {
			taskRows.Close()
			return nil, nil, nil, nil, err
		}
		for _, touch := range touches {
			if touch.TurnKey == "" {
				continue
			}
			if byTurnTasks[touch.TurnKey] == nil {
				byTurnTasks[touch.TurnKey] = make(map[string]bool)
			}
			byTurnTasks[touch.TurnKey][displayID] = true
		}
	}
	if err := taskRows.Err(); err != nil {
		taskRows.Close()
		return nil, nil, nil, nil, err
	}
	taskRows.Close()

	commitRows, err := tx.Query(ctx, `SELECT c.turn_key,c.hash,c.short,c.subject,c.subject_state,c.committed_at,c.kind,c.source,c.repository,c.branch,
		COALESCE(refs.ids,'{}'::text[])
		FROM (SELECT * FROM session_commit_facts WHERE session_id=$1
			ORDER BY committed_at DESC,ordinal DESC LIMIT 201) c
		LEFT JOIN LATERAL (SELECT array_agg(t.display_id ORDER BY t.item_key COLLATE "C") AS ids
			FROM session_task_facts t WHERE t.session_id=c.session_id AND t.item_key=ANY(c.refs)) refs ON true
		ORDER BY c.committed_at DESC,c.ordinal DESC`, id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	type storedCommit struct {
		turnKey, hash, short, subject, subjectState string
		at                                          time.Time
		kind, source, repository, branch            string
		tasks                                       []string
	}
	var stored []storedCommit
	for commitRows.Next() {
		var item storedCommit
		if err := commitRows.Scan(&item.turnKey, &item.hash, &item.short, &item.subject, &item.subjectState,
			&item.at, &item.kind, &item.source, &item.repository, &item.branch, &item.tasks); err != nil {
			commitRows.Close()
			return nil, nil, nil, nil, err
		}
		stored = append(stored, item)
	}
	if err := commitRows.Err(); err != nil {
		commitRows.Close()
		return nil, nil, nil, nil, err
	}
	commitRows.Close()
	commitTruncated := state.CommitsTruncated || len(stored) > 200 || (state.CommitCount != nil && *state.CommitCount > 200)
	if len(stored) > 200 {
		stored = stored[:200]
	}
	for left, right := 0, len(stored)-1; left < right; left, right = left+1, right-1 {
		stored[left], stored[right] = stored[right], stored[left]
	}
	commitItems := make([]interface{}, 0, len(stored))
	byTurnCommits := make(map[string][]interface{})
	for _, item := range stored {
		tasks := make([]interface{}, 0, len(item.tasks))
		for _, taskID := range item.tasks {
			tasks = append(tasks, taskID)
			if item.turnKey != "" {
				if byTurnTasks[item.turnKey] == nil {
					byTurnTasks[item.turnKey] = make(map[string]bool)
				}
				byTurnTasks[item.turnKey][taskID] = true
			}
		}
		value := map[string]interface{}{
			"hash": optionalAnalysisText(item.hash), "short": optionalAnalysisText(item.short),
			"subject": item.subject, "ts": item.at.UTC().Format(time.RFC3339Nano),
			"source": item.source, "subject_state": item.subjectState, "tasks": tasks,
		}
		if item.turnKey != "" {
			value["turn_key"] = item.turnKey
		}
		if item.repository != "" {
			value["repository"] = item.repository
		}
		if item.branch != "" {
			value["branch"] = item.branch
		}
		commitItems = append(commitItems, value)
		if item.turnKey != "" {
			byTurnCommits[item.turnKey] = append(byTurnCommits[item.turnKey], map[string]interface{}{
				"short": optionalAnalysisText(item.short), "subject": item.subject, "tasks": tasks,
			})
		}
	}
	turnTasks := make(map[string][]string, len(byTurnTasks))
	for turn, taskSet := range byTurnTasks {
		values := make([]string, 0, len(taskSet))
		for task := range taskSet {
			values = append(values, task)
		}
		sort.Strings(values)
		turnTasks[turn] = values
	}
	var total interface{}
	if state.CommitsState == "observed" && state.CommitCount != nil {
		total = *state.CommitCount
	}
	commits := map[string]interface{}{"state": state.CommitsState, "total": total, "truncated": commitTruncated, "items": commitItems}
	tasksResponse := map[string]interface{}{"state": state.TasksState, "items": tasks}
	return commits, tasksResponse, byTurnCommits, turnTasks, nil
}

func optionalAnalysisText(value string) interface{} {
	if value == "" {
		return nil
	}
	return value
}

func attachSessionP4Turns(turns []interface{}, commits map[string][]interface{}, tasks map[string][]string) {
	for _, value := range turns {
		turn, ok := value.(map[string]interface{})
		if !ok {
			continue
		}
		key, _ := turn["key"].(string)
		if items := commits[key]; items != nil {
			turn["commits"] = items
		}
		if items := tasks[key]; items != nil {
			turn["tasks"] = items
		}
	}
}
