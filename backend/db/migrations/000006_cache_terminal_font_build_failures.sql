-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE font_artifacts
    ADD COLUMN failure_code TEXT,
    ADD CONSTRAINT font_artifacts_failure_code_known
        CHECK (failure_code IS NULL OR failure_code IN ('unsupported_codepoints'));

ALTER TABLE font_build_jobs
    ADD COLUMN retryable BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN failure_code TEXT,
    ADD CONSTRAINT font_build_jobs_failure_code_known
        CHECK (failure_code IS NULL OR failure_code IN ('unsupported_codepoints')),
    ADD CONSTRAINT font_build_jobs_terminal_failure_identified
        CHECK (retryable OR failure_code IS NOT NULL);

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE font_build_jobs
    DROP CONSTRAINT IF EXISTS font_build_jobs_terminal_failure_identified,
    DROP CONSTRAINT IF EXISTS font_build_jobs_failure_code_known,
    DROP COLUMN IF EXISTS failure_code,
    DROP COLUMN IF EXISTS retryable;

ALTER TABLE font_artifacts
    DROP CONSTRAINT IF EXISTS font_artifacts_failure_code_known,
    DROP COLUMN IF EXISTS failure_code;
