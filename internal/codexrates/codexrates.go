// Package codexrates fetches OpenAI's published per-model token prices.
//
// Codex CLI sessions carry token counts but no cost, so cost is imputed from a
// rate table (codex_model_rates). That table used to be a hand-written seed in
// migrations.go, and it drifted: gpt-5.6-luna was seeded at five times its real
// price and gpt-6-astra was missing entirely, which billed it at $0. This
// package replaces the hand-edit with a daily read of the published table.
package codexrates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Rate is one model's published price in US dollars per 1M tokens.
type Rate struct {
	Input     float64
	Output    float64
	CacheRead float64
}

// Overridable for tests: the two source addresses and the HTTP client, in the
// shape internal/usage/poller.go already uses for the same reason.
var (
	markdownURL = "https://developers.openai.com/api/docs/pricing.md"
	htmlURL     = "https://developers.openai.com/api/docs/pricing"
	httpClient  = &http.Client{Timeout: 15 * time.Second}
)

// maxBody bounds a response so a redirect to something enormous cannot be read
// into memory. The real markdown page is ~22KB and the HTML page ~550KB.
const maxBody = 8 << 20

// Fetch returns the standard-tier rate table, keyed by lowercased model name,
// along with a label naming the source it came from.
//
// Three sources are tried in order and the first that parses wins. They are
// three views of the same table, most to least convenient: the markdown export,
// the hydration props embedded in the HTML page, and the rendered HTML table
// itself. Any of them can be retired without warning, which is why there are
// three; parsing the wrong one is caught by Accept, not here.
func Fetch(ctx context.Context) (map[string]Rate, string, error) {
	sources := []struct {
		label string
		url   *string
		parse func(string) (map[string]Rate, error)
	}{
		{"openai-md", &markdownURL, parseMarkdown},
		{"openai-astro", &htmlURL, parseAstro},
		{"openai-html", &htmlURL, parseHTMLTable},
	}
	bodies := map[string]string{}
	var errs []error
	for _, src := range sources {
		body, ok := bodies[*src.url]
		if !ok {
			b, err := get(ctx, *src.url)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", src.label, err))
				continue
			}
			bodies[*src.url] = b
			body = b
		}
		rates, err := src.parse(body)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", src.label, err))
			continue
		}
		return rates, src.label, nil
	}
	return nil, "", errors.Join(errs...)
}

func get(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", err
	}
	return string(b), nil
}
