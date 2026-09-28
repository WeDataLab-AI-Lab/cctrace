# 서버 운영

로그 위치, DB 백업과 복원, 업그레이드, 데이터 보존 기간.

모든 명령은 저장소 루트에서 실행하고 [설치](install.md)와 같은 compose 인자를 쓴다.

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml <command>
```

HTTPS 오버레이를 쓴다면 기본 파일 뒤에 `-f deploy/docker-compose.tls.yml`을 추가한다.

## 로그 {#logs}

| 대상 | 위치 |
|------|------|
| 서버 로그(시작, 마이그레이션, 설정 토큰, 경고) | `docker compose ... logs cctraced` |
| `/api/`·`/downloads/` 요청의 HTTP 접근 로그 | `docker compose ... logs cctraced`, 그리고 호스트의 `${LOGS_DIR}/access.log`에 추가 기록 |
| DB 로그 | `docker compose ... logs timescaledb` |

`LOGS_DIR`은 Docker 볼륨이 아니라 호스트 디렉터리라서 컨테이너를 지워도 접근 로그가 남는다. `cctraced`가 파일을 열지 못하면 `access log file open failed`를 남기고 접근 로그를 표준 출력에만 쓴다.

알아 둘 로그 줄:

| 로그 | 의미 |
|------|------|
| `[cctraced] running database migrations` | 시작 중 스키마 마이그레이션 적용 |
| `[cctraced] database ready` | 마이그레이션 완료 |
| `[cctraced] initial administrator setup token: ...` | 아직 사용자 없음. [첫 관리자](../dashboard/first-admin.md) 참조 |
| `[cctraced] notice: session_records (conversation content) has no retention policy ...` | 대화 내용 보존 기간이 정해지지 않음. [데이터 보존](#data-retention) 참조 |

## 백업과 복원 {#backup-and-restore}

DB는 Docker 볼륨 `tsdb_data`에 있다. 수집한 텔레메트리와 대화 내용이 모두 여기에 있다.

!!! warning "`tsdb_data`를 지우면 수집 데이터 전체 삭제"
    `docker compose down -v`는 볼륨까지 지운다. 처음부터 다시 시작할 생각이 아니라면 `-v` 없이 `down`을 쓴다.

### 백업 {#back-up}

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml \
    exec -T timescaledb sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" exec pg_dump -U cctrace -d cctrace' | gzip > cctrace-backup.sql.gz
```

이 릴리스로 새로 만든 DB 볼륨은 컨테이너 안에서도 비밀번호 없는 접속을 받지 않으므로, `psql`·`pg_dump`는 모두 컨테이너의 `POSTGRES_PASSWORD`를 넘긴다. cron으로 돌리는 백업 작업도 마찬가지다.

`pg_dump`가 TimescaleDB의 `continuous_agg` 카탈로그 테이블의 순환 외래 키 경고를 출력할 수 있으나, 덤프는 끝까지 진행된다.

### 복원 {#restore}

`cctraced`가 붙기 전의 비어 있는 새 DB 볼륨에 복원한다.

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml down -v
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d timescaledb
$ gunzip -c cctrace-backup.sql.gz | docker compose --env-file deploy/.env \
    -f deploy/docker-compose.yml exec -T timescaledb sh -c 'PGPASSWORD="$POSTGRES_PASSWORD" exec psql -U cctrace -d cctrace'
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
```

복원 전에 `ps`에서 `timescaledb`가 `(healthy)`인지 확인한다. 첫 명령은 현재 DB를 지우므로, 백업 파일이 보존할 데이터일 때만 실행한다.

`wal_data` 볼륨은 수집 큐의 임시 버퍼다. 백업 대상은 아니지만 지우면 수신했으나 아직 DB에 쓰지 않은 텔레메트리를 잃을 수 있다.

## 업그레이드 {#upgrade}

1. 백업
2. 클론을 갱신하고 서버 env 파일이 쓰는 태그로 두 이미지 재빌드:

    ```console
    $ git pull
    $ docker build -t cctrace/timescaledb-pgmq:latest docker/timescaledb-pgmq/
    $ docker build -f deploy/Dockerfile --build-arg UPDATE_SIGNING=optional \
        -t cctrace/cctraced:latest .
    ```

3. 컨테이너 재생성과 상태 확인:

    ```console
    $ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d
    $ docker compose --env-file deploy/.env -f deploy/docker-compose.yml ps
    ```

`cctraced`는 시작할 때 포트를 열기 전에 스키마 마이그레이션을 적용한다. 별도 마이그레이션 명령은 없다. 마이그레이션 중에는 컨테이너가 `Up`이지만 `unhealthy`로 표시되고 응답하지 않는다. 무엇을 기다리는지는 로그에 나온다.

이전 이미지를 남겨 두려면 새 이미지를 새 태그로 빌드하고 서버 env 파일의 `IMAGE_TAG`(새 DB 이미지라면 `DB_IMAGE_TAG`도)를 바꾼다. 마이그레이션은 정방향만 있다. 이전 버전과 그 데이터로 돌아가려면 업그레이드 전에 받은 백업을 복원한다.

DB의 `shm_size`처럼 컨테이너 생성 시점에만 적용되는 compose 설정도 있다. `deploy/docker-compose.yml`에서 그런 값이 바뀌었다면 해당 컨테이너를 명시적으로 다시 만든다.

```console
$ docker compose --env-file deploy/.env -f deploy/docker-compose.yml up -d --force-recreate timescaledb
```

## 데이터 보존 {#data-retention}

새 DB에 스키마가 지정하는 정책:

| 데이터 | 보존 기간 | 압축 |
|--------|-----------|------|
| OpenTelemetry 이벤트·메트릭 | 90일 | 30일 후 |
| 세션 레코드(대화 내용) | 없음: 무기한 보존 | 없음 |

보존 기간은 대시보드의 **Admin > Storage**에서 바꾼다. 이 탭은 테이블별 보존·압축 설정, 크기, 데이터 볼륨의 남은 공간을 보여 준다.

`cctraced`는 시작할 때 `OTEL_RETENTION_DAYS`와 `SESSION_RETENTION_DAYS`도 읽는다. 여기에 지정한 값이 대시보드 설정보다 우선하고 대시보드에서 해당 항목은 잠긴다. 배포된 compose 파일은 이 두 변수를 컨테이너에 전달하지 않는다. [설정](configuration.md#variables-the-compose-file-does-not-pass) 참조.

보존 기간은 이미 저장된 데이터에도 적용된다. 기간을 줄이면 다음 정책 실행 때 더 오래된 레코드가 삭제된다.
