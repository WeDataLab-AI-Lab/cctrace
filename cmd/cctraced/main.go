package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"cctrace/internal/aireport"
	"cctrace/internal/airuntime"
	"cctrace/internal/api"
	"cctrace/internal/auth"
	"cctrace/internal/buffer"
	"cctrace/internal/chatruntime"
	"cctrace/internal/clauderuntime"
	"cctrace/internal/codexappserver"
	"cctrace/internal/codexrates"
	"cctrace/internal/emailalias"
	"cctrace/internal/ingestblock"
	"cctrace/internal/insights"
	"cctrace/internal/openairuntime"
	"cctrace/internal/otelrecv"
	"cctrace/internal/queue"
	"cctrace/internal/store"
	"cctrace/internal/web"
	"cctrace/internal/worker"

	"github.com/jackc/pgx/v5"
	collogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	colmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"
)

var version = "dev"

func cctraceDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(home, ".cctrace")
}

func pidFilePath() string {
	return filepath.Join(cctraceDir(), "cctraced.pid")
}

func writePIDFile() error {
	path := pidFilePath()
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0644)
}

// readPID reads a pid file. Atoi over the trimmed contents on purpose: it
// rejects "123junk", which the Sscanf("%d") the client uses would accept.
func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, fmt.Errorf("invalid pid file: %w", err)
	}
	return pid, nil
}

// removePIDFileIfCurrent removes the pid file only when it still points at the
// given pid, avoiding a race where a quick stop/start lets an old process delete
// the new daemon's pid file.
func removePIDFileIfCurrent(path string, pid int) error {
	current, err := readPID(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || current != pid {
		return nil
	}
	return os.Remove(path)
}

// signalDaemonByPID asks the process to wind down. os.FindProcess never fails
// on unix, which is exactly why the caller must have established that the
// process exists before getting here -- the daemon lock is what does that.
func signalDaemonByPID(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return stopProcess(proc)
}

// stopObserveTimeout is not a bound on shutdown. The shutdown path is unbounded
// (GracefulStop, Shutdown(context.Background()), the drain, <-repairDone); this
// is the point at which --stop gives up watching and reports that the lock is
// still held.
const stopObserveTimeout = 35 * time.Second

var (
	probeDaemonLockFn    = probeDaemonLock
	waitLockFreeFn       = waitForDaemonLockFree
	acquireControlLockFn = acquireControlLock
	signalDaemonFn       = signalDaemonByPID
	removeFileFn         = os.Remove
)

func stopDaemon() error {
	return runStop(stopObserveTimeout)
}

// runStop terminates the daemon holding the daemon lock.
//
// The lock decides whether anything is signalled, not the recorded pid. A pid
// lives in a file, and a file outlives the process it describes: startup
// failures leave one behind (log.Fatalf skips every defer), and by the time
// anyone runs --stop the OS may have handed that number to something else.
// Signalling it then hits an unrelated process. So the pid is read only while
// the lock says its owner is alive, and only under the control lock, which is
// what keeps a daemon from being between "took the lock" and "wrote its pid".
//
// Residual risk: a daemon observed as holding the lock can die and have its pid
// reused before the signal lands. Closing that needs pidfd or a start-time
// comparison; the control lock only closes the startup window.
func runStop(timeout time.Duration) error {
	// Checked before the control lock so that --stop on a machine that never ran
	// a daemon creates neither ~/.cctrace nor a lock file. Everything past here
	// runs in a directory that already exists.
	if _, err := os.Stat(lockFilePath()); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check daemon lock: %w", err)
		}
		return reportNoLockFile()
	}

	ctl, err := acquireControlLockFn(daemonLockAcquireWait)
	if err != nil {
		return fmt.Errorf("check daemon lock: %w", err)
	}
	ctlHeld := true
	releaseControl := func() {
		if ctlHeld {
			ctlHeld = false
			_ = ctl.Unlock()
		}
	}
	defer releaseControl()

	fl, exists, err := probeDaemonLockFn()
	if err != nil {
		return fmt.Errorf("check daemon lock: %w", err)
	}
	if !exists {
		return reportNoLockFile()
	}
	if fl != nil {
		// The probe took the lock, which means no daemon holds it. Cleaning up
		// while still holding it is what keeps a daemon that starts right now
		// from having the pid it just wrote deleted underneath it.
		defer func() { _ = fl.Unlock() }()
		removeStalePIDFile()
		fmt.Println("  No running cctraced")
		return nil
	}

	pid, err := readPID(pidFilePath())
	if err != nil || pid <= 0 {
		// Held by a process this machine has no usable pid for. Signalling
		// something else on a guess is worse than saying so.
		return fmt.Errorf("a cctraced holds the daemon lock but its pid is unknown (%s); "+
			"stop it by hand", pidFilePath())
	}

	if err := signalDaemonFn(pid); err != nil {
		return fmt.Errorf("failed to stop cctraced (pid %d): %w", pid, err)
	}

	// Released before the wait: the daemon does not need the control lock to
	// exit, and holding it here would block the next startup for the whole wait.
	releaseControl()

	free, err := waitLockFreeFn(timeout)
	if err != nil {
		// Reported rather than assumed, and the pid file is left alone: it is the
		// only record of what is still running.
		return fmt.Errorf("signalled pid %d but the daemon lock is still held after %s: %w",
			pid, timeout, err)
	}
	if free == nil {
		// Nothing left to hold, so nothing guarantees the pid file still belongs
		// to the daemon that was just signalled rather than to one starting right
		// now. That daemon did stop, which is what was asked.
		fmt.Fprintf(os.Stderr, "  [!] daemon lock file %s disappeared while waiting; "+
			"pid file left in place\n", lockFilePath())
		fmt.Printf("  Stopped cctraced (pid %d)\n", pid)
		return nil
	}
	defer func() { _ = free.Unlock() }()
	removeStalePIDFile()
	fmt.Printf("  Stopped cctraced (pid %d)\n", pid)
	return nil
}

// reportNoLockFile answers for the two states with no lock file: nothing ever
// ran, or somebody deleted the lock. The second is unknowable from here -- a
// crash or a log.Fatalf leaves the lock file in place -- so nothing is signalled
// and nothing is removed.
func reportNoLockFile() error {
	if _, err := os.Stat(pidFilePath()); errors.Is(err, os.ErrNotExist) {
		fmt.Println("  No running cctraced")
		return nil
	}
	return fmt.Errorf("daemon lock file %s is missing but %s is present; "+
		"refusing to guess whether a cctraced is running", lockFilePath(), pidFilePath())
}

// removeStalePIDFile clears the pid file of a daemon that is no longer running.
//
// Only reached while the daemon lock is held by this command, so no daemon can
// be writing it. A failure does not change the outcome -- #449 saw a removal
// fail on Windows with every error discarded, and the log recorded that the file
// was still there and nothing about why.
func removeStalePIDFile() {
	if err := removeFileFn(pidFilePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(os.Stderr, "  [!] remove %s: %v\n", pidFilePath(), err)
	}
}

