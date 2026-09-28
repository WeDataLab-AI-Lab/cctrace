# 호스팅 가이드 (온프레미스 / 클라우드)

cctraced를 어디에 올릴지 고르기 위한 가이드입니다. 설치·운영 절차 자체는
[guide-installation-and-usage.md](./guide-installation-and-usage.md)를 따르고,
이 문서는 "어떤 인프라를 고를 것인가"만 다룹니다.

---

## 1. 아키텍처가 요구하는 것

호스팅 옵션을 고르기 전에 이 두 가지를 먼저 이해해야 합니다.

1. **DB가 일반 Postgres가 아니다.** `docker/timescaledb-pgmq`에서 빌드하는
   커스텀 이미지로, TimescaleDB(hypertable·압축 정책)와 pgmq(큐) 확장이
   같이 들어갑니다 (`internal/store/migrations.go`, `internal/queue/queue.go`).
   서버 부팅 시 `Migrate()`가 `CREATE EXTENSION timescaledb`를, 이어서
   `CreateQueues()`가 `CREATE EXTENSION pgmq`를 실행하고, 둘 중 하나라도
   실패하면 `log.Fatalf(...)`로 **부팅 자체가 안 됩니다** (`cmd/cctraced/main.go`,
   `internal/store/postgres.go`의 `Migrate`, `internal/queue/queue.go`의 `CreateQueues`).
2. **gRPC가 상시 리스닝 포트다.** OTEL 수신(4317)은 요청-응답형 서버리스가
   아니라 에이전트가 아무 때나 연결하는 상시 TCP 서버입니다. 콜드스타트가
   있는 플랫폼에서는 그 사이 텔레메트리를 놓칩니다.

이 두 조건 때문에 관리형 DB(Cloud SQL, RDS, Supabase 등)와 서버리스/PaaS
무료 티어(Render Free 등)는 **애초에 후보가 아닙니다.** 관리형 Postgres는
전부 확장 허용 목록(allowlist) 방식이고 timescaledb는 그 목록에 없습니다.

| 서비스 | timescaledb | pgmq | 결론 |
|---|---|---|---|
| Supabase | 미지원 (2022년 제거) | 지원 | timescaledb 없어서 불가 |
| Render Postgres | 미지원 | 미지원 | 불가 |
| AWS RDS / Aurora PostgreSQL | 미지원 | 미지원 | 불가 |
| GCP Cloud SQL / AlloyDB | 미지원 | 미지원 | 불가 |
| Timescale Cloud | 지원 | 미확인 (별도 검증 필요) | 조합 미확정, 실사 없이 권장 안 함 |

**결론: 컨테이너 런타임을 직접 통제하는 인스턴스(VM) 위에 `deploy/docker-compose.yml`을
그대로 올리는 방식만 실질적으로 성립합니다.** 아래 온프레미스/클라우드 옵션 모두
이 전제를 따릅니다.

---

## 2. 공통 요구사항 (모든 배포 방식 공통)

| 항목 | 값 |
|---|---|
| CPU / RAM | 최소 2 vCPU / 4GB. 서버에서 직접 `docker build`(Next.js + Go)까지 할 경우 4 vCPU / 8GB 권장 |
| 디스크 | 40GB 이상 (TimescaleDB 압축 정책으로 90일 보존 기준 10인 조직은 여유 있음) |
| 포트 (인바운드) | 4317 (gRPC), 4318 (OTLP/HTTP), 8080 (REST+대시보드). 8080은 compose 기본값이 `HTTP_BIND=127.0.0.1`이라 그대로는 외부에서 닿지 않음 — TLS 오버레이(8443)를 쓰거나 `HTTP_BIND=0.0.0.0`으로 바꿔야 열림. DB는 `127.0.0.1:${DB_PORT}` 전용(기본 5432)으로 외부 노출 금지. 호스트에 PostgreSQL이 이미 5432를 쓰고 있으면 `DB_PORT`를 바꿈 (cctraced는 compose 내부망으로 접속하므로 영향 없음) |
| OS | Docker + Docker Compose가 도는 리눅스 (Ubuntu 22.04/24.04 권장) |
| 고정 주소 | 클라이언트(`cctrace init`)가 가리킬 안정적인 IP 또는 도메인 |

설치 명령 자체는 §1의 링크(guide-installation-and-usage.md)를 그대로 따라가면
됩니다. 아래는 "그 명령을 어느 인프라 위에서 실행하는가"의 차이만 다룹니다.

---

## 3. 온프레미스

이미 사내에 서버실/랙이 있고 외부 노출용 공인 IP를 확보할 수 있는 경우.

### 적합한 상황

- 대화 내용(세션 로그)이 사외로 절대 나가면 안 되는 보안 요건이 있을 때
- 이미 놀고 있는 물리 서버나 사내 하이퍼바이저(Proxmox/ESXi)가 있을 때
- 외부 클라우드 계정을 새로 만들기 부담스러운 조직

### 체크리스트

- [ ] 방화벽/공유기에서 4317, 4318, 8080 인바운드 포트포워딩 (8080은 §2의 `HTTP_BIND` 조건 참고, 5432는 열지 않음)
- [ ] 사내 DNS 또는 `/etc/hosts`로 팀원들이 접속할 안정적 호스트명 확보
- [ ] UPS 또는 정전 대응 (DB가 로컬 디스크에 있으므로 비정상 종료 시 WAL 재생 확인 필요 — §5 데이터 흐름/트러블슈팅은 설치 가이드 참조)
- [ ] 디스크 RAID 또는 별도 백업 대상(NAS 등)으로 `tsdb_data` 볼륨 정기 백업
- [ ] 사내망 밖에서 접속해야 하는 재택 인원이 있다면 VPN 경유로만 노출 (8080을 공인망에 직접 열지 않는 편이 안전)

