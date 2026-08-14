package examples

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Provider names this adapter reports results and errors under. They are plain
// strings rather than a Servicesim type on purpose: the adapter is production
// code, and production code does not import its test simulator.
const (
	ProviderExa        = "exa"
	ProviderTavily     = "tavily"
	ProviderPerplexity = "perplexity"
)

// maxErrorBodyBytes bounds how much of a failed response is quoted in an
// [APIError]. An error message is a log line, not a payload dump.
const maxErrorBodyBytes = 1 << 12

// defaultNumResults is how many results each provider is asked for. It is spelled
// once so the request-body assertions in adapter_test.go have one number to
// name.
const defaultNumResults = 5

// Config configures an [Adapter]. The base URLs are separate fields because the
// three vendors are three hosts in production and three listeners under
// Servicesim, and a test sets them from sim.BaseURLs() exactly as Compose would
// set the environment.
type Config struct {
	ExaBaseURL        string
	TavilyBaseURL     string
	PerplexityBaseURL string

	ExaKey        string
	TavilyKey     string
	PerplexityKey string

	// HTTPClient is used as given. A test should pass sim.Client(), whose
	// keep-alives are disabled so a connection-abort fault is observed rather
	// than absorbed by a pooled connection. nil selects http.DefaultClient.
	HTTPClient *http.Client
}

// Adapter is a small research adapter over the three simulated vendors: it sends
// each one its own search request and normalises what comes back into a single
// [Result] type.
//
// It is deliberately not a product. It exists to be the thing under test in the
// files beside it — documentation that happens to compile — and it stops at the
// point where a real adapter would grow retries, backoff, caching, ranking and
// telemetry. Copy its shape, not its feature set.
type Adapter struct {
	config Config
	client *http.Client
}

// New returns an Adapter configured by cfg.
func New(cfg Config) *Adapter {
	client := cfg.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	return &Adapter{config: cfg, client: client}
}

// Result is one normalised search result: the fields every vendor agrees on,
// under one spelling.
type Result struct {
	Title   string
	Snippet string

	// URL is the canonical URL after [Search] has fused the providers' answers —
	// the same document reached through a tracking link collapses onto one entry.
	// A single-provider call returns the URL its vendor sent, unchanged.
	URL string

	// Providers names every provider that returned this document, in the fixed
	// order the fan-out queries them. A single-provider call leaves exactly one
	// name here, which is what makes corroboration countable after a fusion.
	Providers []string
}

// APIError is a non-2xx response from a provider. It is a distinct type so a
// caller can classify the failure instead of matching on message text, which is
// the difference between "back off and retry" and "stop, the credential is
// wrong".
type APIError struct {
	Provider   string
	StatusCode int

	// RetryAfter is the parsed Retry-After header, zero when the response carried
	// none. Only the delta-seconds form is handled here; the HTTP-date form is
	// legal and rarer, and a real adapter should parse both.
	RetryAfter time.Duration

	// Body is the response body, truncated. It never carries a credential:
	// Servicesim's error envelopes quote the request's fields, never its
	// Authorization header.
	Body string
}

// Error implements error.
func (e *APIError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.StatusCode, e.Body)
}

// Retryable reports whether the same request is worth sending again: a 429 or a
// server error is, and every other 4xx is the caller's own fault and will fail
// identically forever.
func (e *APIError) Retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= http.StatusInternalServerError
}

// SearchExa sends POST /search to Exa, which authenticates with an x-api-key
// header.
func (a *Adapter) SearchExa(ctx context.Context, query string) ([]Result, error) {
	body := map[string]any{
		"query":      query,
		"numResults": defaultNumResults,
	}

	var decoded exaSearchResponse
	err := a.post(ctx, call{
		provider: ProviderExa,
		baseURL:  a.config.ExaBaseURL,
		path:     "/search",
		authorize: func(r *http.Request) {
			r.Header.Set("x-api-key", a.config.ExaKey)
		},
	}, body, &decoded)
	if err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(decoded.Results))
	for _, r := range decoded.Results {
		out = append(out, Result{
			Title:     r.Title,
			URL:       r.URL,
			Snippet:   firstNonEmpty(first(r.Highlights), r.Summary, r.Text),
			Providers: []string{ProviderExa},
		})
	}
	return out, nil
}

