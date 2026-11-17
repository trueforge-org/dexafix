-- +goose Up
-- A height of NaN, which PostgreSQL stores, made every page of that user fail and broke the user list
-- for the administrators. Heights outside what the profile form allows (50 to 280 cm) are cleared
-- (NaN sorts above every number, so it fails the second comparison) and cannot come back.
UPDATE users SET height_cm = NULL WHERE height_cm IS NOT NULL AND NOT (height_cm >= 50 AND height_cm <= 280);
ALTER TABLE users ADD CONSTRAINT users_height_cm_sane CHECK (height_cm IS NULL OR (height_cm >= 50 AND height_cm <= 280));

-- +goose Down
ALTER TABLE users DROP CONSTRAINT users_height_cm_sane;
