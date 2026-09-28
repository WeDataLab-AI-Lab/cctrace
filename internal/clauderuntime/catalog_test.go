package clauderuntime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"cctrace/internal/airuntime"
)

const testKey = "sk-ant-test-SECRET-0123456789"

func newTestRuntime(t *testing.T, h http.HandlerFunc) (*Runtime, *bytes.Buffer) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	logs := &bytes.Buffer{}
	logger := log.New(logs, "", 0)
	r := New(Config{
		APIKey:  func(context.Context) (string, error) { return testKey, nil },
		BaseURL: srv.URL,
		Logf:    logger.Printf,
	})
	return r, logs
}

const modelsPage = `{"data":[
{"type":"model","id":"claude-sonnet-5","display_name":"Claude Sonnet 5"},
{"type":"model","id":"claude-haiku-4-5-20251001","display_name":"Claude Haiku 4.5"},
{"type":"model","id":"claude-unlisted-9","display_name":"Unlisted"}],"has_more":false,"last_id":"claude-unlisted-9"}`

func TestModelsCrossesAPIWithCuratedTable(t *testing.T) {
	var calls atomic.Int32
	r, _ := newTestRuntime(t, func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.URL.Path != "/v1/models" || req.Header.Get("x-api-key") != testKey || req.Header.Get("anthropic-version") == "" {
			t.Errorf("bad request %s headers %v", req.URL.Path, req.Header)
		}
		fmt.Fprint(w, modelsPage)
	})
	models, err := r.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 {
		t.Fatalf("models = %+v, want sonnet-5 and haiku", models)
	}
	byID := map[string]airuntime.Model{}
	for _, m := range models {
		byID[m.ID] = m
	}
	sonnet := byID["claude-sonnet-5"]
	if !sonnet.IsDefault || sonnet.DisplayName != "Claude Sonnet 5" || !sonnet.SupportsEffort("xhigh") || sonnet.DefaultReasoningEffort != "high" {
		t.Errorf("sonnet = %+v", sonnet)
	}
	haiku := byID["claude-haiku-4-5-20251001"]
	if haiku.IsDefault || len(haiku.SupportedReasoningEfforts) != 0 || haiku.DefaultReasoningEffort != "" {
		t.Errorf("haiku = %+v, want no efforts", haiku)
	}
	if _, err := r.Models(context.Background()); err != nil || calls.Load() != 1 {
		t.Fatalf("second call err=%v calls=%d, want cached", err, calls.Load())
	}
}

func TestModelsPaginates(t *testing.T) {
	r, _ := newTestRuntime(t, func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Query().Get("after_id") == "" {
			fmt.Fprint(w, `{"data":[{"id":"claude-opus-5","display_name":"Claude Opus 5"}],"has_more":true,"last_id":"claude-opus-5"}`)
			return
		}
		fmt.Fprint(w, `{"data":[{"id":"claude-sonnet-5","display_name":"Claude Sonnet 5"}],"has_more":false}`)
	})
	models, err := r.Models(context.Background())
	if err != nil || len(models) != 2 {
		t.Fatalf("models=%+v err=%v", models, err)
	}
}

func TestModelsErrorsAndFailureCache(t *testing.T) {
	cases := []struct {
		status int
		want   error
	}{
		{401, airuntime.ErrNotLoggedIn},
		{403, airuntime.ErrUnavailable},
		{429, airuntime.ErrUnavailable},
		{500, airuntime.ErrUnavailable},
		{529, airuntime.ErrUnavailable},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			var calls atomic.Int32
			r, logs := newTestRuntime(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				fmt.Fprintf(w, `{"type":"error","error":{"type":"x","message":"bad key %s"}}`, testKey)
			})
			now := time.Now()
			r.now = func() time.Time { return now }
			_, err := r.Models(context.Background())
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if strings.Contains(err.Error(), testKey) || strings.Contains(logs.String(), testKey) {
				t.Fatalf("key leaked: err=%q logs=%q", err, logs)
			}
			_, _ = r.Models(context.Background())
			if calls.Load() != 1 {
				t.Fatalf("calls = %d, want failure cached", calls.Load())
			}
			now = now.Add(61 * time.Second)
			_, _ = r.Models(context.Background())
			if calls.Load() != 2 {
				t.Fatalf("calls = %d, want refetch after a minute", calls.Load())
			}
		})
	}
}

func TestModelsNetworkErrorIsUnavailable(t *testing.T) {
	r := New(Config{
		APIKey:  func(context.Context) (string, error) { return testKey, nil },
		BaseURL: "http://127.0.0.1:1",
	})
	if _, err := r.Models(context.Background()); !errors.Is(err, airuntime.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
}

func TestStatus(t *testing.T) {
	noKey := New(Config{APIKey: func(context.Context) (string, error) { return "", nil }})
	if st := noKey.Status(context.Background()); st.Configured || st.Available || st.Reason == "" {
		t.Fatalf("no key status = %+v", st)
	}
	if _, err := noKey.Models(context.Background()); !errors.Is(err, airuntime.ErrNotConfigured) {
		t.Fatalf("no key models err = %v", err)
	}

	ok, _ := newTestRuntime(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, modelsPage) })
	if st := ok.Status(context.Background()); !st.Configured || !st.Available {
		t.Fatalf("status = %+v", st)
	}

	bad, _ := newTestRuntime(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) })
	if st := bad.Status(context.Background()); !st.Configured || st.Available || st.Reason == "" {
		t.Fatalf("401 status = %+v", st)
	}
}