func main() {
	if handled, err := handleProjectIdentityRepairCommand(
		context.Background(),
		os.Args[1:],
		os.Getenv,
		os.Stdout,
		runProjectIdentityRepairDryRun,
		runProjectIdentityRepairApply,
	); handled {
		if err != nil {
			fmt.Fprintf(os.Stderr, "  [X] %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Answer the questions an operator can ask of a binary before any boot work:
	// what does it take, and which build is this. Both used to fall through to the
	// normal startup path and die on "JWT_SECRET is required".
	for _, arg := range os.Args[1:] {
		switch arg {
		case "--stop":
			if err := stopDaemon(); err != nil {
				fmt.Fprintf(os.Stderr, "  [X] %v\n", err)
				os.Exit(1)
			}
			return
		case "--help", "-h":
			fmt.Print(usageText())
			return
		case "--version", "-v":
			fmt.Println(versionText())
			return
		}
	}
	// Refuse to serve an empty dashboard. The placeholder that the test and lint
	// make targets leave in internal/web/dist satisfies go:embed, so a binary
	// built after `make test-unit` — or by CI, or by either Dockerfile, none of
	// which build the web assets — links and starts happily while serving
	// nothing. Failing here turns that into a loud deploy-time error instead of
	// a dashboard that is silently blank in production.
	//
	// Backend-only workflows (`make run`) opt out rather than build the assets.
	if !web.HasDashboard() && envOr("CCTRACE_ALLOW_EMPTY_DASHBOARD", "") != "1" {
		log.Fatal("embedded web dashboard is missing or incomplete " +
			"(internal/web/dist needs an index.html that loads the _next asset tree). " +
			"Run ./scripts/build-web.sh before building cctraced, " +
			"or set CCTRACE_ALLOW_EMPTY_DASHBOARD=1 to run the API without a dashboard.")
	}

	grpcPort := envOr("GRPC_PORT", "4317")
	httpPort := envOr("HTTP_PORT", "8080")
	allowedOrigins := strings.Split(envOr("CCTRACE_ALLOWED_ORIGINS", ""), ",")
	httpOtelPort := envOr("HTTP_OTEL_PORT", "4318")
	dsn := envOr("DATABASE_URL", "postgres://cctrace:cctrace@localhost:5432/cctrace?sslmode=disable")
	apiKey := os.Getenv("API_KEY") // empty = auth disabled (dev mode)
	jwtSecret := os.Getenv("JWT_SECRET")
	setupTokenEnv := os.Getenv("CCTRACE_SETUP_TOKEN")
	cookieSecure := os.Getenv("COOKIE_SECURE") == "1"
	if err := validateAuthConfiguration(jwtSecret, os.Getenv("CCTRACE_SECRETS_KEY")); err != nil {
		log.Fatal(err)
	}

	// Take the daemon lock before any slow startup work (PR #70) but after the
	// configuration checks above: those are cheap string and env reads, and an
	// operator who forgot JWT_SECRET should not be told "already running". From
	// here on the database connection, the migration and the servers are all
	// behind the lock.
	//
	// The lock, not the pid file, is what says a daemon is running, so the pid
	// is written only once the lock is held and never without it. The control
	// lock serializes that pair against --stop's "decide, read the pid, signal".
	ctl, err := acquireControlLock(daemonLockAcquireWait)
	switch {
	case errors.Is(err, errDaemonAlreadyRunning):
		// Contention, and only a startup or a --stop takes this lock, so losing
		// it is evidence that another cctraced is right here. Starting anyway
		// would mean starting without the daemon lock -- two daemons on one home,
		// which is what this block exists to prevent. The non-contention failure
		// below says nothing about other instances, which is why it is allowed to
		// continue where this one is not.
		fmt.Fprintf(os.Stderr,
			"  [X] another cctraced is starting or stopping right now (control lock %s); "+
				"retry in a moment or run 'cctraced --stop'.\n", controlLockFilePath())
		os.Exit(1)
	case err != nil:
		log.Printf("[cctraced] warning: could not take the control lock: %v. "+
			"This process starts unmanaged: 'cctraced --stop' cannot stop it and "+
			"nothing prevents a second instance.", err)
	default:
		fl, lockErr := acquireDaemonLock()
		switch {
		case errors.Is(lockErr, errDaemonAlreadyRunning):
			_ = ctl.Unlock()
			fmt.Fprintf(os.Stderr,
				"  [X] another cctraced already holds %s. Stop it with 'cctraced --stop'.\n",
				lockFilePath())
			os.Exit(1)
		case lockErr != nil:
			// Not contention (EACCES, EMFILE, ENOLCK and the like). Serving is
			// still better than not, but the pid file stays unwritten: a pid with
			// no lock behind it is exactly what --stop must not trust.
			_ = ctl.Unlock()
			log.Printf("[cctraced] warning: could not take the daemon lock: %v. "+
				"This process starts unmanaged: 'cctraced --stop' cannot stop it and "+
				"nothing prevents a second instance.", lockErr)
		default:
			// The closure keeps fl referenced for the whole of main. A bare
			// defer fl.Unlock() would not: *Flock holds an *os.File, whose
			// finalizer closes the fd, and closing the fd drops the lock.
			defer func() { _ = fl.Unlock() }()
			if err := writePIDFile(); err != nil {
				// Holding the lock while leaving the previous generation's pid in
				// place would make that stale number look authoritative to --stop.
				_ = fl.Unlock()
				_ = ctl.Unlock()
				log.Fatalf("[cctraced] could not write pid file: %v", err)
			}
			// Registered after the unlock defer so LIFO runs it first: the pid
			// file is removed while this process still holds the lock, so a
			// daemon starting right after cannot have its pid deleted here. The
			// pg.Close and cancel defers registered below run before both of
			// these, which is intended -- the lock stays held until the pool has
			// finished returning its connections.
			defer func() {
				if err := removePIDFileIfCurrent(pidFilePath(), os.Getpid()); err != nil {
					log.Printf("[cctraced] warning: could not remove pid file: %v", err)
				}
			}()
			_ = ctl.Unlock()
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// JWT authentication for dashboard
	jwtMgr, err := auth.NewJWTManager(jwtSecret)
	if err != nil {
		log.Fatalf("[cctraced] JWT setup failed: %v", err)
	}
	log.Println("[cctraced] dashboard authentication enabled (JWT_SECRET set)")

	// Connect to TimescaleDB/PostgreSQL
	pg, err := store.NewPgStore(ctx, dsn)
	if err != nil {
		log.Fatalf("[cctraced] database connection failed: %v", err)
	}
	defer pg.Close() //nolint:errcheck
	// Classification is server-only by design -- see the isolation note on
	// store.TaskClassifier. This is the one place a concrete implementation is
	// ever constructed.
	pg.SetTaskClassifier(insights.KeywordClassifier{})

	// Run table migrations. Announced because this is where boot can stall: the
	// DDL needs locks that a long-running query elsewhere can hold off, and
	// without this line the log just stops with no indication of what for.
	log.Println("[cctraced] running database migrations")
	if err := pg.Migrate(ctx); err != nil {
		log.Fatalf("[cctraced] migration failed: %v", err)
	}
	log.Println("[cctraced] database ready")

	var setupToken string
	setupCtx, setupCancel := context.WithTimeout(ctx, 10*time.Second)
	userCount, setupErr := pg.CountDashboardUsers(setupCtx)
	setupCancel()
	if setupErr != nil {
		log.Printf("[cctraced] setup disabled: user count failed: %v", setupErr)
	} else {
		var generated bool
		setupToken, generated, setupErr = resolveSetupToken(setupTokenEnv, userCount)
		if setupErr != nil {
			log.Printf("[cctraced] setup disabled: token generation failed: %v", setupErr)
		} else if generated {
			log.Printf("[cctraced] initial administrator setup token: %s", setupToken)
		}
	}

	// Retention reconcile at boot. Desired config is resolved durably: an env
	// override wins per axis, else a persisted /storage admin setting (so a UI
	// edit survives restart and is re-asserted after Migrate re-adds a default
	// policy), else nil (a plain deploy with neither never changes any policy).
	// Non-fatal and time-bounded so a slow DB cannot stall startup.
	retCtx, retCancel := context.WithTimeout(ctx, 30*time.Second)
	retCfg, resolveErr := pg.EffectiveRetentionConfig(retCtx, store.RetentionConfig{
		OtelDays:    envIntOpt("OTEL_RETENTION_DAYS"),
		SessionDays: envIntOpt("SESSION_RETENTION_DAYS"),
	})
	if resolveErr != nil {
		log.Printf("[cctraced] retention config resolve failed (non-fatal): %v", resolveErr)
	} else if err := pg.ReconcileRetention(retCtx, retCfg); err != nil {
		log.Printf("[cctraced] retention reconcile failed (non-fatal): %v", err)
	} else if retCfg.OtelDays != nil || retCfg.SessionDays != nil {
		log.Printf("[audit] action=set_retention actor=boot otel_days=%s session_days=%s",
			ptrDaysStr(retCfg.OtelDays), ptrDaysStr(retCfg.SessionDays))
	}
	// Say out loud when nobody has decided how long conversation content is kept.
	// Gated on a clean resolve: a failed resolve saw no stored settings, so it
	// cannot tell "unmanaged" from "we could not look".
	if resolveErr == nil {
		if notice := sessionRetentionNotice(retCfg); notice != "" {
			log.Println(notice)
		}
	}
	retCancel()

	// Authentication: global API key + per-user token validator (fallback to DB lookup)
	tokenValidator := func(ctx context.Context, token string) (bool, error) {
		user, err := pg.GetDashboardUserByIngestionToken(ctx, token)
		if err != nil {
			if errors.Is(err, store.ErrTokenNotIngestion) {
				return false, auth.ErrReadOnlyToken
			}
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			return false, err
		}
		return user.IsActive, nil
	}
	authn := auth.New(apiKey, auth.WithTokenValidator(tokenValidator))
	log.Println("[cctraced] ingestion authentication enabled (per-user tokens active; API_KEY optional)")

	// Email aliases: load from DB + env var, refresh every 30s
	envAliases := emailalias.Parse(os.Getenv("EMAIL_ALIASES"))
	aliasLoader := emailalias.NewDBLoader()

	loadAliases := func() {
		dbAliases, err := pg.LoadAliases(ctx)
		if err != nil {
			log.Printf("[cctraced] alias load error: %v", err)
			dbAliases = make(map[string]string)
		}
		// Merge: env var aliases take precedence over DB aliases
		for k, v := range envAliases {
			dbAliases[k] = v
		}
		aliasLoader.Refresh(dbAliases)
		log.Printf("[cctraced] aliases refreshed (%d mappings)", len(dbAliases))
	}
	loadAliases()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				loadAliases()
			}
		}
	}()

	// Start periodic cleanup of orphan session records
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, err := pg.CleanupOrphanSessionRecords(ctx)
				if err != nil {
					log.Printf("[cleanup] orphan session records: %v", err)
				} else if n > 0 {
					log.Printf("[cleanup] deleted %d orphan session records", n)
				}
			}
		}
	}()

	// Verify swept tombstones on boot and daily. The verifier is bounded and starts
	// from tombstones, so this is cheap in the steady state and cannot fan out from
	// every source row. Any survivor is put back on the ordinary sweep queue.
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		runDeletedSessionVerifier(ctx, ticker.C, pg.VerifyDeletedSessions, func(requeued int64, err error) {
			if err != nil {
				log.Printf("[sweep] verify deleted sessions: %v", err)
			} else if requeued > 0 {
				log.Printf("[sweep] verify: %d tombstones still had rows, requeued", requeued)
			}
		})
	}()

	// Periodic refresh of precomputed Claude imputed-cost table (offline sessions).
	// Both ingest paths -- OTLP here and the sync REST handler below -- consult one
	// blocklist, so a session deleted from the dashboard is refused whichever door
	// it comes back through. Loaded before the listeners start: an empty cache would
	// accept everything for the first tick.
	blocklist := ingestblock.New()
	loadBlocklist := func(c context.Context) (*ingestblock.Sets, error) {
		bl, err := pg.LoadIngestBlocklist(c)
		if err != nil {
			return nil, err
		}
		sets := &ingestblock.Sets{
			DeletedSessions:  bl.DeletedSessions,
			BlockedProjects:  bl.BlockedProjects,
			ExcludedAccounts: map[string]bool{},
			ExcludedEmails:   map[string]bool{},
		}
		for _, a := range bl.ExcludedAccounts {
			sets.ExcludedAccounts[ingestblock.AccountKey(a.BillingProvider, a.AccountID)] = true
		}
		for _, e := range bl.ExcludedEmails {
			sets.ExcludedEmails[e] = true
		}
		return sets, nil
	}
	if err := blocklist.Refresh(ctx, loadBlocklist); err != nil {
		log.Printf("[ingestblock] initial load: %v", err)
	}
	if nSessions, nProjects := blocklist.Len(); nSessions > 0 || nProjects > 0 {
		log.Printf("[cctraced] ingest blocklist: %d deleted sessions, %d blocked projects", nSessions, nProjects)
	}

	// Reclaim the rows behind tombstones. The delete API kicks off its own sweep, so
	// this tick is for what that one could not finish -- a failed batch, a delete made
	// on another instance, or a backlog left by a restart. Nothing here affects what
	// is visible; the tombstones already decided that.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n, err := pg.SweepDeletedSessions(ctx, 25); err != nil {
					log.Printf("[sweep] deleted sessions: %v", err)
				} else if n > 0 {
					log.Printf("[sweep] reclaimed %d rows", n)
				}
			}
		}
	}()

	// The read-path aggregates get their own short tick. The trend charts and the
	// coverage estimate read them, so whatever the newest bucket is missing is
	// missing from the screen -- and the 30-minute maintenance pass below left the
	// current bucket up to half an hour behind ingest. Only the tail is rebuilt;
	// the wide window that catches late arrivals stays on the slow pass.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				usageFrom := time.Now().Add(-store.UsageRollupFreshWindow)
				if err := pg.RefreshUsageHourlyRollups(ctx, &usageFrom); err != nil {
					log.Printf("[usage-rollup] tail refresh: %v", err)
				}
				coverageFrom := time.Now().Add(-store.CoverageMinutesFreshWindow)
				if err := pg.RefreshCoverageMeasuredMinutes(ctx, &coverageFrom); err != nil {
					log.Printf("[coverage-minutes] tail refresh: %v", err)
				}
			}
		}
	}()

	// Exclusion changes queue their usage rebuild instead of running it inside the
	// request, where it took ~100s on production data. It gets its own goroutine so
	// a long rebuild delays neither the tail refresh above nor the maintenance pass.
	go runUsageRollupRebuildWorker(ctx, time.After, pg.RunPendingUsageRollupRebuild, log.Printf)

	// The blocklist refreshes on its own short tick, separate from the 30-minute
	// maintenance pass below. A delete made on one cctraced instance has to reach the
	// others quickly, and the read is two small tables -- cheap enough to repeat.
	// Deletes made through this instance's own API do not wait for it: the handler
	// pushes into the cache directly.
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := blocklist.Refresh(ctx, loadBlocklist); err != nil {
					log.Printf("[ingestblock] refresh: %v", err)
				}
			}
		}
	}()

	go func() {
		// The periodic pass only looks at recent OTEL. Sessions that predate OTEL
		// collection can never be filled, so an unbounded scan would re-derive the
		// same permanently-empty tail every tick; 48h covers any realistic lag
		// between an OTEL export and the JSONL sync that follows it. A durable
		// one-time boot repair handles all history.
		backfill := func(since time.Time) {
			if n, err := pg.BackfillSessionRecordLoginEmail(ctx, since); err != nil {
				log.Printf("[backfill] login_email: %v", err)
			} else if n > 0 {
				log.Printf("[backfill] filled login_email on %d session_records rows", n)
			}
		}
		// Fills what backfill() structurally cannot: a session that emitted no OTEL
		// at all has nothing to partition on, so its rows stay blank forever and a
		// blank matches no account exclusion (#346). The user's own OTEL timeline
		// still covers those minutes.
		//
		// It must never run before backfill() in the same pass: backfill() writes
		// what OTEL observed on that very session, this writes a judgement from the
		// surrounding timeline, and the observation is the better answer, so it goes
		// first and this one only sees what it left. Every call site below keeps that
		// order.
		//
		// Order alone was never enough, because it only holds within one pass. A
		// session whose session_records arrive on tick N and whose otel_events arrive
		// on tick N+1 gets inferred before it is ever observed. What settles that is
		// backfillMatch admitting login_email_source = 'inferred': tick N+1's
		// backfill() overwrites the guess with the observation. Ordering keeps the
		// common case cheap; the provenance column keeps the race correct.
		infer := func(since time.Time) {
			if n, err := pg.InferSessionRecordLoginEmail(ctx, since); err != nil {
				log.Printf("[infer] login_email: %v", err)
			} else if n > 0 {
				log.Printf("[infer] inferred login_email on %d session_records rows", n)
			}
		}
		// Carries the client's own OpenAI account resolution from quota_samples
		// onto Codex records with no account_id at all (#524). It has to run before
		// attributeCodexLogin below in the same pass: an account_id filled here is
		// what makes that same record attributable in this pass instead of the next
		// one, and RefreshCodexImputedCost after both copies whatever login_email
		// and account_id it finds on the row at that moment.
		fillCodexAccount := func(since time.Time) {
			if n, err := pg.FillCodexAccountFromQuota(ctx, since); err != nil {
				log.Printf("[codex-account] fill: %v", err)
			} else if n > 0 {
				log.Printf("[codex-account] filled account_id on %d session_records rows", n)
			}
		}
		// Writes the subscription address each Codex account bills, taken from the
		// account-to-address mapping quota_samples observed (#524). Like backfill and
		// infer above, it must run before RefreshCodexImputedCost, and it must run
		// after fillCodexAccount for the same reason infer runs after backfill: the
		// better input has to land before the pass that reads it.
		attributeCodexLogin := func(since time.Time) {
			if n, err := pg.AttributeCodexLoginEmailFromQuota(ctx, since); err != nil {
				log.Printf("[codex-attribution] login_email: %v", err)
			} else if n > 0 {
				log.Printf("[codex-attribution] attributed login_email on %d session_records rows", n)
			}
		}
		refresh := func() {
			// One bound for all four passes: separate time.Now() calls would give the
			// later passes a window the earlier ones did not see.
			since := time.Now().Add(-48 * time.Hour)
			backfill(since)
			infer(since)
			fillCodexAccount(since)
			attributeCodexLogin(since)
			// Before the imputed pass, not after: the rates it derives are what
			// that pass prices offline sessions with, so deriving them second
			// would leave every tick one cycle behind.
			if err := pg.RefreshModelRateBuckets(ctx); err != nil {
				log.Printf("[model-rates] bucket refresh: %v", err)
			}
			if err := pg.RefreshClaudeImputedCost(ctx); err != nil {
				log.Printf("[imputed-cost] refresh: %v", err)
			}
			// Full codex rebuild. The 10s incremental below only sees rows newer
			// than the table, so it misses rows rewritten in place (sync --reenrich)
			// and repricing caused by a new codex_model_rates entry. This corrects both.
			if err := pg.RefreshCodexImputedCost(ctx); err != nil {
				log.Printf("[codex-imputed] full refresh: %v", err)
			}
			// Keeps visible_session_records' excluded_sessions table (#298) caught up
			// with otel_events ingested since the last tick; RecomputeExcludedSessions
			// handles the excluded_accounts-changed case directly.
			if err := pg.RefreshExcludedSessionsIncremental(ctx); err != nil {
				log.Printf("[excluded-sessions] refresh: %v", err)
			}
			// A quota reading can name the billing account behind an address that
			// was excluded long before (#715). Once the link is known the views hide
			// that account's new rows at once; this tick is what learns a new link
			// and rebuilds what was aggregated before it.
			if changed, err := pg.RefreshExcludedBillingLinks(ctx); err != nil {
				log.Printf("[excluded-billing-links] refresh: %v", err)
			} else if changed {
				log.Printf("[excluded-billing-links] link set changed; exclusion rebuilt")
			}
			// Sweeps rows for blocked projects that reached storage anyway -- a sync
			// already in flight when the block landed, or records that arrived without
			// the project hash the envelope would have carried. The ingest-time refusal
			// is the primary defence; this is what catches the races it cannot.
			if n, err := pg.PurgeBlockedProjects(ctx); err != nil {
				log.Printf("[blocked-projects] purge: %v", err)
			} else if n > 0 {
				log.Printf("[blocked-projects] tombstoned %d sessions that raced past ingest", n)
			}
			// Last in the tick on purpose: it aggregates visible_events, and every
			// step above can still change what is visible -- imputed cost, the
			// exclusion table, blocked-project tombstones. Running it first would
			// bake the previous tick's answer into the rollup for an hour.
			// Its own bound, not `since`: this window is sized by how late a row can
			// arrive for an already-closed hour, which is a different question from
			// how far back the sync backfill and login inference look.
			rollupFrom := time.Now().Add(-store.UsageRollupWindow)
			if err := pg.RefreshUsageHourlyRollups(ctx, &rollupFrom); err != nil {
				log.Printf("[usage-rollup] refresh: %v", err)
			}
			// Same reasoning, its own window: the coverage fit reads 28 days, so a
			// minute this never revisits stays wrong inside the fit for four weeks.
			coverageFrom := time.Now().Add(-store.CoverageMinutesWindow)
			if err := pg.RefreshCoverageMeasuredMinutes(ctx, &coverageFrom); err != nil {
				log.Printf("[coverage-minutes] refresh: %v", err)
			}
		}
		// Boot history repair: full session/user timelines are snapshotted once into
		// durable staging, then source and derived rows advance together in bounded
		// committed batches. A restart resumes from durable progress; the marker lands
		// only after both phases are exhausted. The two bounded calls in refresh()
		// remain periodic and independently retryable.
		backfilled, inferred, err := pg.BackfillSessionRecordLoginEmailHistoryOnce(ctx)
		if err != nil {
			log.Printf("[backfill] login_email history: %v", err)
		} else {
			if backfilled > 0 {
				log.Printf("[backfill] filled login_email on %d session_records rows", backfilled)
			}
			if inferred > 0 {
				log.Printf("[infer] inferred login_email on %d session_records rows", inferred)
			}
		}
		// Same shape, same goroutine, run serially right after: #524's fix has its
		// own one-time repair across all of retained history, and it has to run in
		// the phase order revert -> account -> attribute (see
		// RepairCodexLoginEmailFromQuotaOnce) before the periodic passes above ever
		// see the affected rows.
		reverted, accounts, attributed, err := pg.RepairCodexLoginEmailFromQuotaOnce(ctx)
		if err != nil {
			log.Printf("[codex-repair] login_email history: %v", err)
		} else if reverted > 0 || accounts > 0 || attributed > 0 {
			log.Printf("[codex-repair] reverted %d, filled account_id on %d, attributed login_email on %d session_records rows",
				reverted, accounts, attributed)
		}
		if err := pg.RefreshModelRateBuckets(ctx); err != nil {
			log.Printf("[model-rates] bucket refresh: %v", err)
		}
		if err := pg.RefreshClaudeImputedCost(ctx); err != nil {
			log.Printf("[imputed-cost] refresh: %v", err)
		}
		// Once at boot too (#715): links for addresses excluded before this
		// version existed are otherwise first drawn by the tick 30 minutes in,
		// and until then the dashboard shows those accounts' rows again.
		if changed, err := pg.RefreshExcludedBillingLinks(ctx); err != nil {
			log.Printf("[excluded-billing-links] boot refresh: %v", err)
		} else if changed {
			log.Printf("[excluded-billing-links] link set changed at boot; exclusion rebuilt")
		}
		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()

	// Codex prices come from OpenAI's published table, not from telemetry: Codex
	// JSONL carries tokens but no cost, so cost is imputed from codex_model_rates.
	// That table used to be hand-edited in migrations.go and drifted -- one model
	// was seeded at five times its real price, another was missing and therefore
	// billed at $0. Confirm the published table at boot/daily, with at most three
	// accelerated five-minute checks per newly seen raw model in this process.
	//
	// A price change adds a dated row rather than replacing one, because OpenAI
	// cuts prices and history has to stay billed at what it actually cost. See
	// PgStore.UpsertCodexModelRates.
	//
	// Its own goroutine rather than a step in the 30-minute refresh above: prices
	// move a few times a year, the fetch leaves the process to reach the network,
	// and a slow or hanging source must not delay the imputed-cost rebuild.
	go func() {
		sync := &codexRatesSync{
			now:       time.Now,
			missing:   pg.ListUnpricedCodexModelKeys,
			fetch:     codexrates.Fetch,
			changelog: codexrates.FetchChangelog,
			upsert:    pg.UpsertCodexModelRatesAt,
			reprice:   pg.RefreshCodexImputedCost,
		}
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		sync.run(ctx, ticker.C)
	}()

	// Timescale retention jobs delete chunks asynchronously, outside store write
	// transactions. Poll their durable completion timestamps and repair only sessions
	// crossing a cutoff after a newly completed run; no future ingest is required.
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if n, err := pg.ReconcileSessionOverviewRollupsForRetention(ctx); err != nil {
					log.Printf("[session-overviews] retention reconciliation: %v", err)
				} else if n > 0 {
					log.Printf("[session-overviews] reconciled %d retention-eligible session(s)", n)
				}
				if n, err := pg.ReconcilePluginInvocationFactsForRetention(ctx); err != nil {
					log.Printf("[plugin-facts] retention reconciliation: %v", err)
				} else if n > 0 {
					log.Printf("[plugin-facts] reconciled %d retention-eligible session(s)", n)
				}
				if n, err := pg.ReconcileTaskSegmentFactsForRetention(ctx); err != nil {
					log.Printf("[task-segments] retention reconciliation: %v", err)
				} else if n > 0 {
					log.Printf("[task-segments] reconciled %d retention-eligible session(s)", n)
				}
				// Segment tool counts come from OTEL, which arrives on its own
				// path; only a session_records write recomputes the fact. A
				// session that finished before its tool events landed would
				// otherwise report zero calls forever (#435).
				if n, err := pg.ReconcileTaskSegmentFactsForLateOTEL(ctx); err != nil {
					log.Printf("[task-segments] late-OTEL reconciliation: %v", err)
				} else if n > 0 {
					log.Printf("[task-segments] recomputed %d session(s) whose tool events arrived late", n)
				}
			}
		}
	}()

	// Codex imputed cost, incremental. Short interval because this is the only path
	// by which a finished codex session's cost reaches any chart -- the client syncs
	// at most every 30s, so a slower refresh here would add to that delay rather than
	// hide inside it. Cheap enough to run this often: a tick with nothing new costs
	// ~0.1ms (an index existence check), and one with work to do ~12ms.
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		// A failure here usually keeps failing -- the database is down, or the cursor
		// row is gone. At six ticks a minute that is 8,640 identical lines a day,
		// which does not inform anyone and does bury whatever else is in the log. Say
		// it when it starts and when it stops.
		var lastErr string
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				n, err := pg.RefreshCodexImputedCostIncremental(ctx)
				switch {
				case err != nil:
					if msg := err.Error(); msg != lastErr {
						log.Printf("[codex-imputed] incremental refresh: %v (repeats are suppressed until this changes)", err)
						lastErr = msg
					}
				default:
					if lastErr != "" {
						log.Printf("[codex-imputed] incremental refresh recovered")
						lastErr = ""
					}
					if n > 0 {
						log.Printf("[codex-imputed] refreshed %d row(s)", n)
					}
				}
			}
		}
	}()

	// Initialize PGMQ queues
	q := queue.New(pg.Pool())
	if err := q.CreateQueues(ctx); err != nil {
		log.Fatalf("[cctraced] queue creation failed: %v", err)
	}
	log.Println("[cctraced] queues ready (otel_logs, otel_metrics)")

	// WAL disk spillers for ring buffer overflow
	walDir := envOr("WAL_DIR", "/data/buffer-wal")
	logsSpiller := buffer.NewDiskSpiller(walDir+"/logs", 100<<20)
	metricsSpiller := buffer.NewDiskSpiller(walDir+"/metrics", 100<<20)

	// Recover WAL data from previous crash
	walRecovered := 0
	logsRecovered, err := logsSpiller.Replay(func(data []byte) error {
		_, err := q.SendRaw(ctx, queue.QueueOtelLogs, data)
		return err
	})
	walRecovered += logsRecovered
	if err != nil {
		log.Printf("[cctraced] WAL recovery (logs) paused with unreplayed data preserved: %v", err)
	}
	metricsRecovered, err := metricsSpiller.Replay(func(data []byte) error {
		_, err := q.SendRaw(ctx, queue.QueueOtelMetrics, data)
		return err
	})
	walRecovered += metricsRecovered
	if err != nil {
		log.Printf("[cctraced] WAL recovery (metrics) paused with unreplayed data preserved: %v", err)
	}
	if walRecovered > 0 {
		log.Printf("[cctraced] recovered %d items from WAL", walRecovered)
	}

	// Start worker (dequeue -> parse -> insert)
	w := worker.New(q, pg)
	go w.Run(ctx)

	// In-memory ring buffers for DB-down resilience
	logsBuf := buffer.NewRing(10000)
	metricsBuf := buffer.NewRing(10000)

	logsFlusher := buffer.NewFlusher(logsBuf, func(data []byte) error {
		_, err := q.SendRaw(ctx, queue.QueueOtelLogs, data)
		return err
	}, "logs").WithSpiller(logsSpiller)
	metricsFlusher := buffer.NewFlusher(metricsBuf, func(data []byte) error {
		_, err := q.SendRaw(ctx, queue.QueueOtelMetrics, data)
		return err
	}, "metrics").WithSpiller(metricsSpiller)

	go logsFlusher.Run(ctx)
	go metricsFlusher.Run(ctx)
	log.Println("[cctraced] ring buffers ready (logs, metrics)")

	// gRPC server — OTLP receivers (enqueue to PGMQ)
	grpcLis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		log.Fatalf("[cctraced] gRPC listen failed: %v", err)
	}

	// The same lookup the HTTP receiver uses. Installed on the receivers too, so
	// the gRPC path can bind attribution to the token its interceptor verified
	// rather than trusting the payload (#535).
	ingestIdentity := func(ctx context.Context, token string) (*otelrecv.ClientIdentity, error) {
		user, err := pg.GetDashboardUserByApiToken(ctx, token)
		if err != nil {
			return nil, err
		}
		if user == nil || !user.IsActive {
			return nil, fmt.Errorf("token belongs to no active user")
		}
		return &otelrecv.ClientIdentity{
			ProfileEmail: user.Email,
			LoginEmail:   user.Email,
			UserID:       user.CctraceUserID,
			UserTeam:     user.Team,
		}, nil
	}

	logsRecv := otelrecv.NewLogsReceiver(q, logsBuf, aliasLoader).
		WithSessionFilter(blocklist.SessionDeleted).
		WithAccountFilter(blocklist.EmailExcluded).
		WithIdentityResolver(ingestIdentity)
	metricsRecv := otelrecv.NewMetricsReceiver(q, metricsBuf, aliasLoader).
		WithSessionFilter(blocklist.SessionDeleted).
		WithAccountFilter(blocklist.EmailExcluded).
		WithBillingAccountFilter(blocklist.AccountExcluded).
		WithIdentityResolver(ingestIdentity)

	grpcSrv := newGRPCServer(authn, logsRecv, metricsRecv, otelrecv.NewTracesReceiver())

	go func() {
		log.Printf("[cctraced] gRPC listening on :%s (OTLP receiver)\n", grpcPort)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Fatalf("[cctraced] gRPC serve error: %v", err)
		}
	}()

	// HTTP OTLP server — OTLP/HTTP receiver (port 4318)
	httpOtelHandler := otelrecv.NewHTTPReceiver(logsRecv, metricsRecv).WithIdentityResolver(ingestIdentity).Handler()
	httpOtelHandler = authenticatedOTLPHandler(httpOtelHandler, authn)
	httpOtelSrv := newHTTPServer(":"+httpOtelPort, httpOtelHandler)
	httpOtelLis, err := net.Listen("tcp", httpOtelSrv.Addr)
	if err != nil {
		log.Fatalf("[cctraced] HTTP OTLP listen failed: %v", err)
	}

	go func() {
		log.Printf("[cctraced] HTTP OTLP listening on :%s (OTLP/HTTP receiver)\n", httpOtelPort)
		if err := httpOtelSrv.Serve(httpOtelLis); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[cctraced] HTTP OTLP serve error: %v", err)
		}
	}()

	// HTTP server — REST API (reads from store)
	apiSrv := api.NewServer(pg, jwtMgr, authn.HTTPMiddleware).WithSetupToken(setupToken).WithAllowedOrigins(allowedOrigins).WithCookieSecure(cookieSecure).WithIngestBlocklist(blocklist).WithIngestBlocklistLoader(loadBlocklist).WithSyncBodyLimit(envSyncBodyLimit()).WithAliases(aliasLoader).WithVersion(version).WithDataPath(envOr("STORAGE_VOLUME_PATH", walDir)).WithHealthExtra(func(ctx context.Context) (map[string]interface{}, error) {
		logDepth, err := q.Depth(ctx, queue.QueueOtelLogs)
		if err != nil {
			return nil, fmt.Errorf("log queue depth: %w", err)
		}
		metricDepth, err := q.Depth(ctx, queue.QueueOtelMetrics)
		if err != nil {
			return nil, fmt.Errorf("metric queue depth: %w", err)
		}
		walLogsFiles, err := logsSpiller.FileCount()
		if err != nil {
			return nil, fmt.Errorf("log WAL files: %w", err)
		}
		walMetricsFiles, err := metricsSpiller.FileCount()
		if err != nil {
			return nil, fmt.Errorf("metric WAL files: %w", err)
		}
		return map[string]interface{}{
			"log_queue_depth":    logDepth,
			"metric_queue_depth": metricDepth,
			"log_buffer_len":     logsBuf.Len(),
			"metric_buffer_len":  metricsBuf.Len(),
			"wal_logs_files":     walLogsFiles,
			"wal_metrics_files":  walMetricsFiles,
		}, nil
	})

	// Weekly AI reports. Runs a previous process left running are failed before
	// the routes can report them as still in progress.
	aiSvc := newAIReportService(pg, aiEnvFromEnv(envOr("STORAGE_VOLUME_PATH", walDir)))
	if aiSvc != nil {
		if err := aiSvc.RecoverOnBoot(ctx); err != nil {
			log.Printf("[cctraced] AI report boot recovery: %v", err)
		}
		apiSrv.WithAIReports(aiSvc)

		// Weekly reports that run by themselves. A firing is a weekday and a
		// wall-clock minute in each user's own zone, so the tick is a minute:
		// coarser would drift past the minute a user chose, and finer would ask
		// the same question several times for nothing.
		//
		// The tick goes through the API server because that is what knows how a
		// request's scope is built, so an automatic run reads what the user's
		// own run reads.
		go func() {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, err := apiSrv.RunAIReportSchedule(ctx, time.Now()); err != nil {
						log.Printf("[ai-reports] schedule tick: %v", err)
					}
				}
			}
		}()
	}

	// Combined mux: /api/* → REST API, /downloads/* → client binaries, everything else → embedded SPA
	rootMux := http.NewServeMux()
	rootMux.Handle("/api/", apiSrv.Handler())
	rootMux.Handle("/downloads/", downloadHandler("/downloads"))
	rootMux.Handle("/", web.Handler())

	httpSrv := newHTTPServer(":"+httpPort, accessLog(rootMux))
	if aiSvc != nil {
		// Shutdown waits for active connections; progress streams must end first.
		httpSrv.RegisterOnShutdown(aiSvc.CloseStreams)
	}
	httpLis, err := net.Listen("tcp", httpSrv.Addr)
	if err != nil {
		log.Fatalf("[cctraced] HTTP listen failed: %v", err)
	}

	go func() {
		log.Printf("[cctraced] HTTP listening on :%s (REST API)\n", httpPort)
		if err := httpSrv.Serve(httpLis); err != nil && err != http.ErrServerClosed {
			log.Fatalf("[cctraced] HTTP serve error: %v", err)
		}
	}()

	// One-time data repairs run here, after every listener is bound. Reads stay on
	// their exact live/raw paths until each crash-safe completion marker commits.
	// Repairs are serial to bound database load and are joined during shutdown.
	repairDone := startManagedRepairs(ctx,
		func(ctx context.Context) {
			// Canonicalization rewrites session_records.project_hash. Keep it in the
			// same repair step as the initial rollup so a failed canonicalization can
			// never be followed by the session overview completion marker.
			if err := pg.BackfillCanonicalProjectHash(ctx); err != nil {
				log.Printf("[cctraced] project_hash backfill: %v", err)
				return
			}
			if err := pg.BackfillSessionOverviewRollups(ctx); err != nil {
				log.Printf("[cctraced] session overview rollup backfill: %v", err)
				return
			}
			// Same step, and after the backfill on purpose: the repair reads the
			// overview it builds, and a fresh build already carries the corrected
			// start times.
			if err := pg.RepairEpochSessionOverviewStarts(ctx); err != nil {
				log.Printf("[cctraced] session overview epoch start repair: %v", err)
			}
		},
		func(ctx context.Context) {
			if err := pg.BackfillUsageHourlyRollups(ctx); err != nil {
				log.Printf("[cctraced] usage hourly rollup backfill: %v", err)
			}
		},
		func(ctx context.Context) {
			if err := pg.BackfillCoverageMeasuredMinutes(ctx); err != nil {
				log.Printf("[cctraced] coverage measured minutes backfill: %v", err)
			}
		},
		func(ctx context.Context) {
			if err := pg.BackfillPluginInvocationFacts(ctx); err != nil {
				log.Printf("[cctraced] plugin invocation fact backfill: %v", err)
			}
		},
		func(ctx context.Context) {
			if err := pg.BackfillTaskSegmentFacts(ctx); err != nil {
				log.Printf("[cctraced] task segment fact backfill: %v", err)
			}
		},
		func(ctx context.Context) {
			// Facts for Codex sessions whose developer rows were relabeled, in small
			// batches: a full rebuild would stall session record ingest for minutes.
			if err := pg.BackfillCodexDeveloperFacts(ctx); err != nil {
				log.Printf("[cctraced] codex developer fact repair: %v", err)
			}
		},
		func(ctx context.Context) {
			// Codex facts built before tool calls were counted from JSONL still say
			// 0 calls. Batched for the same reason as the repair above.
			if err := pg.BackfillCodexSegmentToolCounts(ctx); err != nil {
				log.Printf("[cctraced] codex segment tool count backfill: %v", err)
			}
		},
		func(ctx context.Context) {
			// The same for gjc and omo facts, built before their tool_result rows
			// were counted (#752).
			if err := pg.BackfillPiSegmentToolCounts(ctx); err != nil {
				log.Printf("[cctraced] pi segment tool count backfill: %v", err)
			}
		},
		func(ctx context.Context) {
			// Rewrites repository_name from repository_id on rows written before
			// the value was derived at write time (#691).
			if err := pg.BackfillDerivedRepositoryNames(ctx); err != nil {
				log.Printf("[cctraced] derived repository name backfill: %v", err)
			}
		},
		func(ctx context.Context) {
			// Stamps provenance on rows attributed before login_email_source
			// existed. It rewrites every attributed row, so it belongs here rather
			// than in the migration list.
			if err := pg.BackfillLoginEmailSource(ctx); err != nil {
				log.Printf("[cctraced] login_email_source backfill: %v", err)
			}
		},
	)

	// Graceful shutdown
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	<-c
	fmt.Println("\n[cctraced] shutting down...")

	// 1. Stop receiving new data
	grpcSrv.GracefulStop()
	_ = httpSrv.Shutdown(context.Background())
	_ = httpOtelSrv.Shutdown(context.Background())

	// 2. Drain or spill ring buffers (flushes to PGMQ or WAL)
	logsFlusher.DrainOrSpill()
	metricsFlusher.DrainOrSpill()

	// 3. Cancel context to stop flushers, worker, and one-time repairs.
	cancel()
	// Do not close the shared pgx pool out from under a repair. Every repair query uses
	// ctx, so cancellation interrupts a blocked database operation before this returns.
	<-repairDone

	// 3b. Stop AI report runs: each run's app-server child is killed and the run
	// recorded failed(server_shutdown) while the pool is still open.
	if aiSvc != nil {
		aiCtx, aiCancel := context.WithTimeout(context.Background(), 10*time.Second)
		aiSvc.Shutdown(aiCtx)
		aiCancel()
	}

	// 4. Wait for flushers to finish (with timeout)
	drainTimeout := time.After(5 * time.Second)
	for _, done := range []<-chan struct{}{logsFlusher.Done(), metricsFlusher.Done()} {
		select {
		case <-done:
		case <-drainTimeout:
			log.Println("[cctraced] flusher drain timeout")
		}
	}

	// 5. Close spillers
	logsSpiller.Close()
	metricsSpiller.Close()
}

