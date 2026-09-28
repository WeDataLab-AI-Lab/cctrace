package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"

	"cctrace/internal/airuntime"
)

var _ airuntime.AccountManager = (*CodexRuntime)(nil)

// deviceLoginTimeout is how long a device code login waits for the code to be
// entered before it expires and its process is ended.
var deviceLoginTimeout = 15 * time.Minute

// changeTimeout bounds an account change's own process: start, initialize, and
// the change's call. It is kept apart from fetchTimeout, the status read's
// bound, so shortening one cannot starve the other of time to start a process.
var changeTimeout = 20 * time.Second

// cancelLoginGrace bounds the account/login/cancel call made before the kill.
const cancelLoginGrace = 2 * time.Second

// loginSession is the latest device login. Only watchLogin writes its state
// after it is published; cancel is closed at most once to ask it to stop.
type loginSession struct {
	login      airuntime.DeviceLogin
	cancel     chan struct{}
	cancelOnce sync.Once
	done       chan struct{}
}

func (s *loginSession) stop() { s.cancelOnce.Do(func() { close(s.cancel) }) }

// accountState is CodexRuntime's login bookkeeping. changeMu serializes the
// account changes, so a guard's answer still holds when the change runs.
type accountState struct {
	changeMu sync.Mutex
	loginMu  sync.Mutex
	session  *loginSession
}

// invalidateHome drops the cached status and catalog, which describe the
// login that was just replaced.
func invalidateHome(home string) {
	cacheMu.Lock()
	delete(cache, home)
	cacheMu.Unlock()
	modelCacheMu.Lock()
	delete(modelCache, home)
	modelCacheMu.Unlock()
}

// Account reads the login from the home's files plus the cached status, so it
// costs no more processes than Status does.
func (r *CodexRuntime) Account(ctx context.Context) (airuntime.Account, error) {
	if r.cfg.Home == "" {
		return airuntime.Account{}, fmt.Errorf("%w: codex home is not set", airuntime.ErrNotConfigured)
	}
	acct := airuntime.Account{
		AuthMode:          AuthMode(r.cfg.Home, r.cfg.APIKey),
		EnvManaged:        r.cfg.APIKey != "",
		AuthFileIsSymlink: authFileIsLinked(r.cfg.Home),
		Login:             r.pendingLogin(),
	}
	if acct.AuthMode != airuntime.AuthModeNone {
		st := r.Status(ctx)
		acct.Email, acct.PlanType = st.AccountEmail, st.PlanType
	}
	return acct, nil
}

func (r *CodexRuntime) pendingLogin() *airuntime.DeviceLogin {
	r.acct.loginMu.Lock()
	defer r.acct.loginMu.Unlock()
	if s := r.acct.session; s != nil && s.login.State == airuntime.LoginPending {
		l := s.login
		return &l
	}
	return nil
}

// guardChange refuses a change the environment owns, one that would write
// through a linked login file, and one that would race a pending login.
func (r *CodexRuntime) guardChange() error {
	switch {
	case r.cfg.Home == "":
		return fmt.Errorf("%w: codex home is not set", airuntime.ErrNotConfigured)
	case r.cfg.APIKey != "":
		return airuntime.ErrEnvManaged
	case authFileIsLinked(r.cfg.Home):
		return airuntime.ErrAuthFileIsSymlink
	case r.pendingLogin() != nil:
		return airuntime.ErrLoginInProgress
	}
	return nil
}

// loginError wraps a failure without the key: a server that repeats its input
// in an error message would otherwise carry it to the API response and logs.
func loginError(sentinel, err error, key string) error {
	msg := err.Error()
	if key != "" {
		msg = strings.ReplaceAll(msg, key, "[redacted]")
	}
	return fmt.Errorf("%w: %s", sentinel, msg)
}

// server is one app-server process with an initialized connection.
type server struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	conn  *conn
}

