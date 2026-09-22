CREATE TABLE outbox_events (
  id              uuid        PRIMARY KEY,
  aggregate_id    uuid        NOT NULL,
  aggregate_type  text        NOT NULL,
  event_type      text        NOT NULL,
  event_version   int         NOT NULL,
  payload         jsonb       NOT NULL,
  correlation_id  text        NOT NULL,
  trace_id        text,
  occurred_at     timestamptz NOT NULL,
  claimed_at      timestamptz,
  published_at    timestamptz,
  attempts        int         NOT NULL DEFAULT 0,
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  last_error      text,
  CONSTRAINT outbox_attempts_non_negative CHECK (attempts >= 0),
  CONSTRAINT outbox_event_version_positive CHECK (event_version > 0)
);

-- The table grows monotonically but the claim query only ever touches rows that
-- have not been published, so indexing the whole table would waste space and slow
-- every insert for nothing.
--
-- The index carries occurred_at as well as next_attempt_at because the claim
-- orders by occurred_at. With next_attempt_at alone, every due row would have to
-- be sorted on each claim, which is the cost the index exists to avoid.
CREATE INDEX idx_outbox_unpublished
  ON outbox_events (next_attempt_at, occurred_at)
  WHERE published_at IS NULL;
