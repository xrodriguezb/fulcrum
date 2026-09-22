package infra_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xrodriguezb/fulcrum/internal/order/app"
	"github.com/xrodriguezb/fulcrum/internal/order/domain"
	"github.com/xrodriguezb/fulcrum/internal/order/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
)

const (
	validCustomer = "11111111-2222-4333-8444-555555555555"
	validOrderID  = "0b7c6f4e-9d3a-4f1b-8c2d-5a6e7f8b9c0d"
)

type stubCreator struct {
	result  app.CreateOrderResult
	err     error
	lastCmd app.CreateOrderCommand
}

func (s *stubCreator) Handle(_ context.Context, cmd app.CreateOrderCommand) (app.CreateOrderResult, error) {
	s.lastCmd = cmd
	return s.result, s.err
}

type stubReader struct {
	order *domain.Order
	page  []*domain.Order
	total int
	err   error
}

func (s *stubReader) ByID(context.Context, domain.OrderID) (*domain.Order, error) {
	return s.order, s.err
}

func (s *stubReader) Page(context.Context, int, int) ([]*domain.Order, int, error) {
	return s.page, s.total, s.err
}

func newHandlers(t *testing.T, creator *stubCreator, reader *stubReader) *infra.Handlers {
	t.Helper()
	return infra.NewHandlers(creator, reader, logging.NewJSON(discardWriter{}, 0))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func createdResult() app.CreateOrderResult {
	view := app.OrderView{
		ID:         validOrderID,
		CustomerID: validCustomer,
		Status:     "pending",
		TotalCents: 2100,
		Currency:   "EUR",
		Lines:      []app.OrderLineView{{SKU: "WIDGET-001", Quantity: 2, UnitPriceCents: 1050}},
		CreatedAt:  time.Date(2026, time.September, 21, 10, 0, 0, 0, time.UTC),
	}
	body, _ := json.Marshal(view)
	return app.CreateOrderResult{Status: http.StatusCreated, Body: body, View: view}
}

func postOrder(t *testing.T, handlers *infra.Handlers, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/orders", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	recorder := httptest.NewRecorder()
	handlers.Create(recorder, request)
	return recorder
}

func TestCreateReturnsTheStoredBytes(t *testing.T) {
	t.Parallel()

	creator := &stubCreator{result: createdResult()}
	handlers := newHandlers(t, creator, &stubReader{})

	recorder := postOrder(t, handlers,
		`{"customer_id":"`+validCustomer+`","lines":[{"sku":"WIDGET-001","quantity":2}]}`,
		map[string]string{"Idempotency-Key": "key-1"})

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != string(creator.result.Body) {
		t.Errorf("body = %s, want the bytes the use case produced", recorder.Body.String())
	}
	if creator.lastCmd.IdempotencyKey != "key-1" {
		t.Errorf("the handler did not pass the idempotency key through")
	}
	if recorder.Header().Get("Idempotent-Replay") != "" {
		t.Errorf("a fresh creation must not be marked as a replay")
	}
}

func TestCreateMarksAReplay(t *testing.T) {
	t.Parallel()

	result := createdResult()
	result.Replayed = true
	handlers := newHandlers(t, &stubCreator{result: result}, &stubReader{})

	recorder := postOrder(t, handlers,
		`{"customer_id":"`+validCustomer+`","lines":[{"sku":"WIDGET-001","quantity":2}]}`,
		map[string]string{"Idempotency-Key": "key-1"})

	if recorder.Header().Get("Idempotent-Replay") != "true" {
		t.Errorf("a replay must be marked with Idempotent-Replay")
	}
	if recorder.Body.String() != string(result.Body) {
		t.Errorf("a replay must return the stored bytes unchanged")
	}
}

func TestCreateRejectsMalformedRequests(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		body       string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "not json",
			body:       `not json at all`,
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeValidationFailed,
		},
		{
			name:       "unknown field",
			body:       `{"customer_id":"` + validCustomer + `","lines":[{"sku":"WIDGET-001","quantity":1}],"discount":10}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeValidationFailed,
		},
		{
			name:       "trailing document",
			body:       `{"customer_id":"` + validCustomer + `","lines":[{"sku":"WIDGET-001","quantity":1}]}{"another":true}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeValidationFailed,
		},
		{
			name:       "no lines",
			body:       `{"customer_id":"` + validCustomer + `","lines":[]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeValidationFailed,
		},
		{
			name:       "invalid sku",
			body:       `{"customer_id":"` + validCustomer + `","lines":[{"sku":"a","quantity":1}]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeValidationFailed,
		},
		{
			name:       "quantity out of range",
			body:       `{"customer_id":"` + validCustomer + `","lines":[{"sku":"WIDGET-001","quantity":0}]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeValidationFailed,
		},
		{
			name:       "duplicate sku",
			body:       `{"customer_id":"` + validCustomer + `","lines":[{"sku":"WIDGET-001","quantity":1},{"sku":"widget-001","quantity":1}]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeValidationFailed,
		},
		{
			name:       "customer id is not a uuid",
			body:       `{"customer_id":"customer-42","lines":[{"sku":"WIDGET-001","quantity":1}]}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeValidationFailed,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handlers := newHandlers(t, &stubCreator{result: createdResult()}, &stubReader{})
			recorder := postOrder(t, handlers, tc.body, map[string]string{"Idempotency-Key": "key-1"})

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", recorder.Code, tc.wantStatus, recorder.Body.String())
			}
			var problem map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil {
				t.Fatalf("response is not a problem document: %v", err)
			}
			if problem["code"] != tc.wantCode {
				t.Errorf("code = %v, want %v", problem["code"], tc.wantCode)
			}
		})
	}
}

// The use case owns the idempotency rules; the handler must surface them
// unchanged rather than reinterpreting them.
func TestCreateSurfacesUseCaseErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{
			name:       "missing key",
			err:        errs.Validation(errs.CodeIdempotencyKeyRequired, "The Idempotency-Key header is required.", nil),
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeIdempotencyKeyRequired,
		},
		{
			name:       "key reuse",
			err:        errs.Precondition(errs.CodeIdempotencyKeyReuse, "Reused with a different body.", nil),
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   errs.CodeIdempotencyKeyReuse,
		},
		{
			name:       "in progress",
			err:        errs.Conflict(errs.CodeIdempotencyInProgress, "Still in progress.", nil),
			wantStatus: http.StatusConflict,
			wantCode:   errs.CodeIdempotencyInProgress,
		},
		{
			name:       "insufficient inventory",
			err:        errs.Conflict(errs.CodeInventoryInsufficient, "No longer available.", nil),
			wantStatus: http.StatusConflict,
			wantCode:   errs.CodeInventoryInsufficient,
		},
		{
			name:       "internal failure",
			err:        errs.Internal("persist", errors.New("pq: connection refused host=db.internal")),
			wantStatus: http.StatusInternalServerError,
			wantCode:   errs.CodeInternal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			handlers := newHandlers(t, &stubCreator{err: tc.err}, &stubReader{})
			recorder := postOrder(t, handlers,
				`{"customer_id":"`+validCustomer+`","lines":[{"sku":"WIDGET-001","quantity":1}]}`,
				map[string]string{"Idempotency-Key": "key-1"})

			if recorder.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantStatus)
			}
			var problem map[string]any
			_ = json.Unmarshal(recorder.Body.Bytes(), &problem)
			if problem["code"] != tc.wantCode {
				t.Errorf("code = %v, want %v", problem["code"], tc.wantCode)
			}
			if strings.Contains(recorder.Body.String(), "db.internal") {
				t.Errorf("the response leaked infrastructure detail: %s", recorder.Body.String())
			}
		})
	}
}

func TestGetRejectsAnInvalidIdentifier(t *testing.T) {
	t.Parallel()

	handlers := newHandlers(t, &stubCreator{}, &stubReader{})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders/not-a-uuid", nil)
	request.SetPathValue("id", "not-a-uuid")
	recorder := httptest.NewRecorder()

	handlers.Get(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", recorder.Code)
	}
}

func TestGetSurfacesNotFound(t *testing.T) {
	t.Parallel()

	handlers := newHandlers(t, &stubCreator{}, &stubReader{
		err: errs.NotFound(errs.CodeOrderNotFound, "The order does not exist.", nil),
	})
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders/"+validOrderID, nil)
	request.SetPathValue("id", validOrderID)
	recorder := httptest.NewRecorder()

	handlers.Get(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", recorder.Code)
	}
	var problem map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &problem)
	if problem["code"] != errs.CodeOrderNotFound {
		t.Errorf("code = %v, want %v", problem["code"], errs.CodeOrderNotFound)
	}
}

func TestListValidatesPagination(t *testing.T) {
	t.Parallel()

	handlers := newHandlers(t, &stubCreator{}, &stubReader{})

	for _, query := range []string{"?limit=0", "?limit=1000", "?limit=many", "?offset=-1"} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders"+query, nil)
		recorder := httptest.NewRecorder()
		handlers.List(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", query, recorder.Code)
		}
	}

	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/orders", nil)
	recorder := httptest.NewRecorder()
	handlers.List(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}

	var page struct {
		Items  []app.OrderView `json:"items"`
		Total  int             `json:"total"`
		Limit  int             `json:"limit"`
		Offset int             `json:"offset"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
		t.Fatalf("response is not json: %v", err)
	}
	if page.Limit != 20 || page.Offset != 0 {
		t.Errorf("defaults = limit %d offset %d, want 20 and 0", page.Limit, page.Offset)
	}
	if page.Items == nil {
		t.Errorf("an empty page must serialise as an empty array, not null")
	}
}
