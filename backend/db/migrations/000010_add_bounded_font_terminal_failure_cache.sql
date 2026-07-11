-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- Terminal failures are cache entries, not generated artifacts. Keeping them
-- outside font_artifacts prevents unsupported requests from consuming the
-- globally bounded artifact slots needed by publishable outputs.
CREATE TABLE font_terminal_failures (
    artifact_key TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('dynamic', 'static')),
    family_id TEXT NOT NULL REFERENCES font_family(id) ON DELETE CASCADE,
    weight SMALLINT NOT NULL,
    version INT,
    pack TEXT,
    word_hash TEXT,
    normalized_word_set TEXT,
    source_checksum_sha256 TEXT,
    builder_version TEXT NOT NULL,
    artifact_protocol_version TEXT NOT NULL,
    content_type TEXT NOT NULL,
    failure_code TEXT NOT NULL,
    error TEXT,
    cached_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT font_terminal_failures_failure_code_known
        CHECK (failure_code IN ('unsupported_codepoints'))
);

CREATE INDEX font_terminal_failures_eviction_idx
    ON font_terminal_failures (cached_at, artifact_key);

-- Version 6 stored terminal failures in the globally counted artifact table.
-- They are derived negative-cache state, so start the bounded cache empty and
-- release every legacy artifact slot during the migration.
DELETE FROM font_artifacts
WHERE status = 'failed' AND failure_code IS NOT NULL;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TABLE IF EXISTS font_terminal_failures;
