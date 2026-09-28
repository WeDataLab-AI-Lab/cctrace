.PHONY: all build web-dist-placeholder web-dist-check test-scope-check build-client print-update-public-key build-linux build-windows run docker-up docker-down docker-logs install test test-unit test-integration coverage lint clean spike-codex replay-codex gate-phase1 ai-live

# All targets
all: build

# Binaries
VERSION := $(shell git describe --tags --always 2>/dev/null || echo "dev")
# The Ed25519 public key that verifies update manifests. Public by definition —
# it ships inside every released binary. Kept in-tree so locally built clients
# share the release trust root instead of failing every self-update, and so the
# repository states which key a release is expected to be signed with.
UPDATE_PUBLIC_KEY ?= $(shell cat deploy/update-public-key.txt 2>/dev/null)
# Default endpoints offered by `cctrace init`. Deployment-specific, so the file
# is gitignored and the leading `-` keeps a build without it working: the
# variables stay empty and init prompts for both values instead. Copy
# deploy/local-defaults.env.example to create one.
-include deploy/local-defaults.env
LDFLAGS := -ldflags "-X main.version=$(VERSION) -X main.updatePublicKeyBase64=$(UPDATE_PUBLIC_KEY) -X main.defaultSyncEndpoint=$(DEFAULT_SYNC_ENDPOINT) -X main.defaultOTELEndpoint=$(DEFAULT_OTEL_ENDPOINT)"

# The version above already comes from `git describe`, so Go's own VCS stamping
# adds nothing — and it hard-fails with exit 128 inside a git worktree, which
# makes `make build` unusable there. tests/integration_test.go disables it for
# the same reason.
BUILDFLAGS := -buildvcs=false

# cctraced embeds internal/web/dist. The placeholder that test/lint targets
# create satisfies go:embed but contains no dashboard, so without this guard a
# build run after `make test-unit` links a server that serves an empty page —
# and it succeeds silently.
#
# Mirrors web.hasDashboard: an emptied or truncated entry document beside a
# leftover asset tree is not a dashboard either, so require the document to
# actually load the tree.
web-dist-check:
	@{ test -d internal/web/dist/_next && grep -q '/_next/' internal/web/dist/index.html 2>/dev/null; } || { \
		echo "ERROR: internal/web/dist holds no web build (placeholder or incomplete)."; \
		echo "       Run ./scripts/build-web.sh before make build."; \
		exit 1; }

build: web-dist-check
	@if [ -z "$(UPDATE_PUBLIC_KEY)" ]; then \
		echo "[WARN] UPDATE_PUBLIC_KEY is empty — built clients cannot self-update. Internal builds need deploy/update-public-key.txt."; \
	fi
	mkdir -p dist
	go build $(BUILDFLAGS) $(LDFLAGS) -o dist/cctraced ./cmd/cctraced
	GOOS=darwin GOARCH=amd64 go build $(BUILDFLAGS) $(LDFLAGS) -o dist/cctrace-amd64 ./cmd/cctrace
	GOOS=darwin GOARCH=arm64 go build $(BUILDFLAGS) $(LDFLAGS) -o dist/cctrace-arm64 ./cmd/cctrace
	lipo -create -output dist/cctrace dist/cctrace-amd64 dist/cctrace-arm64
	rm dist/cctrace-amd64 dist/cctrace-arm64

# 현재 플랫폼용 클라이언트 하나. `build` 는 lipo 로 darwin 유니버설을 만들므로
# macOS 밖에서 쓸 수 없고, 문서가 -ldflags 를 손으로 베껴 적으면 LDFLAGS 가 바뀔 때
# 조용히 어긋난다. 신뢰 루트 주입을 이 한 곳에 둔다.
CLIENT_OUT ?= dist/cctrace
build-client:
	@mkdir -p "$(dir $(CLIENT_OUT))"
	go build $(BUILDFLAGS) $(LDFLAGS) -o "$(CLIENT_OUT)" ./cmd/cctrace

