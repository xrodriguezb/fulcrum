package httpx_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/httpx"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

func decodeProblem(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	if got := recorder.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Errorf("content type = %q, want application/problem+json", got)
	}
	var problem map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
		t.Fatalf("response is not json: %v (%s)", err, recorder.Body.String())
	}
	return problem
}

func TestProblemCarriesTheStableCodeAndStatus(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "validation",
			err:        errs.Validation(errs.CodeValidationFailed, "The request is not valid.", nil),
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeValidationFailed,
		},
		{
			name:       "inventory conflict",
			err:        errs.Conflict(errs.CodeInventoryInsufficient, "The requested quantity is no longer available.", nil),
			wantStatus: http.StatusConflict,
			wantCode:   errs.CodeInventoryInsufficient,
		},
		{
			name:       "key reuse",
			err:        errs.Precondition(errs.CodeIdempotencyKeyReuse, "This key was used with a different body.", nil),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   errs.CodeIdempotencyKeyReuse,
		},
		{
			name:       "not found",
			err:        errs.NotFound(errs.CodeOrderNotFound, "The order does not exist.", nil),
			wantStatus: http.StatusNotFound,
			wantCode:   errs.CodeOrderNotFound,
		},
		{
			name:       "unclassified",
			err:        errors.New("something nobody classified"),
			wantStatus: http.StatusInternalServerError,
			wantCode:   errs.CodeInternal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orders", nil)

			httpx.WriteProblem(recorder, request, tc.err, logging.NewJSON(discard{}, 0))

			if recorder.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			problem := decodeProblem(t, recorder)
			if problem["code"] != tc.wantCode {
				t.Errorf("code = %v, want %v", problem["code"], tc.wantCode)
			}
			if problem["status"] != float64(tc.wantStatus) {
				t.Errorf("status field = %v, want %d", problem["status"], tc.wantStatus)
			}
			if problem["instance"] != "/api/v1/orders" {
				t.Errorf("instance = %v, want the request path", problem["instance"])
			}
			typeURI, _ := problem["type"].(string)
			if !strings.HasPrefix(typeURI, "https://fulcrum.dev/problems/") {
				t.Errorf("type = %v, want a problem type uri", problem["type"])
			}
			if title, _ := problem["title"].(string); title == "" {
				t.Errorf("a problem needs a title")
			}
		})
	}
}

// The assertion that catches real leaks: nothing from the infrastructure error
// may appear in the response, at any depth of wrapping.
func TestProblemNeverLeaksInfrastructureDetail(t *testing.T) {
	t.Parallel()

	cause := errors.New(`pq: relation "orders" does not exist, host=db.internal user=fulcrum_app /var/lib/postgresql`)
	wrapped := errs.Internal("persist the order", cause)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orders", nil)
	httpx.WriteProblem(recorder, request, wrapped, logging.NewJSON(discard{}, 0))

	body := recorder.Body.String()
	for _, leak := range []string{"pq:", "relation", "db.internal", "fulcrum_app", "/var/lib"} {
		if strings.Contains(body, leak) {
			t.Errorf("the response leaked %q: %s", leak, body)
		}
	}
	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", recorder.Code)
	}
}

// An operator has to be able to join a problem response to a log line, so the
// trace id travels even when nothing else does.
func TestProblemCarriesTheTraceID(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders/1", nil)
	ctx := logging.WithTraceID(request.Context(), "4bf92f3577b34da6a3ce929d0e0e4736")
	request = request.WithContext(ctx)

	httpx.WriteProblem(recorder, request, errs.Internal("boom", errors.New("cause")), logging.NewJSON(discard{}, 0))

	problem := decodeProblem(t, recorder)
	if problem["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("trace_id = %v, want the propagated trace id", problem["trace_id"])
	}
}

func TestRetryAfterIsSetForAnInFlightDuplicate(t *testing.T) {
	t.Parallel()

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orders", nil)
	err := errs.Conflict(errs.CodeIdempotencyInProgress, "A request with this key is still in progress.", nil)

	httpx.WriteProblem(recorder, request, err, logging.NewJSON(discard{}, 0))

	if got := recorder.Header().Get("Retry-After"); got == "" {
		t.Errorf("an in progress duplicate must carry Retry-After")
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// Routing is the one place where an API usually stops speaking its own error
// contract: the router answers with plain text and every client has to special
// case it.
func TestRoutingRejectionsAreProblemDocuments(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/orders", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("GET /api/v1/gone", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteProblem(w, r, errs.NotFound(errs.CodeOrderNotFound, "The order does not exist.", nil),
			logging.NewJSON(discard{}, 0))
	})

	handler := httpx.RoutingProblems(logging.NewJSON(discard{}, 0))(mux)

	cases := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantCode   string
	}{
		{name: "unknown path", method: http.MethodGet, path: "/api/v1/nope", wantStatus: http.StatusNotFound, wantCode: errs.CodeValidationFailed},
		{name: "wrong method", method: http.MethodDelete, path: "/api/v1/orders", wantStatus: http.StatusMethodNotAllowed, wantCode: errs.CodeMethodNotAllowed},
		{name: "handler not found is left alone", method: http.MethodGet, path: "/api/v1/gone", wantStatus: http.StatusNotFound, wantCode: errs.CodeOrderNotFound},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder := httptest.NewRecorder()
			request := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)
			handler.ServeHTTP(recorder, request)

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			problem := decodeProblem(t, recorder)
			if problem["code"] != tc.wantCode {
				t.Errorf("code = %v, want %v", problem["code"], tc.wantCode)
			}
		})
	}

	// A matched route still answers normally.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders", nil))
	if recorder.Code != http.StatusOK {
		t.Errorf("a matched route returned %d, want 200", recorder.Code)
	}
}
