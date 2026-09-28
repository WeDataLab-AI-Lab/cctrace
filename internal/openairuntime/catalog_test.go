package openairuntime

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cctrace/internal/airuntime"
)

const modelsBody = `{"object":"list","data":[{"id":"gpt-6-astra","object":"model"},{"id":"whisper-1","object":"model"},{"id":"gpt-5.6-terra","object":"model"}]}`

func TestModelsCrossesCatalog(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if r.Method != http.MethodGet || r.URL.Path != "/models" {
			return 404, `{}`
		}
		return 200, modelsBody
	})
	models, err := newTestRuntime(f.srv.URL, testKey, nil).Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 2 || models[0].ID != "gpt-6-astra" || models[1].ID != "gpt-5.6-terra" {
		t.Fatalf("models = %+v", models)
	}
	terra, astra := models[1], models[0]
	if !terra.IsDefault || astra.IsDefault || terra.DefaultReasoningEffort != "medium" {
		t.Fatalf("defaults: terra %+v astra %+v", terra, astra)
	}
	if !terra.SupportsEffort("none") || astra.SupportsEffort("none") || !astra.SupportsEffort("max") {
		t.Fatalf("efforts: terra %+v astra %+v", terra.SupportedReasoningEfforts, astra.SupportedReasoningEfforts)
	}
	if f.authAt(0) != "Bearer "+testKey {
		t.Fatalf("auth = %q", f.authAt(0))
	}
}

func TestModelsCache(t *testing.T) {
	var status atomic.Int32
	status.Store(200)
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if s := int(status.Load()); s != 200 {
			return s, `{}`
		}
		return 200, modelsBody
	})
	rt := newTestRuntime(f.srv.URL, testKey, nil)
	now := time.Unix(1_000_000, 0)
	rt.now = func() time.Time { return now }
	ctx := context.Background()

	mustModels := func() {
		t.Helper()
		if _, err := rt.Models(ctx); err != nil {
			t.Fatalf("Models: %v", err)
		}
	}
	mustModels()
	now = now.Add(9 * time.Minute)
	mustModels()
	if f.count("/models") != 1 {
		t.Fatalf("fetches = %d within 10 minutes, want 1", f.count("/models"))
	}
	now = now.Add(2 * time.Minute)
	status.Store(500)
	if _, err := rt.Models(ctx); !errors.Is(err, airuntime.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	status.Store(200)
	now = now.Add(30 * time.Second)
	if _, err := rt.Models(ctx); err == nil || f.count("/models") != 2 {
		t.Fatalf("failure not cached: err %v, fetches %d", err, f.count("/models"))
	}
	now = now.Add(31 * time.Second)
	mustModels()
	if f.count("/models") != 3 {
		t.Fatalf("fetches = %d, want 3", f.count("/models"))
	}
}

func TestModelsCacheFollowsKey(t *testing.T) {
	f := newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		if r.Header.Get("Authorization") != "Bearer "+testKey {
			return 401, `{}`
		}
		return 200, modelsBody
	})
	key := "sk-old-key"
	rt := New(Config{APIKey: func(context.Context) (string, error) { return key, nil }, BaseURL: f.srv.URL})
	if _, err := rt.Models(context.Background()); !errors.Is(err, airuntime.ErrNotLoggedIn) {
		t.Fatalf("old key err = %v", err)
	}
	key = testKey
	if _, err := rt.Models(context.Background()); err != nil {
		t.Fatalf("new key: %v", err)
	}
}

func TestModelsErrors(t *testing.T) {
	for _, tc := range []struct {
		status   int
		sentinel error
	}{{401, airuntime.ErrNotLoggedIn}, {403, airuntime.ErrUnavailable}, {500, airuntime.ErrUnavailable}, {429, airuntime.ErrUnavailable}} {
		f := newFake(t, func(*http.Request, int, map[string]any) (int, string) {
			return tc.status, `{"error":{"message":"RAWBODY"}}`
		})
		_, err := newTestRuntime(f.srv.URL, testKey, nil).Models(context.Background())
		if !errors.Is(err, tc.sentinel) {
			t.Fatalf("%d: err = %v, want %v", tc.status, err, tc.sentinel)
		}
	}
	f := newFake(t, func(*http.Request, int, map[string]any) (int, string) { return 200, modelsBody })
	f.srv.Close()
	if _, err := newTestRuntime(f.srv.URL, testKey, nil).Models(context.Background()); !errors.Is(err, airuntime.ErrUnavailable) {
		t.Fatalf("network err = %v", err)
	}
	if _, err := newTestRuntime(f.srv.URL, "", nil).Models(context.Background()); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("no key err = %v", err)
	}
}

