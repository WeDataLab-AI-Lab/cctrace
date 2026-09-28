package aireport

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

// loginCloser is a runtime that manages accounts; only CloseLogins is called.
type loginCloser struct {
	*airuntime.FakeRuntime
	airuntime.AccountManager
	closed bool
}

func (l *loginCloser) CloseLogins() { l.closed = true }

func TestAccountsNeedsAccountManager(t *testing.T) {
	for name, rt := range map[string]airuntime.Runtime{"nil": nil, "no manager": &airuntime.FakeRuntime{}} {
		if _, err := NewService(NewMemStore(), []airuntime.Runtime{rt}, Config{}).Accounts(); !errors.Is(err, airuntime.ErrNotConfigured) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	rt := &loginCloser{FakeRuntime: &airuntime.FakeRuntime{}}
	if m, err := NewService(NewMemStore(), []airuntime.Runtime{rt}, Config{}).Accounts(); err != nil || m == nil {
		t.Fatalf("Accounts = %v, %v", m, err)
	}
}

// A pending device login holds a process; shutdown must end it.
func TestShutdownClosesLogins(t *testing.T) {
	rt := &loginCloser{FakeRuntime: &airuntime.FakeRuntime{}}
	NewService(NewMemStore(), []airuntime.Runtime{rt}, Config{}).Shutdown(context.Background())
	if !rt.closed {
		t.Fatal("Shutdown did not close pending logins")
	}
}

// accountRuntime is a report runtime whose account changes are scripted.
// entered, when set, receives once LoginAPIKey is running; gate holds it there.
type accountRuntime struct {
	*airuntime.FakeRuntime
	mu      sync.Mutex
	login   airuntime.DeviceLogin
	changes int
	entered chan struct{}
	gate    chan struct{}
}

func (a *accountRuntime) Account(context.Context) (airuntime.Account, error) {
	return airuntime.Account{}, nil
}

func (a *accountRuntime) StartDeviceLogin(context.Context) (airuntime.DeviceLogin, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.changes++
	a.login = airuntime.DeviceLogin{ID: "login-1", State: airuntime.LoginPending}
	return a.login, nil
}

func (a *accountRuntime) DeviceLogin(id string) (airuntime.DeviceLogin, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id != a.login.ID {
		return airuntime.DeviceLogin{}, airuntime.ErrLoginNotFound
	}
	return a.login, nil
}

func (a *accountRuntime) CancelDeviceLogin(_ context.Context, id string) (airuntime.DeviceLogin, error) {
	a.setLoginState(airuntime.LoginCanceled)
	return a.DeviceLogin(id)
}

func (a *accountRuntime) setLoginState(state string) {
	a.mu.Lock()
	a.login.State = state
	a.mu.Unlock()
}

func (a *accountRuntime) LoginAPIKey(context.Context, string) error {
	a.mu.Lock()
	a.changes++
	a.mu.Unlock()
	if a.entered != nil {
		a.entered <- struct{}{}
		<-a.gate
	}
	return nil
}

func (a *accountRuntime) Logout(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.changes++
	return nil
}

func (a *accountRuntime) CloseLogins() {}

func newAccountRuntime(steps ...airuntime.FakeStep) *accountRuntime {
	return &accountRuntime{FakeRuntime: readyRuntime(steps, goodOutput)}
}

// A change swaps the login under a run that is already using the same home,
// so it waits for no run: it is refused while one is running.
func TestAccountChangeRefusedWhileRunRunning(t *testing.T) {
	rt := newAccountRuntime(airuntime.FakeStep{WaitForCancel: true})
	f := newFixture(t, rt)
	run, err := f.svc.Start(context.Background(), f.sc, f.wk)
	if err != nil {
		t.Fatal(err)
	}
	m, err := f.svc.Accounts()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.StartDeviceLogin(context.Background()); !errors.Is(err, ErrRunInProgress) {
		t.Errorf("device login err = %v", err)
	}
	if err := m.LoginAPIKey(context.Background(), "sk-x"); !errors.Is(err, ErrRunInProgress) {
		t.Errorf("api key err = %v", err)
	}
	if err := m.Logout(context.Background()); !errors.Is(err, ErrRunInProgress) {
		t.Errorf("logout err = %v", err)
	}
	if rt.changes != 0 {
		t.Fatalf("refused changes reached the runtime: %d", rt.changes)
	}

	if err := f.svc.Cancel(context.Background(), f.sc, run.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for err := m.Logout(context.Background()); err != nil; err = m.Logout(context.Background()) {
		if time.Now().After(deadline) {
			t.Fatalf("logout after the run ended: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A run started during a device login would use whichever login the code ends
// in, so none starts until the login ends.
func TestRunRefusedWhileDeviceLoginPending(t *testing.T) {
	rt := newAccountRuntime()
	f := newFixture(t, rt)
	m, _ := f.svc.Accounts()
	if _, err := m.StartDeviceLogin(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Start(context.Background(), f.sc, f.wk); !errors.Is(err, ErrAccountChanging) {
		t.Fatalf("start during login err = %v", err)
	}
	rt.setLoginState(airuntime.LoginSucceeded)
	if _, err := f.svc.Start(context.Background(), f.sc, f.wk); err != nil {
		t.Fatalf("start after login: %v", err)
	}
}

func TestRunRefusedWhileAccountChangeRuns(t *testing.T) {
	rt := newAccountRuntime()
	rt.entered, rt.gate = make(chan struct{}), make(chan struct{})
	f := newFixture(t, rt)
	m, _ := f.svc.Accounts()
	done := make(chan error)
	go func() { done <- m.LoginAPIKey(context.Background(), "sk-x") }()
	<-rt.entered
	if _, err := f.svc.Start(context.Background(), f.sc, f.wk); !errors.Is(err, ErrAccountChanging) {
		t.Fatalf("start during key login err = %v", err)
	}
	close(rt.gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Start(context.Background(), f.sc, f.wk); err != nil {
		t.Fatalf("start after key login: %v", err)
	}
}

// stuckCloser's CloseLogins returns only when released.
type stuckCloser struct {
	*airuntime.FakeRuntime
	airuntime.AccountManager
	release chan struct{}
}

func (s *stuckCloser) CloseLogins() { <-s.release }

// Shutdown's deadline bounds the login teardown too, not only the runs.
func TestShutdownDoesNotWaitPastDeadlineForLogins(t *testing.T) {
	rt := &stuckCloser{FakeRuntime: &airuntime.FakeRuntime{}, release: make(chan struct{})}
	defer close(rt.release)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() {
		NewService(NewMemStore(), []airuntime.Runtime{rt}, Config{}).Shutdown(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown waited on CloseLogins past its deadline")
	}
}