# 빌드된 바이너리에 신뢰 루트가 박혔는지 확인하려면 그 값을 알아야 한다. 키 파일을
# 직접 읽는 대신 이 타깃을 쓰면, 파일이 없는 체크아웃에서도 빈 문자열이 나온다.
print-update-public-key:
	@echo "$(UPDATE_PUBLIC_KEY)"

build-linux:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(BUILDFLAGS) $(LDFLAGS) -o dist/cctrace-linux ./cmd/cctrace

build-windows:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 go build $(BUILDFLAGS) $(LDFLAGS) -o dist/cctrace.exe ./cmd/cctrace

# Backend-only dev loop: no web assets are built here, so opt out of the
# empty-dashboard guard rather than force a Next.js build for API work.
run:
	docker compose up -d db
	set -a && . ./.env && set +a && CCTRACE_ALLOW_EMPTY_DASHBOARD=1 go run ./cmd/cctraced

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f cctraced

install:
	go install $(LDFLAGS) ./cmd/cctrace
	@if [ -d "$$HOME/bin" ]; then cp "$$(go env GOPATH)/bin/cctrace" "$$HOME/bin/cctrace"; fi

# Unit tests only (no Docker required)
# Embedded web assets placeholder: internal/web/embed.go requires all:dist to
# match at least one file, but dist/ is gitignored and only exists after a web
# build. Same trick CI uses, so fresh checkouts can build/test/lint Go alone.
web-dist-placeholder:
	@mkdir -p internal/web/dist && touch internal/web/dist/.gitkeep

# scripts/ 는 미러 게이트 테스트만 들고 있고, 그 파일들은 반출되지 않는다.
# 목록에 그대로 두면 미러에서 "matched no packages" 로 exit 1 이 되므로
# 테스트 파일이 있을 때만 인자에 들어가게 한다. 문자열 './scripts/...' 는
# 아래 줄에 그대로 남아 있어야 test-scope-check 의 파서가 계속 인식한다.
# -race 는 붙여 둔다. 2026-09-10 에 internal/store 로 들어온 데이터 레이스가
# 이 게이트를 두 번 통과하고 원격 푸시까지 갔다 (#615). 실측 비용은 unit +24s,
# integration +4s 로 pre-pr 전체의 4% 미만이다. 패키지 목록을 따로 관리하는 안은
# 쓰지 않는다 -- 그 레이스가 난 곳은 아무도 동시성 패키지로 여기지 않던 DB 계층이고,
# 목록은 새로 추가되는 것을 모른다.
# -timeout 은 패키지별 상한이고, 느린 테스트가 아니라 멈춘 테스트를 잡는 장치다.
# 규칙: 상한 >= 그 호출에서 가장 느린 패키지의 조용한 머신 시간 x 5. 경합이
# 걸리면 패키지 시간이 5-7배로 늘기 때문이다 (2026-09-22/23 실측: internal/api
# 49s -> load 162 에서 277s, internal/syncer 18s -> load 204 에서 112s). 같은 날
# 게이트 실패는 테스트 실패가 아니라 전부 이 상한 초과였다.
# 조용한 머신(load ~5.6, pre-pr 233s) 실측과 상한:
#   test-unit        internal/api   49s x 5 = 245s -> 300s (이전 180s 는 3.7배)
#   test-integration internal/store 45s x 5 = 225s -> 300s (6.7배, 규칙 충족)
#   scripts          scripts        41s x 5 = 205s -> 600s (15배, 규칙 충족)
# scripts 는 바이너리를 빌드하고 docker/git 서브프로세스를 부른다. 나머지 패키지와
# 같은 go test 호출에 묶여 매번 다른 테스트에서 상한에 걸렸으므로 별도 go test 로
# 떼어 두었다. 규칙이 깨지면 상한을 올리기 전에 그 패키지가 왜 느려졌는지 본다.
# Ryuk -- testcontainers' reaper -- prunes a session's containers 10 seconds
# after its last client disconnects, and every package in one `go test` run
# shares a single session id. Nothing holds a connection open between the
# moments a package creates a container, so a quiet gap longer than that window
# makes Ryuk delete containers the run is still using. The store package, which
# holds one container for its whole 100-second run, is the one that loses:
# `docker rm -f` SIGKILLs postgres and every later test reports
# "terminating connection due to unexpected postmaster exit", which reads like a
# database crash rather than a reaper doing its job.
#
# Observed directly in Ryuk's own log: clients=0 at T, "prune check" at T+10s,
# 264 failures after it. The window is a race, so this surfaces as a flake that
# tracks how long the quiet gap happens to be -- adding two tables to
# truncateTables was enough to tip it.
#
# Widening the window does not weaken cleanup: Ryuk still reaps at process exit,
# and its own shutdown_timeout is already 10m.
export TESTCONTAINERS_RYUK_RECONNECTION_TIMEOUT ?= 10m

