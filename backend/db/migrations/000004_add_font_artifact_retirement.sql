-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE font_artifacts
    ADD COLUMN retired_at TIMESTAMPTZ;

UPDATE font_artifacts
SET retired_at = updated_at
WHERE status = 'stale'
  AND retired_at IS NULL;

CREATE INDEX font_artifacts_retired_idx
    ON font_artifacts (retired_at, artifact_key)
    WHERE status = 'stale';

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP INDEX IF EXISTS font_artifacts_retired_idx;

ALTER TABLE font_artifacts
    DROP COLUMN IF EXISTS retired_at;
