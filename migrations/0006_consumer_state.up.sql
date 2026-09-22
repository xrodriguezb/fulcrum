-- These two tables belong to the consumer, which arrives in a later phase. The
-- schema lands here because the operations console reads the dead letter queue,
-- and an endpoint that returns an empty list because the table does not exist is
-- indistinguishable from an endpoint that says everything is healthy.

CREATE TABLE processed_events (
  consumer_name text        NOT NULL,
  event_id      uuid        NOT NULL,
  processed_at  timestamptz NOT NULL DEFAULT now(),
  -- The primary key is the deduplication. Inserting it in the same transaction
  -- as the side effect is what makes at-least-once delivery produce exactly one
  -- effect.
  PRIMARY KEY (consumer_name, event_id)
);

CREATE TABLE dead_letter_events (
  id              uuid        PRIMARY KEY,
  event_id        uuid        NOT NULL,
  consumer_name   text        NOT NULL,
  event_type      text        NOT NULL,
  payload         jsonb       NOT NULL,
  attempts        int         NOT NULL,
  first_failed_at timestamptz NOT NULL,
  last_failed_at  timestamptz NOT NULL,
  -- An operator-facing summary, never a driver error or a stack trace. A test
  -- asserts that.
  failure_reason  text        NOT NULL,
  correlation_id  text,
  trace_id        text,
  CONSTRAINT dead_letter_attempts_non_negative CHECK (attempts >= 0)
);

-- The console lists the newest failures first, and an operator triaging an
-- incident filters by consumer.
CREATE INDEX idx_dead_letter_last_failed ON dead_letter_events (last_failed_at DESC);
CREATE INDEX idx_dead_letter_consumer ON dead_letter_events (consumer_name, last_failed_at DESC);
