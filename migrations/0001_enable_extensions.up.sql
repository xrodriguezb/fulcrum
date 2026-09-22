-- pgcrypto supplies gen_random_uuid(), which lets the database generate
-- identifiers for rows that are inserted by operators or fixtures rather than by
-- the application. Application writes still supply their own identifiers so that
-- an event can carry the same id the caller saw.
CREATE EXTENSION IF NOT EXISTS pgcrypto;
