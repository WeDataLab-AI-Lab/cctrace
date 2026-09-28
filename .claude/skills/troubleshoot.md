# claude-code-trace 트러블슈팅 스킬

이 스킬은 claude-code-trace 프로젝트의 문제를 진단하고 해결하는 데 사용합니다.

## 프로젝트 개요

- **구조**: Go 백엔드(cctraced 서버 + cctrace CLI) + Next.js 프론트엔드 (web/)
- **포트**: 4317 (gRPC OTLP), 8080 (HTTP REST API + 웹 UI)
- **DB**: TimescaleDB (PostgreSQL) — 포트 5432
- **인증**: JWT (대시보드), API_KEY (CLI sync), CLI 토큰 (cctrace init 발급)

## 진단 순서

문제가 발생하면 아래 순서로 진단합니다:

### 1. 서버 상태 확인
```bash
docker compose ps
docker compose logs cctraced --tail=50
curl http://localhost:8080/api/health
```

### 2. DB 상태 확인
```bash
docker compose logs timescaledb --tail=20
nc -zv localhost 5432
```

### 3. CLI 프로파일 확인
```bash
cctrace status
cctrace config list
```

### 4. 환경 변수 확인
```bash
echo $OTEL_EXPORTER_OTLP_ENDPOINT
echo $OTEL_EXPORTER_OTLP_PROTOCOL
cat ~/.cctrace/env.sh
```

---

## 증상별 체크리스트

### 텔레메트리가 수집되지 않음

1. `OTEL_EXPORTER_OTLP_ENDPOINT` 환경 변수 설정 확인
2. `source ~/.cctrace/env.sh` 후 Claude Code 재시작
3. `curl http://localhost:8080/api/health` 에서 `log_queue_depth` 확인
4. `docker compose logs -f cctraced` 로 수신 로그 확인

### CLI가 서버에 연결 안 됨

1. `cctrace status` 로 endpoint 확인
2. `cctrace config get server.endpoint` — gRPC: `http://localhost:4317`, HTTP: `http://localhost:8080`
3. `docker compose ps` 로 cctraced 컨테이너 실행 여부 확인

### 세션 동기화 실패

1. `cctrace sync --dry-run` 으로 대상 확인
2. `cctrace config get server.sync_endpoint` — 동기화는 HTTP 엔드포인트 사용 (8080)
3. `cctrace config get server.auth_token` — API_KEY 설정 시 토큰 필요

### 대시보드 로그인 실패

1. `.env` 에 `JWT_SECRET` 설정 여부 확인
2. `docker compose logs cctraced` 에서 "dashboard authentication enabled" 메시지 확인
3. 비밀번호 분실 시: 관리자가 대시보드에서 비밀번호 재설정 가능

### 빌드 실패

```bash
go version          # 1.21+ 필요
make build          # dist/cctrace, dist/cctraced 생성
make lint           # 린트 오류 확인
```

---

## 핵심 파일 위치

| 파일 | 용도 |
|------|------|
| `deploy/docker-compose.yml` | 서비스 정의 (timescaledb, cctraced) |
| `.env` | 서버 환경 변수 (JWT_SECRET, API_KEY 등) |
| `~/.cctrace/profile.json` | CLI 기본 프로파일 |
| `~/.cctrace/env.sh` | OTEL 환경 변수 |
| `internal/api/api.go` | REST API 라우트 전체 목록 |
| `cmd/cctraced/main.go` | 서버 환경 변수 파싱 |
| `cmd/cctrace/config.go` | CLI config 명령어 및 설정 키 목록 |

## 설정 가능한 환경 변수 (서버)

| 변수 | 기본값 | 설명 |
|------|--------|------|
| `GRPC_PORT` | `4317` | gRPC 수신 포트 |
| `HTTP_PORT` | `8080` | HTTP API + 웹 UI 포트 |
| `HTTP_OTEL_PORT` | `4318` | HTTP OTLP 수신 포트 (내부) |
| `DATABASE_URL` | `postgres://cctrace:cctrace@localhost:5432/cctrace` | DB 연결 |
| `API_KEY` | (없음) | CLI sync 인증 키 |
| `JWT_SECRET` | (없음) | 대시보드 JWT 비밀키 |
| `EMAIL_ALIASES` | (없음) | 이메일 별칭 매핑 |
| `WAL_DIR` | `/data/buffer-wal` | WAL 버퍼 디렉터리 |

## CLI config 설정 키

```
user.name / user.email / user.team / user.id
server.endpoint / server.sync_endpoint / server.protocol / server.auth_token / server.read_token
options.sync_enabled / options.codex_sync_enabled / options.gjc_sync_enabled / options.omo_sync_enabled
options.redact_user_prompts / options.redact_tool_details
options.metrics_export_interval / options.logs_export_interval
options.collect_repository_prefixes / options.exclude_accounts / options.codex_dirs / options.gjc_dirs / options.omo_dirs
```

이 목록은 `cmd/cctrace/config.go` 의 `unknown key` 안내 문구와 같아야 한다.
둘이 갈라지면 사용자가 보는 쪽(이 문서)이 조용히 틀린다.

| 키 | 뜻 |
|---|---|
| `options.*_sync_enabled` | 에이전트별 세션 수집 on/off. **claude 는 `sync_enabled`** |
| `options.redact_*` | 업로드 전에 프롬프트·툴 내용을 지운다. 기본 `false`(=수집) |
| `options.collect_repository_prefixes` | 지정한 경로 접두사의 저장소만 수집. 비면 전체 |
| `options.exclude_accounts` | 지정한 결제 계정 ID(`provider:account_id` 가능)의 세션 레코드를 전송하지 않음 |
| `options.codex_dirs` / `gjc_dirs` / `omo_dirs` | 기본 위치 외 세션 디렉터리 추가 |

## 명명된 프로파일 (Named Profile)

여러 서버를 운영할 때 사용합니다:

```bash
cctrace init --profile work
cctrace status --profile work
cctrace sync --profile work
cctrace config set server.endpoint http://remote:4317 --profile work
```

## 테스트 실행

```bash
make test-unit          # Docker 불필요
make test-integration   # Docker 필요 (Ryuk 리퍼가 컨테이너 자동 회수)
make coverage           # 커버리지 리포트 → coverage.html
```

## 자주 쓰는 진단 명령어

```bash
# 전체 서비스 재시작
docker compose down && docker compose up -d

# cctraced만 재빌드 후 재시작
docker compose up -d --build cctraced

# DB 초기화 (데이터 전체 삭제)
docker compose down -v && docker compose up -d

# 실시간 로그
docker compose logs -f

# CLI 완전 제거
cctrace uninstall
```
