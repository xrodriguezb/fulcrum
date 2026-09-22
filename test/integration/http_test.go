//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/xrodriguezb/fulcrum/internal/api"
	idempotencyinfra "github.com/xrodriguezb/fulcrum/internal/idempotency/infra"
	inventoryinfra "github.com/xrodriguezb/fulcrum/internal/inventory/infra"
	opsinfra "github.com/xrodriguezb/fulcrum/internal/ops/infra"
	orderapp "github.com/xrodriguezb/fulcrum/internal/order/app"
	orderinfra "github.com/xrodriguezb/fulcrum/internal/order/infra"
	outboxinfra "github.com/xrodriguezb/fulcrum/internal/outbox/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/httpx"
	"github.com/xrodriguezb/fulcrum/internal/platform/logging"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

type apiHarness struct {
	server *httptest.Server
	pool   *pgxpool.Pool
}

func newAPI(t *testing.T, checks ...httpx.Check) apiHarness {
	t.Helper()

	pool := newPool(t)
	manager := postgres.NewTxManager(pool)
	logger := logging.NewJSON(io.Discard, 0)
	registry := prometheus.NewRegistry()

	creator, err := orderapp.NewCreateOrderHandler(orderapp.CreateOrderDeps{
		Tx:           manager,
		Orders:       orderinfra.NewRepository(manager),
		Inventory:    inventoryinfra.NewReserver(manager),
		Outbox:       outboxinfra.NewWriter(manager),
		Idempotency:  idempotencyinfra.NewStore(manager),
		Clock:        time.Now,
		IDs:          &sequentialIDs{},
		KeyTTL:       time.Hour,
		MaxKeyLength: 255,
		Metrics:      orderinfra.NewMetrics(registry),
	})
	if err != nil {
		t.Fatalf("wire the use case: %v", err)
	}

	chain, err := httpx.Chain(httpx.MiddlewareConfig{
		Logger:         logger,
		Metrics:        httpx.NewMetrics(registry),
		MaxBodyBytes:   2048,
		AllowedOrigins: []string{"http://localhost:5173"},
	})
	if err != nil {
		t.Fatalf("build the middleware chain: %v", err)
	}

	if len(checks) == 0 {
		checks = []httpx.Check{{Name: "postgres", Critical: true, Probe: func(ctx context.Context) error {
			return postgres.HealthCheck(ctx, pool, time.Second)
		}}}
	}

	handler := api.New(api.Deps{
		Orders:   orderinfra.NewHandlers(creator, orderinfra.NewRepository(manager), logger),
		Ops:      opsinfra.NewHandlers(opsinfra.NewReader(manager), inventoryinfra.NewReserver(manager), logger),
		Checks:   checks,
		Registry: registry,
		Logger:   logger,
		Chain:    chain,
	})

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return apiHarness{server: server, pool: pool}
}

// apiResponse is the fully consumed response. The helper reads and closes the
// body itself so that no test can leak a connection by forgetting to.
type apiResponse struct {
	Status int
	Header http.Header
	Body   string
}

func (h apiHarness) do(t *testing.T, method, path, body string, headers map[string]string) apiResponse {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequestWithContext(t.Context(), method, h.server.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := h.server.Client().Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() {
		if closeErr := response.Body.Close(); closeErr != nil {
			t.Errorf("close response body: %v", closeErr)
		}
	}()

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return apiResponse{Status: response.StatusCode, Header: response.Header, Body: string(payload)}
}

const orderBody = `{"customer_id":"11111111-2222-4333-8444-555555555555","lines":[{"sku":"WIDGET-001","quantity":2}]}`

func TestAPICreatesAndReadsBackAnOrder(t *testing.T) {
	h := newAPI(t)
	seedInventory(t, h.pool, "WIDGET-001", 5, 1050)

	created := h.do(t, http.MethodPost, "/api/v1/orders", orderBody, map[string]string{
		"Content-Type":    "application/json",
		"Idempotency-Key": "http-key-1",
	})
	if created.Status != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (%s)", created.Status, created.Body)
	}

	var view orderapp.OrderView
	if err := json.Unmarshal([]byte(created.Body), &view); err != nil {
		t.Fatalf("response is not an order: %v", err)
	}
	if view.TotalCents != 2100 {
		t.Errorf("total = %d, want 2100", view.TotalCents)
	}

	fetched := h.do(t, http.MethodGet, "/api/v1/orders/"+view.ID, "", nil)
	if fetched.Status != http.StatusOK {
		t.Fatalf("get status = %d, want 200", fetched.Status)
	}
	if !strings.Contains(fetched.Body, view.ID) {
		t.Errorf("the fetched order does not carry the id")
	}

	listed := h.do(t, http.MethodGet, "/api/v1/orders?limit=10", "", nil)
	if listed.Status != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listed.Status)
	}
	if !strings.Contains(listed.Body, `"total":1`) {
		t.Errorf("the listing does not report the order")
	}
}

