package codexrates

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
)

// --- shared cell handling ---

var (
	mdLinkRe    = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	parenRe     = regexp.MustCompile(`\s*\([^)]*\)`)
	tagRe       = regexp.MustCompile(`<[^>]*>`)
	cellRe      = regexp.MustCompile(`(?is)<t([dh])[^>]*>(.*?)</t[dh]>`)
	rowRe       = regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)
	tableRe     = regexp.MustCompile(`(?is)<table[^>]*>.*?</table>`)
	islandRe    = regexp.MustCompile(`(?is)<astro-island[^>]*>`)
	propsAttrRe = regexp.MustCompile(`(?is)props="([^"]*)"`)
)

// normaliseModel turns a model cell into the model_prefix stored in Postgres:
// "[GPT-5.4](/models/gpt-5.4) (<272K context length)" becomes "gpt-5.4". The
// parenthetical suffixes are context-window and availability notes, not part of
// the model name, and prefix matching is done on lowercased names.
func normaliseModel(s string) string {
	s = mdLinkRe.ReplaceAllString(s, "$1")
	s = parenRe.ReplaceAllString(s, "")
	return strings.ToLower(strings.TrimSpace(s))
}

// parseCell reads a price cell. The second result is false when the cell holds
// no price -- "-", an em dash or blank, all of which the table uses for "this
// model has no such rate".
func parseCell(s string) (float64, bool) {
	s = strings.TrimSpace(strings.NewReplacer("$", "", ",", "", " ", "").Replace(s))
	switch s {
	case "", "-", "–", "—", "n/a", "N/A":
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// columns locates the four cells we need by header name rather than by index,
// so a column added or moved upstream does not silently shift every rate.
//
// Headers naming the long context window are skipped: the same models appear
// twice in one row, at double the price, and taking the first match would
// otherwise be a coin flip on column order. In the rendered HTML the two groups
// carry identical sub-headers ("Input", "Cached input", ...) with short context
// first, so first-match is the short-context one there too.
type columns struct{ model, input, cached, output int }

func findColumns(header []string) (columns, error) {
	c := columns{-1, -1, -1, -1}
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(h))
		if c.model < 0 && h == "model" {
			c.model = i
			continue
		}
		if strings.Contains(h, "long context") {
			continue
		}
		switch {
		case c.cached < 0 && strings.Contains(h, "cached input"):
			c.cached = i
		case c.input < 0 && strings.Contains(h, "input") && !strings.Contains(h, "cached") && !strings.Contains(h, "cache write"):
			c.input = i
		case c.output < 0 && strings.Contains(h, "output"):
			c.output = i
		}
	}
	if c.model < 0 || c.input < 0 || c.output < 0 {
		return c, fmt.Errorf("header %v has no model/input/output columns", header)
	}
	return c, nil
}

// collect turns header + data rows into the rate table. A row missing an input
// or output price is skipped: it would otherwise be stored as $0 and bill that
// model at nothing. A missing cached-input price is a real 0.
func collect(header []string, rows [][]string) (map[string]Rate, error) {
	c, err := findColumns(header)
	if err != nil {
		return nil, err
	}
	out := map[string]Rate{}
	for _, row := range rows {
		if len(row) <= c.model || len(row) <= c.input || len(row) <= c.output {
			continue
		}
		model := normaliseModel(row[c.model])
		if model == "" {
			continue
		}
		in, okIn := parseCell(row[c.input])
		outRate, okOut := parseCell(row[c.output])
		if !okIn || !okOut {
			continue
		}
		var cached float64
		if c.cached >= 0 && len(row) > c.cached {
			cached, _ = parseCell(row[c.cached])
		}
		out[model] = Rate{Input: in, Output: outRate, CacheRead: cached}
	}
	if len(out) == 0 {
		return nil, errors.New("no priced rows")
	}
	return out, nil
}

// --- source 1: the markdown export ---

// parseMarkdown reads the "Standard pricing data" table out of pricing.md.
//
// The section heading is load-bearing. The same page repeats every model under
// Batch, Flex and Fast at half or double the standard price, and further down
// under "Grouped Pricing Table data" for the Codex-branded models, so a parser
// that just took the first markdown table would price Codex sessions at the
// batch tier.
func parseMarkdown(body string) (map[string]Rate, error) {
	lines := strings.Split(body, "\n")
	start := -1
	for i, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l), "### Standard pricing data") {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, errors.New("no \"### Standard pricing data\" heading")
	}
	var table [][]string
	for _, l := range lines[start:] {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "#") {
			break
		}
		if !strings.HasPrefix(l, "|") {
			continue
		}
		table = append(table, splitMarkdownRow(l))
	}
	if len(table) < 3 {
		return nil, errors.New("standard section has no table")
	}
	// table[1] is the |---|---| separator.
	return collect(table[0], table[2:])
}

func splitMarkdownRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	cells := strings.Split(line, "|")
	for i := range cells {
		cells[i] = strings.TrimSpace(cells[i])
	}
	return cells
}

// --- source 2: the hydration props in the HTML page ---

// parseAstro reads the rate table out of the props the pricing page hands its
// TextTokenPricingTables component. Every value is wrapped as [typeTag, value]
// by Astro's serialiser, and there is one island per tier -- only "standard"
// is ours.
//
// The rows carry no headers, so the columns are positional: either
// [model, input, cached, cacheWrites, output] or, for models with no cache-write
// price, [model, input, cached, output]. Output is always last. Reading them
// the wrong way round is what the Accept anchors catch.
func parseAstro(body string) (map[string]Rate, error) {
	for _, tag := range islandRe.FindAllString(body, -1) {
		if !strings.Contains(tag, `component-export="TextTokenPricingTables"`) {
			continue
		}
		m := propsAttrRe.FindStringSubmatch(tag)
		if m == nil {
			continue
		}
		rates, err := parseAstroProps(html.UnescapeString(m[1]))
		if err != nil || rates == nil {
			continue
		}
		return rates, nil
	}
	return nil, errors.New("no standard-tier TextTokenPricingTables island")
}

// parseAstroProps returns nil (and no error) for a well-formed island of some
// other tier, so the caller keeps looking.
func parseAstroProps(props string) (map[string]Rate, error) {
	var raw struct {
		Tier json.RawMessage `json:"tier"`
		Rows json.RawMessage `json:"rows"`
	}
	// Decode rather than Unmarshal: the attribute has been observed with a
	// second JSON document appended, and only the first one is ours.
	if err := json.NewDecoder(strings.NewReader(props)).Decode(&raw); err != nil {
		return nil, err
	}
	var tier string
	if err := json.Unmarshal(unwrap(raw.Tier), &tier); err != nil {
		return nil, err
	}
	if tier != "standard" {
		return nil, nil
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(unwrap(raw.Rows), &rows); err != nil {
		return nil, err
	}
	out := map[string]Rate{}
	for _, row := range rows {
		var cells []json.RawMessage
		if err := json.Unmarshal(unwrap(row), &cells); err != nil {
			continue
		}
		if len(cells) < 4 {
			continue
		}
		model := normaliseModel(astroString(cells[0]))
		in, okIn := parseCell(astroString(cells[1]))
		outRate, okOut := parseCell(astroString(cells[len(cells)-1]))
		if model == "" || !okIn || !okOut {
			continue
		}
		cached, _ := parseCell(astroString(cells[2]))
		out[model] = Rate{Input: in, Output: outRate, CacheRead: cached}
	}
	if len(out) == 0 {
		return nil, errors.New("standard island has no priced rows")
	}
	return out, nil
}

// unwrap strips Astro's [typeTag, value] envelope, returning the raw value
// unchanged when there is no envelope.
func unwrap(raw json.RawMessage) json.RawMessage {
	var pair []json.RawMessage
	if err := json.Unmarshal(raw, &pair); err != nil || len(pair) != 2 {
		return raw
	}
	return pair[1]
}

// astroString renders a cell value as text so parseCell can handle numbers,
// "-" and null through one path.
func astroString(raw json.RawMessage) string {
	var v any
	if err := json.Unmarshal(unwrap(raw), &v); err != nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// --- source 3: the rendered HTML table ---

// parseHTMLTable scrapes the table the page renders server-side. It is the last
// resort for a reason: the rendered table is collapsed to the newest few models
// (the rest are revealed by client-side JavaScript we do not run), so it will
// usually be rejected by Accept for having too few rows. It stays in the chain
// because the collapse is a rendering choice upstream could reverse.
func parseHTMLTable(body string) (map[string]Rate, error) {
	var errs []error
	for _, table := range tableRe.FindAllString(body, -1) {
		var header []string
		var rows [][]string
		for _, tr := range rowRe.FindAllStringSubmatch(table, -1) {
			cells := htmlCells(tr[1])
			if len(cells) == 0 {
				continue
			}
			if header == nil {
				if _, err := findColumns(cells); err == nil {
					header = cells
				}
				continue
			}
			rows = append(rows, cells)
		}
		if header == nil {
			continue
		}
		rates, err := collect(header, rows)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		return rates, nil
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return nil, errors.New("no pricing table found in HTML")
}

func htmlCells(row string) []string {
	var cells []string
	for _, m := range cellRe.FindAllStringSubmatch(row, -1) {
		cells = append(cells, strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(m[2], ""))))
	}
	return cells
}
