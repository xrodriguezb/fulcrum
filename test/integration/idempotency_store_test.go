//go:build integration

package integration

import (
	"testing"
	"time"

	idempotencyapp "github.com/xrodriguezb/fulcrum/internal/idempotency/app"
	idempotencyinfra "github.com/xrodriguezb/fulcrum/internal/idempotency/infra"
	"github.com/xrodriguezb/fulcrum/internal/platform/postgres"
)

func newStore(t *testing.T) (*idempotencyinfra.Store, *postgres.TxManager) {
	t.Helper()
	pool := newPool(t)
	manager := postgres.NewTxManager(pool)
	return idempotencyinfra.NewStore(manager), manager
}

func TestClaimIsWonOnceAndReportsTheExistingRecord(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()
	expiry := time.Now().Add(time.Hour)

	first, err := store.Claim(ctx, "key-1", []byte("fingerprint"), expiry)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if !first.Claimed {
		t.Fatalf("the first claim must win")
	}

	second, err := store.Claim(ctx, "key-1", []byte("fingerprint"), expiry)
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second.Claimed {
		t.Errorf("the second claim must not win")
	}
	if second.Existing == nil {
		t.Fatalf("a losing claim must report the existing record")
	}
	if second.Existing.Status != idempotencyapp.StatusInProgress {
		t.Errorf("status = %q, want in_progress", second.Existing.Status)
	}
}

func TestCompleteStoresTheResponseForReplay(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()

	if _, err := store.Claim(ctx, "key-2", []byte("fp"), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("claim: %v", err)
	}

	const orderID = "3f8e7d6c-5b4a-4938-8271-0a1b2c3d4e5f"
	body := []byte(`{"id":"3f8e7d6c-5b4a-4938-8271-0a1b2c3d4e5f","status":"pending"}`)
	if err := store.Complete(ctx, "key-2", 201, body, orderID); err != nil {
		t.Fatalf("complete: %v", err)
	}

	record, err := store.Get(ctx, "key-2")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if record.Status != idempotencyapp.StatusCompleted {
		t.Errorf("status = %q, want completed", record.Status)
	}
	if record.ResponseStatus != 201 {
		t.Errorf("response status = %d, want 201", record.ResponseStatus)
	}
	if string(record.ResponseBody) != string(body) {
		t.Errorf("stored body = %s, want %s", record.ResponseBody, body)
	}
	if record.OrderID != orderID {
		t.Errorf("order id = %q, want %q", record.OrderID, orderID)
	}
	if record.CompletedAt == nil {
		t.Errorf("a completed record must carry a completion time")
	}
}

// A failed attempt must not block the client forever: the next request with the
// same key is allowed to try again.
func TestFailedKeyCanBeClaimedAgain(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()
	expiry := time.Now().Add(time.Hour)

	if _, err := store.Claim(ctx, "key-3", []byte("fp"), expiry); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.Fail(ctx, "key-3", "the business transaction rolled back"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	again, err := store.Claim(ctx, "key-3", []byte("fp"), expiry)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if !again.Claimed {
		t.Errorf("a failed key must be claimable again")
	}
}

// An expired key is not a duplicate. It is a key nobody remembers.
func TestExpiredKeyIsTreatedAsFirstRequest(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()

	if _, err := store.Claim(ctx, "key-4", []byte("fp"), time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.Complete(ctx, "key-4", 201, []byte(`{}`), "3f8e7d6c-5b4a-4938-8271-0a1b2c3d4e5f"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	again, err := store.Claim(ctx, "key-4", []byte("fp"), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if !again.Claimed {
		t.Errorf("an expired key must be claimable again")
	}
}

func TestSweepRemovesOnlyExpiredKeys(t *testing.T) {
	store, _ := newStore(t)
	ctx := t.Context()

	if _, err := store.Claim(ctx, "key-old", []byte("fp"), time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := store.Claim(ctx, "key-fresh", []byte("fp"), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("claim: %v", err)
	}

	removed, err := store.DeleteExpired(ctx, time.Now())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed != 1 {
		t.Errorf("sweep removed %d keys, want 1", removed)
	}

	if _, err := store.Get(ctx, "key-fresh"); err != nil {
		t.Errorf("the unexpired key was removed: %v", err)
	}
	if _, err := store.Get(ctx, "key-old"); err == nil {
		t.Errorf("the expired key survived the sweep")
	}
}
