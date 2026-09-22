-- The application does not own its schema.
--
-- fulcrum_app can read and write rows and nothing else: it cannot create, alter
-- or drop a table, so an injection or a compromised container cannot change the
-- shape of the database. Migrations run as the owner, which is a separate
-- connection string used once at startup.
--
-- This runs only on an empty data directory, which is where the compose stack
-- starts from.

CREATE ROLE fulcrum_app WITH LOGIN PASSWORD 'fulcrum_app_password';

GRANT CONNECT ON DATABASE fulcrum TO fulcrum_app;
GRANT USAGE ON SCHEMA public TO fulcrum_app;

-- Tables that already exist, and tables the migration runner creates later. The
-- default privileges are the important half: without them every new migration
-- would need a matching grant, and the one that is forgotten fails in production.
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO fulcrum_app;
GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO fulcrum_app;

ALTER DEFAULT PRIVILEGES FOR ROLE fulcrum IN SCHEMA public
  GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO fulcrum_app;
ALTER DEFAULT PRIVILEGES FOR ROLE fulcrum IN SCHEMA public
  GRANT USAGE, SELECT ON SEQUENCES TO fulcrum_app;

-- Creating objects is the owner's job, and saying so explicitly documents the
-- boundary for anyone reading the database rather than this file.
REVOKE CREATE ON SCHEMA public FROM fulcrum_app;