// publishedSecrets were handed out as examples, so anyone can sign a dashboard
// token or derive the API-key sealing key with them. Exact values only: a
// pattern would also refuse secrets nobody published. Checked with surrounding
// whitespace trimmed, because a quoted .env value or `set -a; . ./.env` keeps
// it and the padded copy is just as public.
var publishedSecrets = map[string]bool{
	// JWT_SECRET in deploy/.env.example since the first Docker deploy,
	// exported to the mirror with that file.
	"change-me-at-least-32-bytes-long-secret-key": true,
}

// validateAuthConfiguration takes CCTRACE_SECRETS_KEY too: it seals the API
// keys registered in Admin > AI and falls back to JWT_SECRET when empty.
func validateAuthConfiguration(jwtSecret, secretsKey string) error {
	if jwtSecret == "" {
		return fmt.Errorf("JWT_SECRET is required")
	}
	var errs []error
	if publishedSecrets[strings.TrimSpace(jwtSecret)] {
		msg := "JWT_SECRET is a published example value: anyone can sign dashboard tokens with it, admin ones included. " +
			"Replace it with a random value (openssl rand -hex 32); that ends every dashboard session"
		if _, source := aireport.SecretsSource(secretsKey, jwtSecret); source == aireport.KeySourceJWTSecret {
			msg += ", and because CCTRACE_SECRETS_KEY is empty, API keys registered in Admin > AI were sealed with it and must be registered again"
		}
		errs = append(errs, errors.New(msg))
	}
	if publishedSecrets[strings.TrimSpace(secretsKey)] {
		errs = append(errs, errors.New("CCTRACE_SECRETS_KEY is a published example value: anyone can derive the key that seals API keys registered in Admin > AI. "+
			"Replace it with a random value (openssl rand -hex 32), then register those keys again in Admin > AI"))
	}
	return errors.Join(errs...)
}

