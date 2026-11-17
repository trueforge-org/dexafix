-- +goose Up
-- Automatic syncs. The *_allowed columns are set by an administrator; the others are the user's own
-- switch, which is off until they turn it on. Taking the permission away also clears the switch.
ALTER TABLE users ADD COLUMN withings_auto_allowed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN withings_auto INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN strengthlevel_push_allowed INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN strengthlevel_push INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE users DROP COLUMN withings_auto_allowed;
ALTER TABLE users DROP COLUMN withings_auto;
ALTER TABLE users DROP COLUMN strengthlevel_push_allowed;
ALTER TABLE users DROP COLUMN strengthlevel_push;
