# 페이지

대시보드 각 페이지가 보여 주는 내용과 사용 가능한 역할.

모든 페이지는 로그인이 필요하다. `user` 계정은 본인 세션만 보지만 집계는 로그인한 모든 사용자가 공유한다. [개인정보](../reference/privacy.md#who-can-see-what) 참고.

## 헤더 {#header}

모든 페이지 위의 헤더에 에이전트 선택기와 계정 선택기가 있다. 이를 지원하는 페이지는 선택한 에이전트와 계정으로 데이터를 좁힌다.

## Sessions (`/sessions`) {#sessions-sessions}

왼쪽에 세션 목록, 오른쪽에 선택한 세션의 트랜스크립트.

- 목록에서 세션을 선택하면 트랜스크립트가 열림. 스크롤하면 더 불러오고 조건에 맞는 세션 수와 전체 수를 표시
- **Assembled**: 관련 세션 파일(서브에이전트, 분기)을 한 대화로 합침. **Raw**: 파일별 레코드를 그대로, 파일 단위로 묶어 표시
- **Interactive**(기본값): 사람의 입력이 있는 세션. **Headless**: `claude -p` 같은 기계 구동 세션. **All**: 둘 다
- 프로젝트 선택기로 한 프로젝트만 표시. 검색창은 불러온 세션을 세션, 사용자, 프로젝트로 거름

### 세션 삭제 {#delete-a-session}

트랜스크립트 헤더의 휴지통 아이콘(툴팁 `세션 삭제`)은 세션 레코드와 거기 딸린 이벤트·메트릭을 삭제한다. 되돌릴 수 없고 클라이언트가 같은 세션을 다시 올려도 서버가 거부한다.

| 역할 | 삭제 범위 |
|---|---|
| `admin` | 모든 세션. 이어서 프로젝트의 다른 세션 삭제와 프로젝트 수집 중단을 묻는 대화상자 표시 |
| `user` | 본인 세션. 소유자 삭제 정책이 허용할 때(기본값 허용) |

관리자는 휴지통 옆 톱니 대화상자(`세션 수집 설정`)에서 소유자 삭제 정책을 정하고 차단 목록의 프로젝트를 해제한다. 프로젝트 선택기에서 프로젝트 전체를 삭제할 수도 있다.

## Users (`/users`) {#users-users}

**Analytics** 탭은 사용자를 비용 순으로 나열한다. 관리자에게는 **Management** 탭이 추가되며, 이 탭은 [사용자](users.md)에서 설명한다.

## Plugins & Skills (`/plugins`) {#plugins-skills-plugins}

Claude Code 슬래시 명령·스킬 호출과 Codex 스킬 주입을 플러그인 또는 스킬 이름별로 묶어 표시.

- 기간: **7d**, **30d**(기본값), **90d**, **All**
- 요약 카드: **Claude Calls**, **Codex Calls**, **Avg Tokens**, **Total**, **Input**, **Output** 토큰
- 보기: **By Skill**, **By User**, **By Project**. **By Project**는 git 저장소 안의 세션만 집계

`/skills`는 이 페이지로 이동.

## Rules (`/rules`) {#rules-rules}

세션이 실행된 git 저장소에서 클라이언트가 찾은 CLAUDE.md와 AGENTS.md 파일, 그리고 버전 이력.

- 텍스트, 상태(**Active**, **Missing**), 저장소로 필터. 요약 카드는 **Repositories**와 **Rule Files** 수
- 파일을 선택하면 버전별 내용 확인과 댓글 작성 가능
- `user` 계정은 본인 세션이 있는 저장소의 규칙 파일만 조회

## Logs (`/logs`) {#logs-logs}

최근 30일 원시 텔레메트리, 페이지당 50행.

| 탭 | 열 |
|---|---|
| Events | Time, Event, Model, Session, User, Input, Output, Cost, Age |
| Metrics | Time, Metric, Model, User, Value, Session, Age |

Logs는 본인 데이터로 제한되지 않는다. 프롬프트나 도구 출력처럼 자유 텍스트를 담은 속성은 페이지로 오기 전에 제거된다.

## Admin (`/admin`) {#admin-admin}

관리자에게만 보임.

| 탭 | 내용 |
|---|---|
| **Clients** | 버전을 보고한 클라이언트별 Profile Email, Name, User ID, Client Version, Last Seen, Status |
| **Storage** | 보존 기간, 테이블 크기, 데이터 볼륨 여유 공간. 보존 기간은 여기서 수정. [운영](../server/operations.md#data-retention) 참고 |

`/versions`와 `/storage`는 `/admin`으로 이동.

**Excluded Accounts**, **Unpriced Models**, **Insights**, **AI** 탭은 변경이 진행 중이라 이 문서에서 다루지 않음.

## Settings (`/settings`) {#settings-settings}

- **Change Password**: 현재 비밀번호와 8자 이상의 새 비밀번호 입력. 임시 비밀번호 계정은 변경할 때까지 이 페이지로 이동
- **Appearance**: 강조 색, 에이전트 색조, 로고 마크 표시 여부

API 토큰과 결제 계정 섹션은 변경이 진행 중이라 이 문서에서 다루지 않음.

## 변경이 진행 중인 페이지 {#pages-under-active-development}

다음 페이지는 존재하지만 변경이 진행 중이라 이 문서에서 다루지 않음.

| 페이지 | 사이드바 항목 |
|---|---|
| `/` | Overview |
| `/cost` | Usage |
| `/tools` | Tools |
| `/weekly` | Weekly report |
| `/open-api` | Open API |