// slowModels answers /models once release is closed.
func slowModels(t *testing.T, release <-chan struct{}) *fakeAPI {
	return newFake(t, func(r *http.Request, n int, body map[string]any) (int, string) {
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		return 200, modelsBody
	})
}

func TestModelsWaitFollowsCallerContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	rt := newTestRuntime(slowModels(t, release).srv.URL, testKey, nil)
	go func() { _, _ = rt.Models(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	st := rt.Status(ctx)
	if waited := time.Since(start); waited > time.Second || st.Available {
		t.Fatalf("status %+v after %s, want unavailable at the caller's 100ms deadline", st, waited)
	}
}

func TestModelsConcurrentCallersShareOneFetch(t *testing.T) {
	release := make(chan struct{})
	f := slowModels(t, release)
	rt := newTestRuntime(f.srv.URL, testKey, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := rt.Models(context.Background())
			errs <- err
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Models: %v", err)
		}
	}
	if got := f.count("/models"); got != 1 {
		t.Fatalf("fetches = %d, want 1", got)
	}
}

func TestModelsFetchHasItsOwnTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	rt := newTestRuntime(slowModels(t, release).srv.URL, testKey, nil)
	rt.modelsTimeout = 100 * time.Millisecond
	start := time.Now()
	if _, err := rt.Models(context.Background()); !errors.Is(err, airuntime.ErrUnavailable) || time.Since(start) > time.Second {
		t.Fatalf("err = %v after %s, want ErrUnavailable at the 100ms fetch timeout", err, time.Since(start))
	}
}

func TestModelsReturnsCopies(t *testing.T) {
	f := newFake(t, func(*http.Request, int, map[string]any) (int, string) { return 200, modelsBody })
	rt := newTestRuntime(f.srv.URL, testKey, nil)
	first, err := rt.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	first[0].DisplayName = "changed"
	first[0].SupportedReasoningEfforts[0].ReasoningEffort = "changed"
	again, _ := rt.Models(context.Background())
	if again[0].DisplayName == "changed" || again[0].SupportedReasoningEfforts[0].ReasoningEffort == "changed" {
		t.Fatalf("cached catalog changed through a returned slice: %+v", again[0])
	}
}

func TestStatusNeedsAUsableDefaultModel(t *testing.T) {
	for name, body := range map[string]string{
		"no catalog model": `{"object":"list","data":[{"id":"whisper-1","object":"model"}]}`,
		"no default model": `{"object":"list","data":[{"id":"gpt-6-astra","object":"model"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(*http.Request, int, map[string]any) (int, string) { return 200, body })
			if st := newTestRuntime(f.srv.URL, testKey, nil).Status(context.Background()); !st.Configured || st.Available || st.Reason == "" {
				t.Fatalf("status = %+v, want configured but unavailable with a reason", st)
			}
		})
	}
}

func TestStatus(t *testing.T) {
	ok := newFake(t, func(*http.Request, int, map[string]any) (int, string) { return 200, modelsBody })
	if st := newTestRuntime(ok.srv.URL, "", nil).Status(context.Background()); st.Configured || st.Available || st.Reason == "" {
		t.Fatalf("no key status = %+v", st)
	}
	if st := newTestRuntime(ok.srv.URL, testKey, nil).Status(context.Background()); !st.Configured || !st.Available || st.AccountEmail != "" || st.PlanType != "" {
		t.Fatalf("ok status = %+v", st)
	}
	denied := newFake(t, func(*http.Request, int, map[string]any) (int, string) { return 401, `{}` })
	if st := newTestRuntime(denied.srv.URL, testKey, nil).Status(context.Background()); !st.Configured || st.Available || st.Reason == "" {
		t.Fatalf("denied status = %+v", st)
	}
}