func startServer(ctx context.Context, home string) (*server, error) {
	bin, err := lookPathFn("codex")
	if err != nil {
		return nil, fmt.Errorf("codex CLI not found on PATH: %w", err)
	}
	cmd := exec.Command(bin, "app-server")
	cmd.Env = childEnv(RuntimeConfig{Home: home}, "")
	cmd.Stderr = nil
	setProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start codex app-server: %w", err)
	}
	s := &server{cmd: cmd, stdin: stdin, conn: newConn(stdin, stdout, defaultMaxLineBytes, maxResponseBytes)}
	if _, err := s.conn.Call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": clientName, "version": clientVersion},
	}); err != nil {
		s.close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := s.conn.Notify("initialized", nil); err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

// close is Fetch's teardown: kill the group before reaping.
func (s *server) close() {
	s.conn.Close()
	_ = s.stdin.Close()
	killGroup(s.cmd.Process)
	_ = s.cmd.Wait()
}

type loginCompleted struct {
	LoginID *string `json:"loginId"`
	Success bool    `json:"success"`
	Error   *string `json:"error"`
}

// nextLoginCompleted skips other traffic until account/login/completed. ok is
// false when the stream ended first.
func nextLoginCompleted(m rpcMessage, c *conn) (loginCompleted, bool) {
	var p loginCompleted
	if m.Method != "account/login/completed" {
		if len(m.ID) > 0 {
			_ = c.RespondError(m.ID, -32601, "cctrace does not handle "+m.Method)
		}
		return p, false
	}
	return p, json.Unmarshal(m.Params, &p) == nil
}

func (p loginCompleted) message() string {
	if p.Error != nil && *p.Error != "" {
		return *p.Error
	}
	return "login failed"
}

// StartDeviceLogin starts a process that stays up until the login ends: the
// server completes the login itself once the code is entered.
func (r *CodexRuntime) StartDeviceLogin(ctx context.Context) (airuntime.DeviceLogin, error) {
	r.acct.changeMu.Lock()
	defer r.acct.changeMu.Unlock()
	if err := r.guardChange(); err != nil {
		return airuntime.DeviceLogin{}, err
	}
	callCtx, cancel := context.WithTimeout(ctx, changeTimeout)
	defer cancel()
	s, err := startServer(callCtx, r.cfg.Home)
	if err != nil {
		return airuntime.DeviceLogin{}, loginError(airuntime.ErrLoginFailed, err, "")
	}
	raw, err := s.conn.Call(callCtx, "account/login/start", map[string]any{"type": "chatgptDeviceCode"})
	if err != nil {
		s.close()
		return airuntime.DeviceLogin{}, loginError(airuntime.ErrLoginFailed, err, "")
	}
	var resp struct {
		LoginID         string `json:"loginId"`
		VerificationURL string `json:"verificationUrl"`
		UserCode        string `json:"userCode"`
	}
	if json.Unmarshal(raw, &resp) != nil || resp.LoginID == "" || resp.UserCode == "" {
		s.close()
		return airuntime.DeviceLogin{}, fmt.Errorf("%w: no device code in the reply", airuntime.ErrLoginFailed)
	}
	sess := &loginSession{
		login: airuntime.DeviceLogin{
			ID: resp.LoginID, VerificationURL: resp.VerificationURL, UserCode: resp.UserCode,
			ExpiresAt: nowFn().Add(deviceLoginTimeout), State: airuntime.LoginPending,
		},
		cancel: make(chan struct{}),
		done:   make(chan struct{}),
	}
	// Copied before the watcher starts: it may write the state at once.
	started := sess.login
	r.acct.loginMu.Lock()
	r.acct.session = sess
	r.acct.loginMu.Unlock()
	go r.watchLogin(sess, s, deviceLoginTimeout)
	return started, nil
}

// watchLogin owns the login's process until the login ends, and reaps it
// before the end is published and done is closed.
func (r *CodexRuntime) watchLogin(sess *loginSession, s *server, timeout time.Duration) {
	defer close(sess.done)
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	// code is what the screen sees; the server's own text goes to the log.
	state, code := "", ""
	for state == "" {
		select {
		case m, ok := <-s.conn.Incoming():
			if !ok {
				state, code = airuntime.LoginFailed, airuntime.LoginErrorRuntimeExited
				log.Printf("[codexappserver] device login %s: codex app-server exited before the login completed", sess.login.ID)
				continue
			}
			p, ok := nextLoginCompleted(m, s.conn)
			if !ok || (p.LoginID != nil && *p.LoginID != sess.login.ID) {
				continue
			}
			switch {
			// guardChange looked when the login started; the file may have
			// become a link since, and the login then wrote through it.
			case p.Success && authFileIsLinked(r.cfg.Home):
				state, code = airuntime.LoginFailed, airuntime.LoginErrorAuthFileLinked
				log.Printf("[codexappserver] device login %s: auth.json became a link before the login completed", sess.login.ID)
			case p.Success:
				state = airuntime.LoginSucceeded
			default:
				state, code = airuntime.LoginFailed, airuntime.LoginErrorFailed
				log.Printf("[codexappserver] device login %s failed: %s", sess.login.ID, p.message())
			}
		case <-timer.C:
			state = airuntime.LoginExpired
			cancelLogin(s, sess.login.ID)
		case <-sess.cancel:
			state = airuntime.LoginCanceled
			cancelLogin(s, sess.login.ID)
		}
	}
	// Before the state is published, so whoever sees the login end can start
	// the next change without a process still holding the home, and sees the
	// status that follows it.
	s.close()
	invalidateHome(r.cfg.Home)
	r.acct.loginMu.Lock()
	sess.login.State, sess.login.Error = state, code
	r.acct.loginMu.Unlock()
}

// cancelLogin asks the server to drop the login before the process is killed,
// so nothing it holds is left half-written.
func cancelLogin(s *server, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), cancelLoginGrace)
	defer cancel()
	_, _ = s.conn.Call(ctx, "account/login/cancel", map[string]any{"loginId": id})
}

