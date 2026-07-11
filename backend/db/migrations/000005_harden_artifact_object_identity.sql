-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

CREATE SEQUENCE font_artifact_fence_seq AS BIGINT;

WITH fence_state AS (
    SELECT GREATEST(
        COALESCE((SELECT MAX(generation) FROM font_artifacts), 0),
        COALESCE((SELECT MAX(attempts) FROM font_build_jobs), 0),
        COALESCE((
            SELECT CASE
                WHEN metadata_value ~ '^[0-9]{1,18}$'
                    OR (metadata_value ~ '^[0-9]{19}$' AND metadata_value <= '9223372036854775807')
                    THEN metadata_value::BIGINT
                ELSE 0
            END
            FROM system_metadata
            WHERE metadata_key = 'font_artifact_fence_high_water'
        ), 0)
    ) AS high_water
)
SELECT setval(
    'font_artifact_fence_seq',
    GREATEST(high_water, 1),
    high_water > 0
)
FROM fence_state;

ALTER TABLE font_build_jobs
    ADD COLUMN fence BIGINT NOT NULL DEFAULT 0,
    ADD CONSTRAINT font_build_jobs_fence_nonnegative CHECK (fence >= 0);

UPDATE font_build_jobs
SET fence = attempts
WHERE fence = 0 AND attempts > 0;

ALTER TABLE font_artifacts
    ADD COLUMN object_version_id TEXT;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- A rollback removes the v5 fence column and sequence, but the high-water
-- mark must survive a later re-upgrade. Reusing a fence could let a delayed
-- worker publish over a newer claim.
INSERT INTO system_metadata (metadata_key, metadata_value, created_at, updated_at)
SELECT
    'font_artifact_fence_high_water',
    last_value::TEXT,
    now(),
    now()
FROM font_artifact_fence_seq
ON CONFLICT (metadata_key) DO UPDATE
SET
    metadata_value = GREATEST(
        CASE
            WHEN system_metadata.metadata_value ~ '^[0-9]{1,18}$'
                OR (
                    system_metadata.metadata_value ~ '^[0-9]{19}$'
                    AND system_metadata.metadata_value <= '9223372036854775807'
                )
                THEN system_metadata.metadata_value::BIGINT
            ELSE 0
        END,
        EXCLUDED.metadata_value::BIGINT
    )::TEXT,
    updated_at = now();

ALTER TABLE font_artifacts
    DROP COLUMN IF EXISTS object_version_id;

ALTER TABLE font_build_jobs
    DROP CONSTRAINT IF EXISTS font_build_jobs_fence_nonnegative,
    DROP COLUMN IF EXISTS fence;

DROP SEQUENCE IF EXISTS font_artifact_fence_seq;