test-unit: web-dist-placeholder test-scope-check
	go test \
		./internal/activitylabel/... \
		./internal/aireport/... \
		./internal/airuntime/... \
		./internal/api/... \
		./internal/atomicfile/... \
		./internal/auth/... \
		./internal/chatruntime/... \
		./internal/claudeauth/... \
		./internal/clauderuntime/... \
		./internal/codexappserver/... \
		./internal/codexauth/... \
		./internal/commandclass/... \
		./internal/codexconfig/... \
		./internal/codexlog/... \
		./internal/codexrates/... \
		./internal/codexsyncer/... \
		./internal/containertest/... \
		./internal/envgen/... \
		./internal/gitctx/... \
		./internal/gjclog/... \
		./internal/gjcsyncer/... \
		./internal/jsonlscan/... \
		./internal/ingestblock/... \
		./internal/insights/... \
		./internal/omolog/... \
		./internal/omosyncer/... \
		./internal/openairuntime/... \
		./internal/openinsights/... \
		./internal/profile/... \
		./internal/projecthash/... \
		./internal/projectrule/... \
		./internal/sessionlog/... \
		./internal/sessionview/... \
		./internal/synclog/... \
		./internal/syncer/... \
		./internal/usage/... \
		./internal/web/... \
		./internal/worker/... \
		./packages/usage-insights/... \
		./cmd/cctrace/... \
		./cmd/cctraced/... \
		-race -count=1 -timeout 300s
	$(if $(wildcard scripts/*_test.go),go test ./scripts/... -race -count=1 -timeout 600s)

# Packages intentionally outside the test targets. Empty on purpose: scripts/
# used to sit here as "dev-only helpers", but it now also holds the open-source
# mirror leak gate, whose whole failure mode is passing quietly. That guard has
# to run on the pre-commit path, not only under `make test`.
TEST_SCOPE_EXEMPT :=

# Guard against scope drift: a package whose tests are in neither test-unit nor
# test-integration only ever runs under `make test`, so it silently drops out of
# the pre-commit path. Lists any such package; run after adding a package.
#
# Module path is derived via `go list -m`, not hardcoded: a hardcoded path
# silently stops matching after a module rename, `$$d` stays an import path
# instead of a directory, every package fails the `[ -d "$$d" ]` check below,
# and the guard reports "OK" while checking nothing. The `checked` counter
# below is the actual defense against a repeat of that failure mode.
test-scope-check: web-dist-placeholder
	@set -e; \
	listed=$$(go list -buildvcs=false ./...) || { \
		echo "ERROR: go list failed; cannot verify test scope."; exit 1; }; \
	modpath=$$(go list -m) || { echo "ERROR: go list -m failed; cannot verify test scope."; exit 1; }; \
	pkgs=$$(printf '%s\n' "$$listed" | sed "s|^$${modpath}/||" | grep -v '^web/node_modules' || true); \
	[ -n "$$pkgs" ] || { echo "ERROR: no packages resolved; scope check is not meaningful."; exit 1; }; \
	scoped=$$(sed -n '/^test-unit:/,/^$$/p;/^test-integration:/,/^$$/p' Makefile \
		| sed 's|[[:space:]]#.*$$||' \
		| grep -v '^[[:space:]]*#' \
		| sed -n 's|.*\./\([a-zA-Z0-9_/-]*\)/\.\.\..*|\1|p' | tr '\n' ' '); \
	[ -n "$$scoped" ] || { echo "ERROR: could not parse test targets; scope check is not meaningful."; exit 1; }; \
	missing=""; \
	checked=0; \
	for d in $$pkgs; do \
		[ -d "$$d" ] || continue; \
		checked=$$((checked+1)); \
		ls "$$d"/*_test.go >/dev/null 2>&1 || continue; \
		case " $(TEST_SCOPE_EXEMPT) " in *" $$d "*) continue;; esac; \
		covered=""; \
		for s in $$scoped; do \
			case "$$d" in $$s|$$s/*) covered=1; break;; esac; \
		done; \
		[ -z "$$covered" ] || continue; \
		missing="$$missing $$d"; \
	done; \
	[ "$$checked" -gt 0 ] || { echo "ERROR: no package directories resolved; scope check is not meaningful."; exit 1; }; \
	if [ -n "$$missing" ]; then \
		echo "ERROR: packages with tests missing from test-unit/test-integration:"; \
		for p in $$missing; do echo "  $$p"; done; \
		echo "Add them to a make target, or to TEST_SCOPE_EXEMPT with a reason."; \
		exit 1; \
	fi; \
	echo "test scope OK"

# Integration tests (requires Docker / Colima)
test-integration: web-dist-placeholder
	CCTRACE_REQUIRE_CONTAINERS=1 go test \
		./internal/buffer/... \
		./internal/otelrecv/... \
		./internal/queue/... \
		./internal/store/... \
		./tests/... \
		-race -count=1 -timeout 300s

# All tests
test: web-dist-placeholder
	CCTRACE_REQUIRE_CONTAINERS=1 go test ./... -count=1 -timeout 300s

# Spike: analyze real Codex JSONL wire format (requires local ~/.codex/sessions/)
spike-codex:
	go test -tags spike ./internal/codexlog/ -v -run TestSpikeParseCodexJSONL 2>&1 | tee /tmp/codex-spike.txt
	@echo "결과 저장: /tmp/codex-spike.txt"

replay-codex:
	go test -tags codex_replay ./internal/codexlog/ -v -run TestReplayLocalCodexFiles

# 실호출 e2e 테스트: 실제 OpenAI와 Anthropic API를 부른다.
# 키: CCTRACE_OPENAI_LIVE_KEY·CCTRACE_ANTHROPIC_LIVE_KEY, 없으면 공급자 표준
# 이름인 OPENAI_API_KEY·ANTHROPIC_API_KEY 를 쓴다.
AI_LIVE_KEY_FILE ?= $(HOME)/Documents/GitHub/claude-code-trace/.env.ai
ai-live: web-dist-placeholder
	@if [ ! -f "$(AI_LIVE_KEY_FILE)" ]; then \
		echo "ERROR: $(AI_LIVE_KEY_FILE) not found"; \
		echo "Create it with OPENAI_API_KEY and ANTHROPIC_API_KEY (or the CCTRACE_*_LIVE_KEY names)"; \
		exit 1; \
	fi
	set -a && . $(AI_LIVE_KEY_FILE) && set +a && \
	go test -tags aireportlive ./internal/aireport/... -v -run TestLive

gate-phase1:
	go test ./internal/codexlog/... -count=1 -timeout 60s
	go vet ./internal/codexlog/...

# Coverage report
coverage: web-dist-placeholder
	go test ./... -count=1 -timeout 300s \
		-coverprofile=coverage.out
	go tool cover -func=coverage.out | tail -1
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

# Lint
lint: web-dist-placeholder
	golangci-lint run ./...

clean:
	rm -rf dist/ internal/web/dist/ coverage.out coverage.html

# Internal dev-server targets (dev-admin/dev-up/dev-down/dev-logs/dev-sync) and
# the agent harness contract target (harness-check). The file is absent from the
# open-source mirror, so -include keeps its absence from failing the build.
-include deploy/Makefile.internal