func (r *CodexRuntime) DeviceLogin(id string) (airuntime.DeviceLogin, error) {
	r.acct.loginMu.Lock()
	defer r.acct.loginMu.Unlock()
	if s := r.acct.session; s != nil && s.login.ID == id {
		return s.login, nil
	}
	return airuntime.DeviceLogin{}, airuntime.ErrLoginNotFound
}

// CancelDeviceLogin ends a pending login and waits for its process to be
// reaped. A login that already ended is returned as it ended.
func (r *CodexRuntime) CancelDeviceLogin(ctx context.Context, id string) (airuntime.DeviceLogin, error) {
	r.acct.loginMu.Lock()
	sess := r.acct.session
	r.acct.loginMu.Unlock()
	if sess == nil || sess.login.ID != id {
		return airuntime.DeviceLogin{}, airuntime.ErrLoginNotFound
	}
	sess.stop()
	select {
	case <-sess.done:
	case <-ctx.Done():
		return airuntime.DeviceLogin{}, ctx.Err()
	}
	return r.DeviceLogin(id)
}

func (r *CodexRuntime) CloseLogins() {
	r.acct.loginMu.Lock()
	sess := r.acct.session
	r.acct.loginMu.Unlock()
	if sess == nil {
		return
	}
	sess.stop()
	<-sess.done
}

// LoginAPIKey has the server store the key as the home's login. The key goes
// to the child's stdin only.
func (r *CodexRuntime) LoginAPIKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("api key is empty")
	}
	r.acct.changeMu.Lock()
	defer r.acct.changeMu.Unlock()
	if err := r.guardChange(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, changeTimeout)
	defer cancel()
	s, err := startServer(ctx, r.cfg.Home)
	if err != nil {
		return loginError(airuntime.ErrLoginFailed, err, key)
	}
	defer s.close()
	if _, err := s.conn.Call(ctx, "account/login/start", map[string]any{"type": "apiKey", "apiKey": key}); err != nil {
		return loginError(airuntime.ErrLoginFailed, err, key)
	}
	for {
		select {
		case m, ok := <-s.conn.Incoming():
			if !ok {
				return fmt.Errorf("%w: codex app-server exited before the login completed", airuntime.ErrLoginFailed)
			}
			p, ok := nextLoginCompleted(m, s.conn)
			if !ok {
				continue
			}
			if !p.Success {
				return loginError(airuntime.ErrLoginFailed, errors.New(p.message()), key)
			}
			invalidateHome(r.cfg.Home)
			return nil
		case <-ctx.Done():
			return fmt.Errorf("%w: codex app-server did not complete the login within %s", airuntime.ErrLoginFailed, changeTimeout)
		}
	}
}

// Logout has the server remove the home's login. Report runs stop until
// someone logs in again.
func (r *CodexRuntime) Logout(ctx context.Context) error {
	r.acct.changeMu.Lock()
	defer r.acct.changeMu.Unlock()
	if err := r.guardChange(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, changeTimeout)
	defer cancel()
	s, err := startServer(ctx, r.cfg.Home)
	if err != nil {
		return fmt.Errorf("%w: %v", airuntime.ErrUnavailable, err)
	}
	defer s.close()
	if _, err := s.conn.Call(ctx, "account/logout", nil); err != nil {
		return fmt.Errorf("%w: account/logout: %v", airuntime.ErrUnavailable, err)
	}
	invalidateHome(r.cfg.Home)
	return nil
}
