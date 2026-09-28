package codexrates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// point makes the package fetch from srv instead of developers.openai.com.
func point(t *testing.T, srv *httptest.Server) {
	t.Helper()
	oldMD, oldHTML := markdownURL, htmlURL
	markdownURL, htmlURL = srv.URL+"/pricing.md", srv.URL+"/pricing"
	t.Cleanup(func() { markdownURL, htmlURL = oldMD, oldHTML })
}

func TestFetch_PrefersMarkdown(t *testing.T) {
	md := readFixture(t, "pricing.md")
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pricing.md" {
			t.Errorf("unexpected request for %s -- markdown should have been enough", r.URL.Path)
		}
		_, _ = w.Write([]byte(md))
	})
	point(t, srv)

	rates, source, err := Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if source != "openai-md" {
		t.Errorf("source = %q, want openai-md", source)
	}
	wantRate(t, rates, "gpt-6-astra", 10, 50, 1)
}

// The markdown endpoint is one URL away from being retired; the HTML page's
// hydration props are the fallback.
func TestFetch_FallsBackToAstro(t *testing.T) {
	html := readFixture(t, "pricing.html")
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pricing.md" {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(html))
	})
	point(t, srv)

	rates, source, err := Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if source != "openai-astro" {
		t.Errorf("source = %q, want openai-astro", source)
	}
	wantRate(t, rates, "gpt-5.6-sol", 4, 20, 0.4)
}

// A markdown body that no longer contains the standard table is a parse
// failure, not an empty table -- it must fall through, not win.
func TestFetch_FallsBackWhenMarkdownUnparseable(t *testing.T) {
	html := readFixture(t, "pricing.html")
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/pricing.md" {
			_, _ = w.Write([]byte("# Pricing\n\nSee the pricing page.\n"))
			return
		}
		_, _ = w.Write([]byte(html))
	})
	point(t, srv)

	_, source, err := Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if source != "openai-astro" {
		t.Errorf("source = %q, want openai-astro", source)
	}
}

func TestFetch_AllSourcesFail(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	point(t, srv)

	if _, _, err := Fetch(context.Background()); err == nil {
		t.Fatal("Fetch succeeded with every source failing")
	}
}

// The changelog is a separate address from the price table, and only the price
// table can stop the sync. A changelog that 500s leaves the rates intact; the
// caller then dates any change from today.
func TestFetchChangelog_FailureIsIndependentOfRates(t *testing.T) {
	md := readFixture(t, "pricing.md")
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/changelog.md" {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(md))
	})
	point(t, srv)
	oldChangelog := changelogURL
	changelogURL = srv.URL + "/changelog.md"
	t.Cleanup(func() { changelogURL = oldChangelog })

	if _, err := FetchChangelog(context.Background()); err == nil {
		t.Fatal("FetchChangelog succeeded against a 500")
	}
	rates, _, err := Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if err := Accept(rates); err != nil {
		t.Fatalf("Accept: %v", err)
	}
}
