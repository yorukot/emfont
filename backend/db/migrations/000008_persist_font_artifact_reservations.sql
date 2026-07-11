-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE font_artifacts
    ADD COLUMN reservation_bytes BIGINT;

UPDATE font_artifacts
SET reservation_bytes = GREATEST(size_bytes, 134217728::BIGINT);

ALTER TABLE font_artifacts
    ALTER COLUMN reservation_bytes SET DEFAULT 134217728,
    ALTER COLUMN reservation_bytes SET NOT NULL,
    ADD CONSTRAINT font_artifacts_reservation_bytes_positive
        CHECK (reservation_bytes > 0),
    ADD CONSTRAINT font_artifacts_size_within_reservation
        CHECK (size_bytes <= reservation_bytes);

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

ALTER TABLE font_artifacts
    DROP COLUMN IF EXISTS reservation_bytes;
