//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	idempotencyinfra "github.com/xrodriguezb/fulcrum/internal/idempotency/infra"
	inventoryapp "github.com/xrodriguezb/fulcrum/internal/inventory/app"
	inventoryinfra "github.com/xrodriguezb/fulcrum/internal/inventory/infra"
	orderdomain "github.com/xrodriguezb/fulcrum/internal/order/domain"
	orderinfra "github.com/xrodriguezb/fulcrum/internal/order/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/errs"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

// deadPool points at an address nothing is listening on, which is what a database
// outage looks like from inside the process.
func deadPool(t *testing.T) *postgres.TxManager {
	t.Helper()

	cfg, err := pgxpool.ParseConfig("postgres://fulcrum:fulcrum@127.0.0.1:1/fulcrum?sslmode=disable")
	if err != nil {
		t.Fatalf("parse the unreachable dsn: %v", err)
	}
	cfg.ConnConfig.ConnectTimeout = 2 * time.Second

	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatalf("build the pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return postgres.NewTxManager(pool)
}

// A database that cannot be reached is a dependency failure, not a defect in the
// request.
//
// The difference is what a caller does next. Unavailable answers 503 and says
// the request may succeed on retry; internal answers 500 and says it will not.
// Reporting internal told every client to give up during a blip, and put an
// outage into the alert that watches for bugs in the order path.
func TestAnUnreachableDatabaseIsReportedAsUnavailable(t *testing.T) {
	manager := deadPool(t)

	orderID, err := orderdomain.NewOrderID("0b7c6f4e-9d3a-4f1b-8c2d-5a6e7f8b9c0d")
	if err != nil {
		t.Fatalf("build an order id: %v", err)
	}

	cases := map[string]func() error{
		"begin a transaction": func() error {
			return manager.WithinTx(t.Context(), func(context.Context) error { return nil })
		},
		"read an order": func() error {
			_, readErr := orderinfra.NewRepository(manager).ByID(t.Context(), orderID)
			return readErr
		},
		"list orders": func() error {
			_, _, listErr := orderinfra.NewRepository(manager).Page(t.Context(), 20, 0)
			return listErr
		},
		"claim an idempotency key": func() error {
			_, claimErr := idempotencyinfra.NewStore(manager).
				Claim(t.Context(), "key", []byte("fingerprint"), time.Now().Add(time.Hour))
			return claimErr
		},
		"reserve inventory": func() error {
			_, reserveErr := inventoryinfra.NewReserver(manager).Reserve(t.Context(),
				[]inventoryapp.ReservationRequest{{SKU: "WIDGET-001", Quantity: 1}})
			return reserveErr
		},
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatalf("an unreachable database must fail")
			}
			if kind := errs.KindOf(err); kind != errs.KindUnavailable {
				t.Errorf("kind = %v, want unavailable", kind)
			}
			if status := errs.HTTPStatus(errs.KindOf(err)); status != 503 {
				t.Errorf("status = %d, want 503", status)
			}
			if !errs.IsTransient(err) {
				t.Errorf("an unreachable database must be retryable")
			}
			if message := errs.PublicMessage(err); message == "" {
				t.Errorf("the caller was given no message at all")
			}
		})
	}
}
