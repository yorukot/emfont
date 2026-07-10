-- +goose Up
CREATE TABLE IF NOT EXISTS system_metadata (
    metadata_key TEXT PRIMARY KEY,
    metadata_value TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

COMMENT ON TABLE system_metadata IS 'Small key/value table for backend system metadata.';
COMMENT ON COLUMN system_metadata.metadata_key IS 'Stable metadata key.';
COMMENT ON COLUMN system_metadata.metadata_value IS 'Metadata value encoded by the caller.';

-- +goose Down
DROP TABLE IF EXISTS system_metadata;