func authenticatedOTLPHandler(next http.Handler, authenticator *auth.Authenticator) http.Handler {
	if !authenticator.Enabled() {
		return next
	}
	return authenticator.HTTPMiddleware(next)
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

// downloadHandler clears the server write deadline before streaming release
// binaries. API responses retain their defensive timeout, while slow links can
// complete an otherwise valid signed update download.
func downloadHandler(dir string) http.Handler {
	files := http.StripPrefix("/downloads/", http.FileServer(http.Dir(dir)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
		files.ServeHTTP(w, r)
	})
}

// envDefault reads a setting an admin can override in Admin > AI. The name
// carries _DEFAULT because that is all the environment supplies: the admin's
// choice wins and is stored in the database. The old spelling is still read so
// a deployment that set it does not go dark on upgrade, with a warning naming
// its replacement.
//
// Both names are passed as literals so the help-text guard still sees them --
// TestUsageTextCoversEveryEnvKeyTheDaemonReads scans the source for env reads
// and its pattern knows this helper, so a key added here without a --help entry
// fails that test rather than going undocumented.
func envDefault(preferred, deprecated string) string {
	if v := os.Getenv(preferred); strings.TrimSpace(v) != "" {
		return v
	}
	if v := os.Getenv(deprecated); strings.TrimSpace(v) != "" {
		log.Printf("[cctraced] %s is deprecated; use %s (same meaning: the default an admin's choice in Admin > AI overrides)", deprecated, preferred)
		return v
	}
	return ""
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// envIntOpt reads an optional integer env var, distinguishing "unset"/blank
// (nil) from a real value. Retention config relies on this: a nil result means
// "leave the DB policy untouched", so a plain deploy never changes retention.
func envIntOpt(key string) *int {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		log.Printf("[cctraced] %s=%q is not an integer; ignoring", key, v)
		return nil
	}
	return &n
}

// envSyncBodyLimit reads the /api/sync body ceiling override in bytes. It fails
// closed: anything missing or unparseable returns 0, which leaves the API server
// on its compiled 8 MiB default rather than opening the route. Values that parse
// are handed over as read -- api.Server is the one place that decides whether a
// limit is usable -- and only warned about here, where the operator can see it.
func envSyncBodyLimit() int64 {
	// The key is spelled out inline, not held in a const: usage_test.go's
	// envKeyRe scans for a string literal inside the lookup call, and a const
	// would hide this key from the gate that proves every env the daemon reads
	// appears in --help.
	raw := strings.TrimSpace(os.Getenv("CCTRACE_MAX_SYNC_BODY_BYTES"))
	if raw == "" {
		return 0
	}
	const key = "CCTRACE_MAX_SYNC_BODY_BYTES"
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.Printf("[cctraced] %s=%q is not a byte count; using the default limit", key, raw)
		return 0
	}
	if n <= 0 {
		log.Printf("[cctraced] %s=%d is not positive; using the default limit", key, n)
	} else if n > api.MaxConfigurableSyncBodyBytes {
		log.Printf("[cctraced] %s=%d exceeds the %d byte ceiling; using the default limit", key, n, int64(api.MaxConfigurableSyncBodyBytes))
	}
	return n
}

