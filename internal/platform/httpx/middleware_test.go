package httpx_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/httpx"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

func testChain(t *testing.T, handler http.Handler, options ...func(*httpx.MiddlewareConfig)) http.Handler {
	t.Helper()

	cfg := httpx.MiddlewareConfig{
		Logger:         logging.NewJSON(discard{}, 0),
		Metrics:        httpx.NewMetrics(prometheus.NewRegistry()),
		MaxBodyBytes:   1024,
		AllowedOrigins: []string{"http://localhost:5173"},
	}
	for _, option := range options {
		option(&cfg)
	}
	chain, err := httpx.Chain(cfg)
	if err != nil {
		t.Fatalf("build middleware chain: %v", err)
	}
	return chain(handler)
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})
}

// A panic must become a problem document, not a dropped connection, and the
// trace id must survive so the log line and the response can be joined.
func TestRecoveryTurnsAPanicIntoAProblem(t *testing.T) {
	t.Parallel()

	handler := testChain(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("deliberate panic in a handler")
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders", nil)
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
	var problem map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("response is not json: %v", err)
	}
	if problem["code"] != errs.CodeInternal {
		t.Errorf("code = %v, want %v", problem["code"], errs.CodeInternal)
	}
	if strings.Contains(recorder.Body.String(), "deliberate panic") {
		t.Errorf("the panic message leaked into the response: %s", recorder.Body.String())
	}
}

func TestRequestIDIsGeneratedAndEchoed(t *testing.T) {
	t.Parallel()

	var seen struct {
		requestID     string
		correlationID string
	}
	handler := testChain(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.requestID = logging.RequestID(r.Context())
		seen.correlationID = logging.CorrelationID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders", nil)
	handler.ServeHTTP(recorder, request)

	if seen.requestID == "" {
		t.Errorf("a request without an id must be given one")
	}
	if seen.correlationID == "" {
		t.Errorf("a request without a correlation id must be given one")
	}
	if recorder.Header().Get("X-Request-Id") != seen.requestID {
		t.Errorf("the response must echo the request id")
	}
}

func TestSuppliedIdentifiersAreHonoured(t *testing.T) {
	t.Parallel()

	var correlation string
	handler := testChain(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlation = logging.CorrelationID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders", nil)
	request.Header.Set("X-Request-Id", "req-supplied")
	request.Header.Set("X-Correlation-Id", "corr-supplied")
	handler.ServeHTTP(recorder, request)

	if correlation != "corr-supplied" {
		t.Errorf("correlation id = %q, want the supplied one", correlation)
	}
	if got := recorder.Header().Get("X-Request-Id"); got != "req-supplied" {
		t.Errorf("request id = %q, want the supplied one", got)
	}
}

// A client supplied identifier ends up in logs, so it cannot be arbitrary.
func TestSuppliedIdentifiersAreRejectedWhenUnsafe(t *testing.T) {
	t.Parallel()

	var requestID string
	handler := testChain(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID = logging.RequestID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders", nil)
	request.Header.Set("X-Request-Id", strings.Repeat("x", 500))
	handler.ServeHTTP(recorder, request)

	if requestID == strings.Repeat("x", 500) {
		t.Errorf("an oversized client identifier was accepted verbatim")
	}
	if requestID == "" {
		t.Errorf("rejecting the supplied id must still produce one")
	}
}

func TestBodyLimitRejectsAnOversizeRequest(t *testing.T) {
	t.Parallel()

	handler := testChain(t, okHandler(), func(cfg *httpx.MiddlewareConfig) { cfg.MaxBodyBytes = 16 })

	recorder := httptest.NewRecorder()
	body := strings.NewReader(strings.Repeat("a", 512))
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orders", body)
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", recorder.Code)
	}
	var problem map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &problem)
	if problem["code"] != errs.CodeRequestTooLarge {
		t.Errorf("code = %v, want %v", problem["code"], errs.CodeRequestTooLarge)
	}
}

func TestContentTypeIsEnforcedOnWrites(t *testing.T) {
	t.Parallel()

	handler := testChain(t, okHandler())

	cases := []struct {
		name        string
		method      string
		contentType string
		wantStatus  int
	}{
		{name: "json is accepted", method: http.MethodPost, contentType: "application/json", wantStatus: http.StatusOK},
		{name: "json with charset is accepted", method: http.MethodPost, contentType: "application/json; charset=utf-8", wantStatus: http.StatusOK},
		{name: "form is rejected", method: http.MethodPost, contentType: "application/x-www-form-urlencoded", wantStatus: http.StatusUnsupportedMediaType},
		{name: "missing is rejected", method: http.MethodPost, contentType: "", wantStatus: http.StatusUnsupportedMediaType},
		{name: "reads are unaffected", method: http.MethodGet, contentType: "", wantStatus: http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequestWithContext(t.Context(), tc.method, "/api/v1/orders", strings.NewReader("{}"))
			if tc.contentType != "" {
				request.Header.Set("Content-Type", tc.contentType)
			}
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
		})
	}
}

func TestCORSAllowsOnlyConfiguredOrigins(t *testing.T) {
	t.Parallel()

	handler := testChain(t, okHandler())

	allowed := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	handler.ServeHTTP(allowed, request)
	if got := allowed.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
		t.Errorf("allowed origin header = %q", got)
	}

	denied := httptest.NewRecorder()
	request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders", nil)
	request.Header.Set("Origin", "https://evil.example.com")
	handler.ServeHTTP(denied, request)
	if got := denied.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("an unlisted origin was allowed: %q", got)
	}

	preflight := httptest.NewRecorder()
	request = httptest.NewRequestWithContext(t.Context(), http.MethodOptions, "/api/v1/orders", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	request.Header.Set("Access-Control-Request-Method", "POST")
	handler.ServeHTTP(preflight, request)
	if preflight.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", preflight.Code)
	}
	if !strings.Contains(preflight.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key") {
		t.Errorf("preflight must allow the Idempotency-Key header, got %q",
			preflight.Header().Get("Access-Control-Allow-Headers"))
	}
}

func TestSecurityHeadersArePresent(t *testing.T) {
	t.Parallel()

	handler := testChain(t, okHandler())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders", nil)
	handler.ServeHTTP(recorder, request)

	want := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	}
	for header, value := range want {
		if got := recorder.Header().Get(header); got != value {
			t.Errorf("%s = %q, want %q", header, got, value)
		}
	}
}

func TestMetricsRecordEveryRequest(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()
	metrics := httpx.NewMetrics(registry)
	handler := testChain(t, okHandler(), func(cfg *httpx.MiddlewareConfig) { cfg.Metrics = metrics })

	for range 3 {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders", nil)
		handler.ServeHTTP(recorder, request)
	}

	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}

	var total float64
	for _, family := range families {
		if family.GetName() != "http_requests_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			total += metric.GetCounter().GetValue()
			for _, label := range metric.GetLabel() {
				// Labels must stay bounded. A path with an identifier in it
				// would make the cardinality grow with traffic.
				if label.GetName() == "route" && strings.Contains(label.GetValue(), "0b7c6f4e") {
					t.Errorf("route label carries an identifier: %q", label.GetValue())
				}
			}
		}
	}
	if total != 3 {
		t.Errorf("http_requests_total = %v, want 3", total)
	}
}

func TestChainRejectsIncompleteConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := httpx.Chain(httpx.MiddlewareConfig{}); err == nil {
		t.Errorf("a chain without a logger or metrics must not build")
	}
}
