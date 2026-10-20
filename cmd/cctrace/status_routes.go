package main

import (
	"fmt"
	"strings"

	"cctrace/internal/localdaemon"
	"cctrace/internal/profile"
	"cctrace/internal/syncer"
)

// readDaemonSnapshotFn returns the running daemon's latest snapshot, and false
// when no daemon is running or the snapshot is not that daemon's current one.
var readDaemonSnapshotFn = func() (localdaemon.Snapshot, bool) {
	return localdaemon.LiveSnapshot(profile.DefaultDir())
}

// routeNoticeIndent lines continuation rows up under the value column, as the
// notices above it in `cctrace status` are.
const routeNoticeIndent = "                   "

// routeLabelWords are the names of where a folder came from, as the planner is
// given them. A folder is never named by its path: it is the person's own, and
// an explicit Codex home is the one a policy report hides as well.
var routeLabelWords = map[string]string{
	"home": "홈 폴더", "default": "기본 폴더", "env": "CODEX_HOME", "configured": "설정 폴더", "explicit": "[명시 Codex 홈]",
}

// holderStateWords say what the profile that holds a source is doing. A holder
// that is gone, nobody's or not known has no state to speak of, and is named
// for that instead.
var holderStateWords = map[string]string{
	localdaemon.HolderActive: "수집 중", localdaemon.HolderStopped: "중지됨", localdaemon.HolderDisabled: "꺼짐",
	localdaemon.HolderBlocked: "막힘", localdaemon.HolderTransferring: "이전 중",
}

var holderlessWords = map[string]string{
	localdaemon.HolderRemoved: "(삭제됨)", localdaemon.HolderUnowned: "(주인 없음)", localdaemon.HolderUnknown: "(알 수 없음)",
}

// routesNoticeFor explains which of a profile's sources the daemon leaves to
// another profile, or returns "" when there are none. A running daemon is asked
// for what it published. With none running the answer is the plan it would make,
// since that is what the one-shot collection follows; a daemon that runs but has
// published nothing current is not guessed for.
func routesNoticeFor(profileName string) string {
	id := "default"
	if profileName != "" {
		id = profile.ProfileID(profileName)
	}
	shared, all, ok := leftOutSources(id)
	if !ok {
		return ""
	}
	return routeNotice(shared, all, afterBlockMarked(profileName))
}

func leftOutSources(id string) (shared []localdaemon.SharedSource, allExcluded, ok bool) {
	root := profile.DefaultDir()
	if snapshot, live := readDaemonSnapshotFn(); live {
		for _, job := range snapshot.Jobs {
			if job.ID == id {
				return job.SharedSources, job.ErrorCode == "duplicate_route", true
			}
		}
		return nil, false, false
	}
	if running, err := localdaemon.Running(root); err != nil || running {
		return nil, false, false
	}
	entries, err := (profile.Registry{Root: root}).List()
	if err != nil {
		return nil, false, false
	}
	specs := make([]localdaemon.Spec, 0, len(entries))
	for _, entry := range entries {
		specs = append(specs, profileJobSpec(entry, daemonOverrides{}))
	}
	job, found := localdaemon.PlanOffline(root, specs).Job(id)
	return job.Excluded, job.AllExcluded, found
}

// afterBlockMarked says whether any of the profile's state files began a folder
// when the profile was let into it, not when it was held back from it.
func afterBlockMarked(profileName string) bool {
	for _, path := range syncStatePaths(profileName) {
		st, err := syncer.LoadState(path)
		if err != nil {
			continue
		}
		for _, mark := range st.Sources {
			if mark.AfterBlock {
				return true
			}
		}
	}
	return false
}

// routeNotice is the text after `수집 경로:`. Nothing left out is no notice:
// what is normal stays quiet.
func routeNotice(shared []localdaemon.SharedSource, allExcluded, afterBlock bool) string {
	if len(shared) == 0 {
		return ""
	}
	var out []string
	if allExcluded {
		out = append(out, "[!] 이 프로필의 모든 경로가 다른 프로필과 겹쳐 수집이 멈춰 있습니다 (duplicate_route)")
	} else {
		out = append(out, fmt.Sprintf("[!] 이 프로필에서 수집하지 않는 경로 %d개 -- 같은 서버·사용자로 수집하는 다른 프로필이 맡고 있습니다", len(shared)))
	}
	kindWidth, labelWidth, noID := 0, 0, false
	for _, s := range shared {
		kindWidth, labelWidth = max(kindWidth, columnWidth(s.Kind)), max(labelWidth, columnWidth(routeLabel(s.Label)))
		noID = noID || s.Source == ""
	}
	for _, s := range shared {
		id := s.Source
		if id == "" {
			id = "-"
		}
		row := padColumn(s.Kind, kindWidth) + "  " + padColumn(routeLabel(s.Label), labelWidth) + "  " + id + "  맡은 프로필: " + routeHolder(s)
		if s.Reason == localdaemon.ShareNested {
			row += " -- 일부 파일은 어느 프로필에도 수집되지 않을 수 있음"
		}
		out = append(out, routeNoticeIndent+row)
	}
	switch {
	case allExcluded:
		out = append(out, routeNoticeIndent+"cctrace profile claim <프로필> --all 로 이 프로필이 맡거나, 겹친 쪽 설정을 바꾸세요")
	case noID:
		out = append(out, routeNoticeIndent+"나머지 경로는 정상 수집합니다. 예약 파일을 읽지 못해 id가 없으므로 폴더로 지정합니다: cctrace profile claim <프로필> --source <폴더>")
	default:
		out = append(out, routeNoticeIndent+"나머지 경로는 정상 수집합니다. 맡길 프로필을 바꾸려면: cctrace profile claim <프로필> --source <id>")
	}
	if afterBlock {
		out = append(out, routeNoticeIndent+"겹침으로 막혀 있던 기간의 세션은 수집되지 않았습니다 (수집을 시작한 시점부터 수집)")
	}
	return strings.Join(out, "\n")
}

func routeLabel(label string) string {
	if word, ok := routeLabelWords[label]; ok {
		return word
	}
	return routeLabelWords["configured"]
}

func routeHolder(s localdaemon.SharedSource) string {
	if word, ok := holderlessWords[s.HolderState]; ok {
		return word
	}
	name := s.HolderName
	if name == "" {
		name = strings.TrimPrefix(s.Holder, "named:")
	}
	state, ok := holderStateWords[s.HolderState]
	if name == "" || !ok {
		return holderlessWords[localdaemon.HolderUnknown]
	}
	return name + " (" + state + ")"
}

// columnWidth is how many terminal columns text takes: Hangul and other wide
// characters take two.
func columnWidth(text string) int {
	width := 0
	for _, r := range text {
		width++
		if r >= 0x1100 && (r <= 0x115F || (r >= 0x2E80 && r <= 0xA4CF) || (r >= 0xAC00 && r <= 0xD7A3) || (r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFF00 && r <= 0xFF60)) {
			width++
		}
	}
	return width
}

func padColumn(text string, width int) string {
	return text + strings.Repeat(" ", max(0, width-columnWidth(text)))
}