// ptrDaysStr renders an optional day count for the boot retention audit line.
func ptrDaysStr(p *int) string {
	if p == nil {
		return "-"
	}
	return strconv.Itoa(*p)
}

// statusRecorder captures the response status code for access logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController reach the connection. Without it Flush
// and SetWriteDeadline fail silently, so the AI report progress stream would
// buffer and be cut at the server's WriteTimeout.
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Flush serves callers that assert http.Flusher directly.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// aiEnv is the weekly AI report configuration read from the environment.
// Runtime is only the default choice; Model and ReasoningEffort are Codex's.
type aiEnv struct {
	// Enabled is CCTRACE_AI_ENABLED_DEFAULT, nil when unset.
	Enabled         *bool
	Runtime         string
	CodexHome       string
	Model           string
	ReasoningEffort string
	APIKey          string
	OpenAIModel     string
	ClaudeModel     string
	NVIDIAModel     string
	LiteLLMModel    string
	// OpenAIAPIKey and AnthropicAPIKey come from dedicated variables only, never
	// OPENAI_API_KEY or ANTHROPIC_API_KEY.
	OpenAIAPIKey    string
	AnthropicAPIKey string
	NVIDIAAPIKey    string
	LiteLLMAPIKey   string
	// LiteLLMBaseURL is where the proxy lives. Unlike the other providers the
	// address is deployment-specific, so the runtime cannot hardcode one.
	LiteLLMBaseURL string
	// SecretsKey seals keys registered in Admin > AI: CCTRACE_SECRETS_KEY, or
	// JWT_SECRET when that is unset; SecretsSource names which.
	SecretsKey    string
	SecretsSource string
	MaxConcurrent int
	WallClock     time.Duration
	// DefaultTZ is CCTRACE_AI_DEFAULT_TZ, already loaded; empty is UTC.
	DefaultTZ string
}

