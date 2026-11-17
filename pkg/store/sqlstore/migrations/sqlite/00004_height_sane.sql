-- +goose Up
-- A height that is not a number (SQLite may keep a NaN as text) made every page of that user fail and
-- broke the user list for the administrators. Heights outside what the profile form allows (50 to
-- 280 cm) are cleared. SQLite cannot add a CHECK to a table without rebuilding it, so the application
-- keeps new values in range (the profile form and UpdateUserProfile).
UPDATE users SET height_cm = NULL
 WHERE height_cm IS NOT NULL AND NOT (typeof(height_cm) IN ('real', 'integer') AND height_cm >= 50 AND height_cm <= 280);

-- +goose Down
-- The cleared heights are gone, and nothing was added.
SELECT 1;