func TestAPIReplaysADuplicate(t *testing.T) {
	h := newAPI(t)
	seedInventory(t, h.pool, "WIDGET-001", 5, 1050)

	headers := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "http-key-replay"}
	first := h.do(t, http.MethodPost, "/api/v1/orders", orderBody, headers)
	firstBody := first.Body

	second := h.do(t, http.MethodPost, "/api/v1/orders", orderBody, headers)
	if second.Status != http.StatusCreated {
		t.Fatalf("replay status = %d, want 201", second.Status)
	}
	if second.Header.Get("Idempotent-Replay") != "true" {
		t.Errorf("the replay is not marked")
	}
	if second.Body != firstBody {
		t.Errorf("the replayed body differs from the original")
	}
}

func TestAPIRejectsBadRequests(t *testing.T) {
	h := newAPI(t)
	seedInventory(t, h.pool, "WIDGET-001", 1, 1050)

	cases := []struct {
		name       string
		body       string
		headers    map[string]string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "missing idempotency key",
			body:       orderBody,
			headers:    map[string]string{"Content-Type": "application/json"},
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeIdempotencyKeyRequired,
		},
		{
			name:       "wrong content type",
			body:       orderBody,
			headers:    map[string]string{"Content-Type": "text/plain", "Idempotency-Key": "k1"},
			wantStatus: http.StatusUnsupportedMediaType,
			wantCode:   errs.CodeUnsupportedMediaType,
		},
		{
			name:       "oversize body",
			body:       `{"customer_id":"11111111-2222-4333-8444-555555555555","padding":"` + strings.Repeat("x", 4096) + `"}`,
			headers:    map[string]string{"Content-Type": "application/json", "Idempotency-Key": "k2"},
			wantStatus: http.StatusRequestEntityTooLarge,
			wantCode:   errs.CodeRequestTooLarge,
		},
		{
			name:       "malformed json",
			body:       `{"customer_id":`,
			headers:    map[string]string{"Content-Type": "application/json", "Idempotency-Key": "k3"},
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeValidationFailed,
		},
		{
			name:       "unknown field",
			body:       `{"customer_id":"11111111-2222-4333-8444-555555555555","lines":[{"sku":"WIDGET-001","quantity":1}],"coupon":"x"}`,
			headers:    map[string]string{"Content-Type": "application/json", "Idempotency-Key": "k4"},
			wantStatus: http.StatusBadRequest,
			wantCode:   errs.CodeValidationFailed,
		},
		{
			name:       "insufficient inventory",
			body:       `{"customer_id":"11111111-2222-4333-8444-555555555555","lines":[{"sku":"WIDGET-001","quantity":5}]}`,
			headers:    map[string]string{"Content-Type": "application/json", "Idempotency-Key": "k5"},
			wantStatus: http.StatusConflict,
			wantCode:   errs.CodeInventoryInsufficient,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := h.do(t, http.MethodPost, "/api/v1/orders", tc.body, tc.headers)
			if response.Status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", response.Status, tc.wantStatus)
			}
			body := response.Body
			if got := response.Header.Get("Content-Type"); got != "application/problem+json" {
				t.Errorf("content type = %q, want application/problem+json", got)
			}
			var problem map[string]any
			if err := json.Unmarshal([]byte(body), &problem); err != nil {
				t.Fatalf("response is not a problem document: %v (%s)", err, body)
			}
			if problem["code"] != tc.wantCode {
				t.Errorf("code = %v, want %v", problem["code"], tc.wantCode)
			}
			for _, leak := range []string{"pgx", "postgres", "sql:", "goroutine"} {
				if strings.Contains(strings.ToLower(body), leak) {
					t.Errorf("the response leaked %q: %s", leak, body)
				}
			}
		})
	}
}

// The probe has to report the truth when the database is gone, which is the only
// way a load balancer can stop sending traffic to this instance.
func TestReadinessFailsWhenTheDatabaseIsUnreachable(t *testing.T) {
	h := newAPI(t, httpx.Check{Name: "postgres", Critical: true, Probe: func(context.Context) error {
		return errs.Unavailable("the database is not reachable", nil)
	}})

	response := h.do(t, http.MethodGet, "/readyz", "", nil)
	if response.Status != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d, want 503", response.Status)
	}

	live := h.do(t, http.MethodGet, "/healthz", "", nil)
	if live.Status != http.StatusOK {
		t.Errorf("healthz status = %d, want 200 even when a dependency is down", live.Status)
	}
}