### 단점

- 하드웨어 장애·전원 이중화를 직접 책임져야 함
- 사외 원격 근무자 접근에는 VPN 같은 별도 계층이 필요
- 재해복구(DR)를 별도로 설계해야 함 (클라우드는 스냅샷/리전 복제가 기본 제공)

---

## 4. 클라우드 호스팅

### 4-1. VPS (권장 — Hetzner 등)

가장 저렴하고 이 프로젝트의 compose 구조와 마찰이 가장 적은 방식입니다.

| 항목 | 스펙 | 월 비용 (약, EUR) |
|---|---|---|
| CX22 (이미지 CI 빌드, 서버는 실행만) | 2 vCPU / 4GB / 40GB SSD | €3.79 |
| CX32 (서버에서 직접 빌드까지, 권장) | 4 vCPU / 8GB / 80GB SSD | €6.80 |
| 추가 공인 IPv4 | 고정 IP 1개 | €0.50 |
| 자동 백업 (옵션) | 인스턴스가의 20% | €0.76~1.36 |
| 트래픽 | 20TB/월 포함 | €0 (10인 조직 규모론 초과 거의 없음) |

**예상 합계**
- 최소 구성: 약 €4.3/월
- 권장 구성(백업 포함): 약 €8~9/월

리전은 뉘른베르크/팔켄슈타인(독일) 또는 헬싱키(핀란드). 한국에서 대시보드
조회 레이턴시는 150~250ms대로, 실시간 워터폴(`cctrace live`)이 아닌 일반
대시보드 조회 용도로는 문제되지 않습니다.

Vultr, DigitalOcean 등 다른 VPS도 동일한 방식(Docker + Compose 직접 실행)이
그대로 적용됩니다. Hetzner를 우선 꼽는 이유는 가격 대비 스펙이 가장 좋기
때문이고, 구조상 락인은 없습니다.

### 4-2. GCP Compute Engine

이미 사내에 GCP 조직/VPC/IAM이 있어서 그 안에 두고 싶은 경우.

| 인스턴스 | 스펙 | 월 비용 (약) |
|---|---|---|
| `e2-small` | 2 vCPU / 2GB | 부족 (빌드 시 OOM 가능) |
| `e2-medium` | 2 vCPU / 4GB | ~$27 |
| `e2-standard-2` | 2 vCPU / 8GB | ~$49 (여유 있게 갈 경우) |

- VPC 방화벽 규칙으로 4317/4318/8080 인바운드 허용 (8080은 §2의 `HTTP_BIND` 조건 참고), 5432는 열지 않음
- 정적 외부 IP 예약 (Compute Engine 콘솔 → IP addresses)
- Persistent Disk 스냅샷 스케줄로 백업
- Cloud SQL/AlloyDB로 DB를 분리하려는 시도는 §1 표 때문에 불가 — DB 컨테이너도 같은 VM에 둡니다

### 4-3. AWS EC2

| 인스턴스 | 스펙 | 월 비용 (약, 온디맨드) |
|---|---|---|
| t3.small | 2 vCPU / 2GB | 부족 |
| t3.medium | 2 vCPU / 4GB | ~$30 |
| t3.large | 2 vCPU / 8GB | ~$61 (여유 있게 갈 경우) |

- Security Group으로 동일 포트 오픈
- Elastic IP 연결 (고정 주소)
- EBS gp3 볼륨 + AWS DLM(Data Lifecycle Manager)으로 자동 스냅샷
- RDS로 DB를 옮기려는 시도는 §1 표 때문에 불가

### 4-4. 어느 쪽을 고를지

- 별도 인프라 요건(감사 로그, VPC 피어링, 사내 SSO 연동)이 없다면 → **Hetzner류 VPS**가 비용 대비 압도적으로 유리
- 이미 GCP/AWS 조직 계정과 네트워크가 있고 그 안에서 감사·모니터링 체계를 통일해야 한다면 → 해당 클라우드의 VM (Compute Engine 또는 EC2)
- Supabase, Render 무료 티어, Cloud SQL/RDS 관리형 DB → 전부 부적합 (§1 참조)

---

## 5. 공통 운영 체크리스트

| 항목 | 온프레미스 | 클라우드 VM 공통 |
|---|---|---|
| TLS | 리버스 프록시(Caddy/nginx)를 8080 앞단에 추가 | 동일 |
| 백업 | NAS/외부 스토리지로 `tsdb_data` 정기 백업 | 스냅샷 스케줄(Hetzner Snapshot / GCE Snapshot / AWS DLM) |
| DB 포트 노출 | 금지 (compose가 호스트 포트 `DB_PORT`를 항상 127.0.0.1에만 바인딩) | 동일 |
| 고정 주소 | 사내 DNS 또는 고정 공인 IP | 정적 IP 예약 (Elastic IP / GCP 고정 외부 IP / Hetzner Floating IP) |
| `JWT_SECRET` | 48바이트 이상 무작위 문자열 | 동일 (guide-installation-and-usage.md §9) |
| 원격 접근 | VPN 경유 권장 | HTTPS로 직접 노출 가능(TLS 필수) |

세부 설치 명령, 환경변수, 사용자 온보딩, 트러블슈팅은 전부
[guide-installation-and-usage.md](./guide-installation-and-usage.md)를 따릅니다.
이 문서는 인프라 선택 단계에서만 참조하세요.