// SearchTavily sends POST /search to Tavily, which authenticates with
// Authorization: Bearer. An api_key property in the body is not Tavily's REST
// contract and Servicesim raises a finding for it, so the key goes in the header
// and nowhere else.
func (a *Adapter) SearchTavily(ctx context.Context, query string) ([]Result, error) {
	body := map[string]any{
		"query":       query,
		"max_results": defaultNumResults,
	}

	var decoded tavilySearchResponse
	err := a.post(ctx, call{
		provider:  ProviderTavily,
		baseURL:   a.config.TavilyBaseURL,
		path:      "/search",
		authorize: bearer(a.config.TavilyKey),
	}, body, &decoded)
	if err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(decoded.Results))
	for _, r := range decoded.Results {
		out = append(out, Result{
			Title:     r.Title,
			URL:       r.URL,
			Snippet:   r.Content,
			Providers: []string{ProviderTavily},
		})
	}
	return out, nil
}

// SearchPerplexity sends POST /v1/sonar and reads the grounding out of
// search_results — not out of the deprecated bare-URL citations array, which
// carries no title and no snippet.
func (a *Adapter) SearchPerplexity(ctx context.Context, query string) ([]Result, error) {
	body := map[string]any{
		"model": "sonar",
		"messages": []map[string]any{
			{"role": "user", "content": query},
		},
	}

	var decoded perplexitySonarResponse
	err := a.post(ctx, call{
		provider:  ProviderPerplexity,
		baseURL:   a.config.PerplexityBaseURL,
		path:      "/v1/sonar",
		authorize: bearer(a.config.PerplexityKey),
	}, body, &decoded)
	if err != nil {
		return nil, err
	}

	out := make([]Result, 0, len(decoded.SearchResults))
	for _, r := range decoded.SearchResults {
		out = append(out, Result{
			Title:     r.Title,
			URL:       r.URL,
			Snippet:   r.Snippet,
			Providers: []string{ProviderPerplexity},
		})
	}
	return out, nil
}

// Search queries all three providers concurrently and fuses their answers into
// one deduplicated list.
//
// It returns whatever succeeded together with a joined error for whatever did
// not: one provider being rate limited is not a reason to discard the other two,
// and a caller that wants all-or-nothing checks the error before reading the
// results.
func (a *Adapter) Search(ctx context.Context, query string) ([]Result, error) {
	searches := []func(context.Context, string) ([]Result, error){
		a.SearchExa,
		a.SearchTavily,
		a.SearchPerplexity,
	}

	// Indexed slices rather than a channel: each goroutine writes its own element,
	// so the fused order is the fixed provider order above rather than whichever
	// call happened to finish first. A simulator is deterministic; a consumer's
	// fusion should be too.
	found := make([][]Result, len(searches))
	failed := make([]error, len(searches))

	var wg sync.WaitGroup
	for i, search := range searches {
		wg.Add(1)
		go func() {
			defer wg.Done()
			found[i], failed[i] = search(ctx, query)
		}()
	}
	wg.Wait()

	return fuse(found), errors.Join(failed...)
}

// fuse deduplicates results by canonical URL, preserving first-seen order and
// recording every provider that returned each document.
func fuse(perProvider [][]Result) []Result {
	var out []Result
	at := map[string]int{}

	for _, batch := range perProvider {
		for _, r := range batch {
			key := CanonicalURL(r.URL)
			if i, seen := at[key]; seen {
				out[i].Providers = appendMissing(out[i].Providers, r.Providers...)
				if out[i].Snippet == "" {
					out[i].Snippet = r.Snippet
				}
				continue
			}
			r.URL = key
			at[key] = len(out)
			out = append(out, r)
		}
	}
	return out
}

