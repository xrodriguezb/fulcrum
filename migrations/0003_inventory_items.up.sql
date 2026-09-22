CREATE TABLE inventory_items (
  sku              text        PRIMARY KEY,
  available        int         NOT NULL,
  reserved         int         NOT NULL DEFAULT 0,
  unit_price_cents bigint      NOT NULL,
  currency         char(3)     NOT NULL DEFAULT 'EUR',
  version          int         NOT NULL DEFAULT 1,
  updated_at       timestamptz NOT NULL DEFAULT now(),
  -- These two constraints must never fire in correct operation: the reservation
  -- statement already refuses to take more than is available. They exist so that
  -- a bug, a migration or a manual write cannot leave the table in a state that
  -- claims stock nobody has. One test deliberately bypasses the application path
  -- to prove the backstop is real.
  CONSTRAINT inventory_available_non_negative CHECK (available >= 0),
  CONSTRAINT inventory_reserved_non_negative CHECK (reserved >= 0),
  CONSTRAINT inventory_price_non_negative CHECK (unit_price_cents >= 0)
);
