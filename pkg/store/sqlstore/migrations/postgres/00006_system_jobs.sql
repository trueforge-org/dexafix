-- +goose Up
-- Jobs that belong to no user: the sign-in of a visitor (the code exchange with an identity
-- provider) and the refresh of a provider's keys run before anyone is known.
ALTER TABLE worker_jobs ALTER COLUMN user_id DROP NOT NULL;

-- What a worker learned from an identity provider (its discovery document and signing keys), so
-- the api can verify ID tokens without connecting out. Only a worker's result fills it.
CREATE TABLE auth_provider_metadata (
  slug TEXT PRIMARY KEY REFERENCES auth_providers(slug) ON DELETE CASCADE,
  issuer TEXT NOT NULL DEFAULT '',
  auth_url TEXT NOT NULL DEFAULT '',
  token_url TEXT NOT NULL DEFAULT '',
  userinfo_url TEXT NOT NULL DEFAULT '',
  jwks TEXT NOT NULL DEFAULT '',
  fetched_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE auth_provider_metadata;
DELETE FROM worker_jobs WHERE user_id IS NULL;
ALTER TABLE worker_jobs ALTER COLUMN user_id SET NOT NULL;
