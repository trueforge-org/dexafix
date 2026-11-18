-- +goose Up
-- Jobs that belong to no user: the sign-in of a visitor (the code exchange with an identity
-- provider) and the refresh of a provider's keys run before anyone is known. user_id is therefore
-- optional. SQLite cannot drop NOT NULL, so the table is rebuilt; nothing references it.
CREATE TABLE worker_jobs_new (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  public_id TEXT NOT NULL,
  user_id INTEGER REFERENCES users(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  origin TEXT NOT NULL,
  interactive INTEGER NOT NULL DEFAULT 0,
  dedupe_key TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  payload TEXT NOT NULL DEFAULT '',
  progress TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL,
  timeout_seconds INTEGER NOT NULL,
  created_at TIMESTAMP NOT NULL,
  run_after TIMESTAMP NOT NULL,
  expires_at TIMESTAMP,
  started_at TIMESTAMP,
  finished_at TIMESTAMP,
  updated_at TIMESTAMP NOT NULL,
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_token_hash TEXT NOT NULL DEFAULT '',
  lease_until TIMESTAMP,
  deadline_at TIMESTAMP,
  cancel_requested INTEGER NOT NULL DEFAULT 0
);
INSERT INTO worker_jobs_new SELECT * FROM worker_jobs;
DROP TABLE worker_jobs;
ALTER TABLE worker_jobs_new RENAME TO worker_jobs;
CREATE UNIQUE INDEX idx_worker_jobs_public ON worker_jobs(public_id);
CREATE UNIQUE INDEX idx_worker_jobs_dedupe ON worker_jobs(dedupe_key) WHERE dedupe_key <> '' AND state IN ('queued', 'running');
CREATE INDEX idx_worker_jobs_ready ON worker_jobs(state, run_after);
CREATE INDEX idx_worker_jobs_user ON worker_jobs(user_id, kind, id);
CREATE INDEX idx_worker_jobs_finished ON worker_jobs (finished_at);
CREATE INDEX idx_worker_jobs_kind_finished ON worker_jobs (kind, finished_at);

-- What a worker learned from an identity provider (its discovery document and signing keys), so
-- the api can verify ID tokens without connecting out. Only a worker's result fills it.
CREATE TABLE auth_provider_metadata (
  slug TEXT PRIMARY KEY REFERENCES auth_providers(slug) ON DELETE CASCADE,
  issuer TEXT NOT NULL DEFAULT '',
  auth_url TEXT NOT NULL DEFAULT '',
  token_url TEXT NOT NULL DEFAULT '',
  userinfo_url TEXT NOT NULL DEFAULT '',
  jwks TEXT NOT NULL DEFAULT '',
  fetched_at TIMESTAMP NOT NULL
);

-- +goose Down
DROP TABLE auth_provider_metadata;
DELETE FROM worker_jobs WHERE user_id IS NULL;
CREATE TABLE worker_jobs_old (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  public_id TEXT NOT NULL,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,
  origin TEXT NOT NULL,
  interactive INTEGER NOT NULL DEFAULT 0,
  dedupe_key TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  payload TEXT NOT NULL DEFAULT '',
  progress TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL,
  timeout_seconds INTEGER NOT NULL,
  created_at TIMESTAMP NOT NULL,
  run_after TIMESTAMP NOT NULL,
  expires_at TIMESTAMP,
  started_at TIMESTAMP,
  finished_at TIMESTAMP,
  updated_at TIMESTAMP NOT NULL,
  lease_owner TEXT NOT NULL DEFAULT '',
  lease_token_hash TEXT NOT NULL DEFAULT '',
  lease_until TIMESTAMP,
  deadline_at TIMESTAMP,
  cancel_requested INTEGER NOT NULL DEFAULT 0
);
INSERT INTO worker_jobs_old SELECT * FROM worker_jobs;
DROP TABLE worker_jobs;
ALTER TABLE worker_jobs_old RENAME TO worker_jobs;
CREATE UNIQUE INDEX idx_worker_jobs_public ON worker_jobs(public_id);
CREATE UNIQUE INDEX idx_worker_jobs_dedupe ON worker_jobs(dedupe_key) WHERE dedupe_key <> '' AND state IN ('queued', 'running');
CREATE INDEX idx_worker_jobs_ready ON worker_jobs(state, run_after);
CREATE INDEX idx_worker_jobs_user ON worker_jobs(user_id, kind, id);
CREATE INDEX idx_worker_jobs_finished ON worker_jobs (finished_at);
CREATE INDEX idx_worker_jobs_kind_finished ON worker_jobs (kind, finished_at);