func TestOperationalEndpointsAndMetricsAreServed(t *testing.T) {
	h := newAPI(t)
	seedInventory(t, h.pool, "WIDGET-001", 5, 1050)

	if _, err := http.NewRequestWithContext(t.Context(), http.MethodGet, h.server.URL, nil); err != nil {
		t.Fatalf("build request: %v", err)
	}

	h.do(t, http.MethodPost, "/api/v1/orders", orderBody, map[string]string{
		"Content-Type": "application/json", "Idempotency-Key": "ops-key",
	})

	outbox := h.do(t, http.MethodGet, "/api/v1/ops/outbox", "", nil)
	if outbox.Status != http.StatusOK {
		t.Fatalf("outbox status = %d, want 200", outbox.Status)
	}
	if !strings.Contains(outbox.Body, `"pending":1`) {
		t.Errorf("the outbox snapshot does not report the pending event")
	}

	dlq := h.do(t, http.MethodGet, "/api/v1/ops/dead-letters", "", nil)
	if dlq.Status != http.StatusOK {
		t.Errorf("dead letters status = %d, want 200", dlq.Status)
	}

	inventory := h.do(t, http.MethodGet, "/api/v1/inventory", "", nil)
	if !strings.Contains(inventory.Body, "WIDGET-001") {
		t.Errorf("the inventory endpoint does not list the seeded sku")
	}

	metrics := h.do(t, http.MethodGet, "/metrics", "", nil)
	if metrics.Status != http.StatusOK {
		t.Fatalf("metrics status = %d, want 200", metrics.Status)
	}
	body := metrics.Body
	if !strings.Contains(body, "http_requests_total") {
		t.Errorf("the metrics endpoint does not expose request counters")
	}
	// The route label must be the pattern, not the path, or one metric series
	// appears per order id.
	if strings.Contains(body, "/api/v1/orders/00000000-") {
		t.Errorf("an identifier reached a metric label: %s", body)
	}
}

// A page of orders must cost one query for the orders and one for their lines.
// The first implementation issued one query per order, which is invisible with
// two orders in the table and expensive with a hundred.
func TestListingOrdersDoesNotQueryPerOrder(t *testing.T) {
	h := newAPI(t)
	seedInventory(t, h.pool, "WIDGET-001", 100, 1050)

	const orders = 12
	for i := range orders {
		response := h.do(t, http.MethodPost, "/api/v1/orders", orderBody, map[string]string{
			"Content-Type":    "application/json",
			"Idempotency-Key": fmt.Sprintf("page-key-%d", i),
		})
		if response.Status != http.StatusCreated {
			t.Fatalf("create %d: status %d", i, response.Status)
		}
	}

	before := queryCount(t, h.pool)
	listed := h.do(t, http.MethodGet, "/api/v1/orders?limit=12", "", nil)
	if listed.Status != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listed.Status)
	}
	after := queryCount(t, h.pool)

	// The page runs one statement for the orders and one for the lines. The
	// allowance covers the statistics read itself and pool bookkeeping.
	if queries := after - before; queries > 6 {
		t.Errorf("listing %d orders issued %d round trips, want a constant handful", orders, queries)
	}
	if !strings.Contains(listed.Body, `"total":12`) {
		t.Errorf("the page does not report every order: %s", listed.Body)
	}
}

// queryCount reads the transaction counter PostgreSQL keeps for this database.
// A query outside an explicit transaction counts as one, which is what makes it
// a usable proxy for round trips here.
func queryCount(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()

	var count int64
	const query = `SELECT xact_commit + xact_rollback FROM pg_stat_database WHERE datname = current_database()`
	if err := pool.QueryRow(t.Context(), query).Scan(&count); err != nil {
		t.Fatalf("read statement counter: %v", err)
	}
	return count
}

// The business metrics answer questions the transport metrics cannot: how many
// orders exist, how often stock ran out, and how keyed requests were answered.
// A 409 counted by http_requests_total does not distinguish a sold out sku from
// a client reusing a key.
func TestBusinessMetricsAreExposed(t *testing.T) {
	h := newAPI(t)
	seedInventory(t, h.pool, "WIDGET-001", 2, 1050)

	headers := map[string]string{"Content-Type": "application/json", "Idempotency-Key": "metrics-1"}
	if created := h.do(t, http.MethodPost, "/api/v1/orders", orderBody, headers); created.Status != http.StatusCreated {
		t.Fatalf("create: status %d (%s)", created.Status, created.Body)
	}
	// The same key again: a replay, not a second order.
	h.do(t, http.MethodPost, "/api/v1/orders", orderBody, headers)

	// Two units are gone, so this one cannot be satisfied.
	conflict := h.do(t, http.MethodPost, "/api/v1/orders", orderBody,
		map[string]string{"Content-Type": "application/json", "Idempotency-Key": "metrics-2"})
	if conflict.Status != http.StatusConflict {
		t.Fatalf("expected a conflict, got %d (%s)", conflict.Status, conflict.Body)
	}

	metrics := h.do(t, http.MethodGet, "/metrics", "", nil).Body

	for _, want := range []string{
		"orders_created_total 1",
		"inventory_conflicts_total 1",
		`idempotency_hits_total{outcome="claimed"} 2`,
		`idempotency_hits_total{outcome="replayed"} 1`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("the metrics endpoint does not expose %q", want)
		}
	}
}
