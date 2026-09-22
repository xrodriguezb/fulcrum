CREATE TABLE orders (
  id          uuid        PRIMARY KEY,
  customer_id uuid        NOT NULL,
  status      text        NOT NULL,
  total_cents bigint      NOT NULL,
  currency    char(3)     NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT orders_status_known CHECK (status IN ('pending', 'confirmed', 'cancelled')),
  CONSTRAINT orders_total_non_negative CHECK (total_cents >= 0)
);

CREATE TABLE order_lines (
  order_id         uuid   NOT NULL REFERENCES orders (id) ON DELETE CASCADE,
  sku              text   NOT NULL,
  quantity         int    NOT NULL,
  unit_price_cents bigint NOT NULL,
  -- The primary key is what makes a duplicate sku within one order impossible at
  -- the schema level. The aggregate rejects it too, and both are wanted: one
  -- states the rule, the other enforces it against writes the aggregate never saw.
  PRIMARY KEY (order_id, sku),
  CONSTRAINT order_lines_quantity_positive CHECK (quantity > 0),
  CONSTRAINT order_lines_price_non_negative CHECK (unit_price_cents >= 0)
);

-- Listing orders is always newest first. The id breaks ties so that keyset
-- pagination is stable when two orders share a timestamp.
CREATE INDEX idx_orders_created_at ON orders (created_at DESC, id DESC);