// aiEnvFromEnv reads the AI report settings. Unusable numbers keep the defaults
// (2 concurrent runs, 8 minutes) rather than disabling the feature.
func aiEnvFromEnv(storageDir string) aiEnv {
	env := aiEnv{
		Runtime:         strings.TrimSpace(envDefault("CCTRACE_AI_RUNTIME_DEFAULT", "CCTRACE_AI_RUNTIME")),
		CodexHome:       envOr("CCTRACE_AI_CODEX_HOME", filepath.Join(storageDir, "ai-codex-home")),
		Model:           envDefault("CCTRACE_AI_MODEL_DEFAULT", "CCTRACE_AI_MODEL"),
		ReasoningEffort: envDefault("CCTRACE_AI_REASONING_EFFORT_DEFAULT", "CCTRACE_AI_REASONING_EFFORT"),
		APIKey:          os.Getenv("CODEX_API_KEY"),
		OpenAIModel:     envDefault("CCTRACE_AI_OPENAI_MODEL_DEFAULT", "CCTRACE_AI_OPENAI_MODEL"),
		ClaudeModel:     envDefault("CCTRACE_AI_CLAUDE_MODEL_DEFAULT", "CCTRACE_AI_CLAUDE_MODEL"),
		OpenAIAPIKey:    os.Getenv("CCTRACE_AI_OPENAI_API_KEY"),
		AnthropicAPIKey: os.Getenv("CCTRACE_AI_ANTHROPIC_API_KEY"),
		NVIDIAModel:     envDefault("CCTRACE_AI_NVIDIA_MODEL_DEFAULT", "CCTRACE_AI_NVIDIA_MODEL"),
		LiteLLMModel:    envDefault("CCTRACE_AI_LITELLM_MODEL_DEFAULT", "CCTRACE_AI_LITELLM_MODEL"),
		NVIDIAAPIKey:    os.Getenv("CCTRACE_AI_NVIDIA_API_KEY"),
		LiteLLMAPIKey:   os.Getenv("CCTRACE_AI_LITELLM_API_KEY"),
		LiteLLMBaseURL:  strings.TrimSpace(envDefault("CCTRACE_AI_LITELLM_BASE_URL_DEFAULT", "CCTRACE_AI_LITELLM_BASE_URL")),
		MaxConcurrent:   2,
		WallClock:       8 * time.Minute,
	}
	env.SecretsKey, env.SecretsSource = aireport.SecretsSource(os.Getenv("CCTRACE_SECRETS_KEY"), os.Getenv("JWT_SECRET"))
	if raw := strings.TrimSpace(envDefault("CCTRACE_AI_ENABLED_DEFAULT", "CCTRACE_AI_ENABLED")); raw != "" {
		on, err := strconv.ParseBool(raw)
		if err != nil {
			log.Printf("[cctraced] CCTRACE_AI_ENABLED_DEFAULT=%q is not true or false; AI reports stay off", raw)
		}
		env.Enabled = &on
	}
	if n := envIntOpt("CCTRACE_AI_MAX_CONCURRENT"); n != nil {
		if *n > 0 {
			env.MaxConcurrent = *n
		} else {
			log.Printf("[cctraced] CCTRACE_AI_MAX_CONCURRENT=%d is not positive; using %d", *n, env.MaxConcurrent)
		}
	}
	if raw := strings.TrimSpace(os.Getenv("CCTRACE_AI_WALL_CLOCK")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			env.WallClock = d
		} else {
			log.Printf("[cctraced] CCTRACE_AI_WALL_CLOCK=%q is not a positive duration; using %s", raw, env.WallClock)
		}
	}
	// Loaded here, once: a zone that does not load would otherwise reach the
	// ticker and skip, every minute, each user who has no zone of their own.
	// A typo must not stop the server either, so it warns and reads UTC.
	if raw := strings.TrimSpace(os.Getenv("CCTRACE_AI_DEFAULT_TZ")); raw != "" {
		if _, err := time.LoadLocation(raw); err == nil {
			env.DefaultTZ = raw
		} else {
			log.Printf("[cctraced] WARNING: CCTRACE_AI_DEFAULT_TZ=%q is not a known time zone; automatic AI reports use UTC for users without a zone", raw)
		}
	}
	return env
}