func TestStatusNeedsAUsableDefaultModel(t *testing.T) {
	for name, page := range map[string]string{
		"no curated model": `{"data":[{"id":"claude-unlisted-9","display_name":"Unlisted"}],"has_more":false}`,
		"no default model": `{"data":[{"id":"claude-haiku-4-5-20251001","display_name":"Claude Haiku 4.5"}],"has_more":false}`,
	} {
		t.Run(name, func(t *testing.T) {
			r, _ := newTestRuntime(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, page) })
			if st := r.Status(context.Background()); !st.Configured || st.Available || st.Reason == "" {
				t.Fatalf("status = %+v, want configured but unavailable with a reason", st)
			}
		})
	}
}

func TestInfo(t *testing.T) {
	info := New(Config{}).Info()
	if info.Key != "claude-api" || info.Model != "claude-sonnet-5" || info.AuthMode != airuntime.AuthModeAPIKey {
		t.Fatalf("info = %+v", info)
	}
}

// slowModelsHandler answers /v1/models once release is closed, counting calls.
func slowModelsHandler(release <-chan struct{}, calls *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
		fmt.Fprint(w, modelsPage)
	}
}

func TestModelsWaitFollowsCallerContext(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	r, _ := newTestRuntime(t, slowModelsHandler(release, &calls))
	go func() { _, _ = r.Models(context.Background()) }()
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	st := r.Status(ctx)
	if waited := time.Since(start); waited > time.Second || st.Available {
		t.Fatalf("status %+v after %s, want unavailable at the caller's 100ms deadline", st, waited)
	}
}

func TestModelsConcurrentCallersShareOneFetch(t *testing.T) {
	release := make(chan struct{})
	var calls atomic.Int32
	r, _ := newTestRuntime(t, slowModelsHandler(release, &calls))
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := r.Models(context.Background())
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
	if calls.Load() != 1 {
		t.Fatalf("fetches = %d, want 1", calls.Load())
	}
}

func TestModelsFetchHasItsOwnTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int32
	r, _ := newTestRuntime(t, slowModelsHandler(release, &calls))
	r.modelsTimeout = 100 * time.Millisecond
	start := time.Now()
	if _, err := r.Models(context.Background()); !errors.Is(err, airuntime.ErrUnavailable) || time.Since(start) > time.Second {
		t.Fatalf("err = %v after %s, want ErrUnavailable at the 100ms fetch timeout", err, time.Since(start))
	}
}

func TestModelsReturnsCopies(t *testing.T) {
	r, _ := newTestRuntime(t, func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, modelsPage) })
	first, err := r.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	first[0].DisplayName = "changed"
	first[0].SupportedReasoningEfforts[0].ReasoningEffort = "changed"
	again, _ := r.Models(context.Background())
	if again[0].DisplayName == "changed" || again[0].SupportedReasoningEfforts[0].ReasoningEffort == "changed" {
		t.Fatalf("cached catalog changed through a returned slice: %+v", again[0])
	}
}

func TestModelsDoesNotCacheCallerCancel(t *testing.T) {
	var calls atomic.Int32
	r, _ := newTestRuntime(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, modelsPage)
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Models(ctx); err == nil {
		t.Fatal("canceled ctx succeeded")
	}
	if _, err := r.Models(context.Background()); err != nil {
		t.Fatalf("after cancel err = %v, want refetch", err)
	}
}

func TestModelsCacheFollowsKey(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		if req.Header.Get("x-api-key") != "new" {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, modelsPage)
	}))
	t.Cleanup(srv.Close)
	key := "old"
	r := New(Config{APIKey: func(context.Context) (string, error) { return key, nil }, BaseURL: srv.URL, Logf: func(string, ...any) {}})
	if _, err := r.Models(context.Background()); !errors.Is(err, airuntime.ErrNotLoggedIn) {
		t.Fatalf("old key err = %v", err)
	}
	key = "new"
	if _, err := r.Models(context.Background()); err != nil || calls.Load() != 2 {
		t.Fatalf("new key err=%v calls=%d", err, calls.Load())
	}
}

func TestKeyLookupFailureIsNotMissingKey(t *testing.T) {
	r := New(Config{APIKey: func(context.Context) (string, error) { return "", errors.New("db down") }})
	if st := r.Status(context.Background()); !st.Configured || st.Available {
		t.Fatalf("status = %+v", st)
	}
	if _, err := r.Models(context.Background()); !errors.Is(err, airuntime.ErrUnavailable) {
		t.Fatalf("models err = %v", err)
	}
}

func TestLogMasksKeyAtTruncationBoundary(t *testing.T) {
	r, logs := newTestRuntime(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		fmt.Fprint(w, strings.Repeat("x", maxLoggedBody-5)+testKey)
	})
	_, _ = r.Models(context.Background())
	if strings.Contains(logs.String(), testKey[:5]) {
		t.Fatalf("key prefix in logs: %q", logs)
	}
}

func TestLoggedBodyCutsOnRuneBoundary(t *testing.T) {
	if got := loggedBody([]byte("x"+strings.Repeat("가", maxLoggedBody)), testKey); !utf8.ValidString(got) || len(got) > maxLoggedBody {
		t.Fatalf("logged body is %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
}

func TestRedirectDoesNotForwardKey(t *testing.T) {
	var leaked atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("x-api-key") != "" {
			leaked.Store(true)
		}
		fmt.Fprint(w, modelsPage)
	}))
	t.Cleanup(other.Close)
	r, _ := newTestRuntime(t, func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, other.URL+req.URL.Path, http.StatusTemporaryRedirect)
	})
	_, err := r.Models(context.Background())
	if leaked.Load() || err == nil {
		t.Fatalf("leaked=%v err=%v", leaked.Load(), err)
	}
}
