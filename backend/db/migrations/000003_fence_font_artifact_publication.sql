-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE font_artifacts
    ADD COLUMN generation BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN artifact_protocol_version TEXT NOT NULL DEFAULT 'v1',
    ADD CONSTRAINT font_artifacts_generation_nonnegative CHECK (generation >= 0);

ALTER TABLE font_build_jobs
    ALTER COLUMN attempts TYPE BIGINT,
    ADD COLUMN next_attempt_at TIMESTAMPTZ;

CREATE INDEX font_build_jobs_retry_idx
    ON font_build_jobs (next_attempt_at, artifact_key)
    WHERE status = 'failed';

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP INDEX IF EXISTS font_build_jobs_retry_idx;

ALTER TABLE font_build_jobs
    DROP COLUMN IF EXISTS next_attempt_at,
    -- Version 2 stored attempts as INT. Clamp values that were legal while
    -- version 3 was active so an emergency rollback cannot fail halfway.
    ALTER COLUMN attempts TYPE INT USING LEAST(attempts, 2147483647)::INT;

ALTER TABLE font_artifacts
    DROP CONSTRAINT IF EXISTS font_artifacts_generation_nonnegative,
    DROP COLUMN IF EXISTS artifact_protocol_version,
    DROP COLUMN IF EXISTS generation;