// The Postgres store must keep satisfying the report service's storage
// contract; a drifted signature fails the build instead of silently leaving AI
// reports unconfigured at boot.
var _ aireport.Store = (*store.PgStore)(nil)

// newCodexRuntime builds the Codex app-server runtime from aiEnv. The dedicated
// CODEX_HOME gets its config.toml (model and effort only) before any run; if
// that cannot be written the feature stays unconfigured rather than starting
// against a home that might carry otel, hooks or MCP servers. Apps are disabled
// so connectors never load for a report turn.
var newCodexRuntime = func(env aiEnv) airuntime.Runtime {
	cfg := codexappserver.RuntimeConfig{
		Home:            env.CodexHome,
		Model:           env.Model,
		ReasoningEffort: env.ReasoningEffort,
		APIKey:          env.APIKey,
		Disable:         []string{"apps"},
	}
	if err := codexappserver.PrepareHome(cfg); err != nil {
		log.Printf("[cctraced] the codex home for AI reports could not be prepared (%v); the Codex runtime is not built", err)
		return nil
	}
	return codexappserver.NewRuntime(cfg)
}

// lookCodexCLI finds the codex binary the Codex runtime spawns.
var lookCodexCLI = exec.LookPath

// aiRuntimesFromEnv builds every runtime this server can run, Codex first:
// Codex when its driver is linked, the codex CLI is on PATH and its home is
// prepared; the OpenAI and Claude API runtimes always, reading their keys from
// kr on each call. missing says why a runtime was not built.
// nvidiaBaseURL is NVIDIA's hosted inference endpoint. Fixed, unlike LiteLLM's.
const nvidiaBaseURL = "https://integrate.api.nvidia.com"

