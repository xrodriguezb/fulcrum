-- The extension is left in place on rollback. Dropping it would break any other
-- object in the database that depends on it, and its presence is harmless.
SELECT 1;