// trackingParams are the query parameters CanonicalURL drops. The list is short
// and illustrative rather than exhaustive.
var trackingParams = []string{
	"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content",
	"gclid", "fbclid", "ref",
}

// CanonicalURL reduces a URL to the document it addresses: no fragment, no
// tracking parameters, a lower-cased host and no trailing slash.
//
// It is exported because it is the whole point of the fusion-overlap scenario. A
// consumer that deduplicates on the raw URL string reports the same report twice
// when one provider returned it through a newsletter link, and the resulting
// double count looks exactly like corroboration.
func CanonicalURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw // an unparseable URL is still a usable dedup key
	}

	u.Fragment = ""
	q := u.Query()
	for _, name := range trackingParams {
		q.Del(name)
	}
	// Encode sorts by key, so two orderings of the same parameters canonicalise
	// to one string.
	u.RawQuery = q.Encode()
	u.Host = strings.ToLower(u.Host)
	if u.Path != "/" {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}
	return u.String()
}

// call is one provider's endpoint and how to authenticate against it.
type call struct {
	provider  string
	baseURL   string
	path      string
	authorize func(*http.Request)
}

// post sends one JSON request and decodes a JSON response into out.
func (a *Adapter) post(ctx context.Context, c call, body, out any) error {
	if c.baseURL == "" {
		return fmt.Errorf("%s: no base URL is configured", c.provider)
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("%s: encoding the request: %w", c.provider, err)
	}

	// The base URL may already carry a Servicesim namespace prefix
	// (http://host:8081/n/<name>), so the route is appended to it rather than
	// replacing its path.
	endpoint := strings.TrimSuffix(c.baseURL, "/") + c.path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("%s: building the request: %w", c.provider, err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req)

	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", c.provider, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(c.provider, resp)
	}

	// Decoding without DisallowUnknownFields is deliberate. Real APIs evolve
	// additively, and a decoder that rejects a field the vendor added last week
	// turns a compatible change into an outage; Servicesim's extra-fields
	// scenario exists to prove a consumer tolerates them.
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s: decoding the response: %w", c.provider, err)
	}
	return nil
}

// apiError builds the typed error for a non-2xx response.
func apiError(provider string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))

	e := &APIError{
		Provider:   provider,
		StatusCode: resp.StatusCode,
		Body:       strings.TrimSpace(string(body)),
	}
	if seconds, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && seconds >= 0 {
		e.RetryAfter = time.Duration(seconds) * time.Second
	}
	return e
}

// bearer returns an authorizer that presents key as an Authorization: Bearer
// credential.
func bearer(key string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+key)
	}
}

// appendMissing appends the names of add that base does not already hold.
func appendMissing(base []string, add ...string) []string {
	for _, name := range add {
		if !slices.Contains(base, name) {
			base = append(base, name)
		}
	}
	return base
}

// first returns the first element of s, or the empty string.
func first(s []string) string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// firstNonEmpty returns the first non-empty argument. Each vendor puts its
// excerpt somewhere different, and a normalising adapter has to pick.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// The response types below carry only the fields this adapter reads. That is the
// realistic shape: a consumer declares what it consumes, and everything else on
// the wire is ignored rather than rejected.

type exaSearchResponse struct {
	Results []struct {
		Title      string   `json:"title"`
		URL        string   `json:"url"`
		Text       string   `json:"text"`
		Summary    string   `json:"summary"`
		Highlights []string `json:"highlights"`
	} `json:"results"`
}

type tavilySearchResponse struct {
	Results []struct {
		Title   string  `json:"title"`
		URL     string  `json:"url"`
		Content string  `json:"content"`
		Score   float64 `json:"score"`
	} `json:"results"`
}

type perplexitySonarResponse struct {
	SearchResults []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet"`
	} `json:"search_results"`
}
