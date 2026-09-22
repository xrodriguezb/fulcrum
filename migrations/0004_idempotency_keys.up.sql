CREATE TABLE idempotency_keys (
  key                 text        PRIMARY KEY,
  request_fingerprint bytea       NOT NULL,
  status              text        NOT NULL,
  response_status     int,
  -- The stored response is text, not jsonb. jsonb normalises whitespace and
  -- reorders object keys, so a replayed response would not be byte identical to
  -- the one the first caller received. Idempotent replay is a promise about the
  -- response, and a client that compares or hashes bodies would see two
  -- different answers to the same request.
  response_body       text,
  order_id            uuid,
  created_at          timestamptz NOT NULL DEFAULT now(),
  completed_at        timestamptz,
  expires_at          timestamptz NOT NULL,
  CONSTRAINT idempotency_status_known CHECK (status IN ('in_progress', 'completed', 'failed')),
  -- A completed key without a stored response cannot be replayed, which would
  -- turn a retry into a second attempt at work that already succeeded.
  CONSTRAINT idempotency_completed_has_response CHECK (
    status <> 'completed' OR (response_status IS NOT NULL AND response_body IS NOT NULL)
  )
);

-- The sweep deletes by expiry, so that is the column worth indexing. The table is
-- otherwise only ever read by primary key.
CREATE INDEX idx_idempotency_expires_at ON idempotency_keys (expires_at);