// savedBaseURL answers with the address an admin saved for a runtime, falling
// back to the environment. It is read per request, so saving an address in the
// screen takes effect on the next run rather than the next restart.
func savedBaseURL(st aireport.Store, runtime, envURL string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		if st != nil {
			if s, err := st.GetAISettings(ctx, runtime); err == nil && s != nil && strings.TrimSpace(s.BaseURL) != "" {
				return s.BaseURL, nil
			}
		}
		return envURL, nil
	}
}

func aiRuntimesFromEnv(env aiEnv, kr *aireport.Keyring, st aireport.Store) ([]airuntime.Runtime, map[string]string) {
	var rts []airuntime.Runtime
	missing := map[string]string{}
	switch newCodexRuntime {
	case nil:
		missing[aireport.DefaultRuntimeKey] = "이 빌드에는 Codex 드라이버가 없습니다"
	default:
		if _, err := lookCodexCLI("codex"); err != nil {
			missing[aireport.DefaultRuntimeKey] = "서버 PATH 에서 codex CLI 를 찾을 수 없습니다"
		} else if rt := newCodexRuntime(env); rt == nil {
			missing[aireport.DefaultRuntimeKey] = "Codex 전용 홈(CCTRACE_AI_CODEX_HOME)을 준비하지 못했습니다"
		} else {
			rts = append(rts, rt)
		}
	}
	rts = append(rts,
		openairuntime.New(openairuntime.Config{APIKey: kr.APIKey(aireport.ProviderOpenAI)}),
		clauderuntime.New(clauderuntime.Config{APIKey: kr.APIKey(aireport.ProviderAnthropic)}),
		// One implementation serves both: they differ only by base URL and key,
		// so RuntimeKey is what makes each answer to its own name.
		//
		// NVIDIA is built but not selectable: aireport.Unimplemented refuses it
		// wherever a runtime is chosen, because this path does not work end to
		// end yet (chatruntime echoes the assistant content the gateway returned
		// and vLLM then refuses the turn; the models measured there skip the
		// tools or ignore the output schema). It stays registered so the screen
		// can account for it and so re-enabling is one map entry, not a rebuild.
		chatruntime.New(chatruntime.Config{
			APIKey:       kr.APIKey(aireport.ProviderNVIDIA),
			RuntimeKey:   aireport.RuntimeNVIDIA,
			BaseURL:      func(context.Context) (string, error) { return nvidiaBaseURL, nil },
			ProviderName: aireport.ProviderNVIDIA,
		}),
	)
	// LiteLLM is self-hosted and its address is the one setting an admin types
	// in. It is registered whatever the environment says, because the address
	// can arrive later through the screen; Status reports the missing half
	// until one is set.
	rts = append(rts, chatruntime.New(chatruntime.Config{
		APIKey:       kr.APIKey(aireport.ProviderLiteLLM),
		RuntimeKey:   aireport.RuntimeLiteLLM,
		BaseURL:      savedBaseURL(st, aireport.RuntimeLiteLLM, env.LiteLLMBaseURL),
		ProviderName: aireport.ProviderLiteLLM,
	}))
	return rts, missing
}

// newAIReportService returns nil when st lacks the AI report tables' methods,
// in which case every AI report route answers runtime_unconfigured.
func newAIReportService(st any, env aiEnv) *aireport.Service {
	aiStore, ok := st.(aireport.Store)
	if !ok {
		log.Printf("[cctraced] store has no AI report storage; AI report routes answer runtime_unconfigured")
		return nil
	}
	if env.Runtime != "" && !slices.Contains(aireport.RuntimeKeys, env.Runtime) {
		log.Printf("[cctraced] CCTRACE_AI_RUNTIME_DEFAULT=%q is not a known runtime; no AI runtime is active until an admin chooses one", env.Runtime)
	}
	// Every provider the keyring names in ProviderEnvVars belongs here: the map
	// is what actually carries the value. Leaving one out shows the runtime as
	// "API key not configured" while the screen still says the variable manages
	// it -- the environment is read, just never handed over.
	kr := aireport.NewSourcedKeyring(aiStore, env.SecretsKey, env.SecretsSource, map[string]string{
		aireport.ProviderOpenAI:    env.OpenAIAPIKey,
		aireport.ProviderAnthropic: env.AnthropicAPIKey,
		aireport.ProviderNVIDIA:    env.NVIDIAAPIKey,
		aireport.ProviderLiteLLM:   env.LiteLLMAPIKey,
	})
	if reason := kr.SecretsReason(); reason != "" {
		log.Printf("[cctraced] WARNING: API keys cannot be registered in Admin > AI: CCTRACE_SECRETS_KEY (or JWT_SECRET when unset) must be at least %d bytes", aireport.MinSecretBytes)
	}
	rts, missing := aiRuntimesFromEnv(env, kr, aiStore)
	for key, reason := range missing {
		log.Printf("[cctraced] AI runtime %s not built: %s", key, reason)
	}
	return aireport.NewService(aiStore, rts, aireport.Config{
		EnvRuntime: env.Runtime,
		EnvEnabled: env.Enabled,
		Env: map[string]aireport.RuntimeEnv{
			aireport.DefaultRuntimeKey: {Model: env.Model, ReasoningEffort: env.ReasoningEffort},
			aireport.RuntimeOpenAI:     {Model: env.OpenAIModel},
			aireport.RuntimeClaude:     {Model: env.ClaudeModel},
			aireport.RuntimeNVIDIA:     {Model: env.NVIDIAModel},
			aireport.RuntimeLiteLLM:    {Model: env.LiteLLMModel},
		},
		Missing:       missing,
		Keyring:       kr,
		MaxConcurrent: env.MaxConcurrent,
		WallClock:     env.WallClock,
		DefaultTZ:     env.DefaultTZ,
	})
}

// newAccessLogger writes to stdout + an append-only file at path (host-mounted)
// so entries survive container recreates.
//
// It returns the open file so a caller can close it. The daemon never does --
// the logger lives as long as the process -- but a test that opens one has to,
// because Windows refuses to delete a file that still has a handle on it.
// Returns a nil closer when it falls back to stdout.
func newAccessLogger(path string) (*log.Logger, io.Closer) {
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("[cctraced] access log file open failed (%v); falling back to stdout only", err)
		return log.New(os.Stdout, "", log.LstdFlags), nil
	}
	return log.New(io.MultiWriter(os.Stdout, f), "", log.LstdFlags), f
}

// accessLogger is built on first request, not at import. As a package-level var
// initializer it ran before main, so `cctraced --help` and `--version` created
// /data/logs and printed an open-failure warning ahead of their own output --
// the binary could not answer a question without touching the filesystem.
var accessLogger = sync.OnceValue(func() *log.Logger {
	logger, _ := newAccessLogger(envOr("ACCESS_LOG_FILE", "/data/logs/access.log"))
	return logger
})

// accessLog logs interesting HTTP requests (API + downloads). Static SPA
// assets are skipped to avoid log spam.
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rec, r)
		path := r.URL.Path
		if !strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/downloads/") {
			return
		}
		remote := r.Header.Get("X-Forwarded-For")
		if remote == "" {
			remote = r.RemoteAddr
		}
		accessLogger().Printf("[access] %s %s %d %dms %s", r.Method, path, rec.status, time.Since(start).Milliseconds(), remote)
	})
}

func newGRPCServer(authn *auth.Authenticator, logs collogspb.LogsServiceServer, metrics colmetricspb.MetricsServiceServer, traces coltracepb.TraceServiceServer) *grpc.Server {
	var opts []grpc.ServerOption
	if authn.Enabled() {
		opts = append(opts, grpc.UnaryInterceptor(authn.GRPCUnaryInterceptor()))
	}
	server := grpc.NewServer(opts...)
	coltracepb.RegisterTraceServiceServer(server, traces)
	colmetricspb.RegisterMetricsServiceServer(server, metrics)
	collogspb.RegisterLogsServiceServer(server, logs)
	return server
}
