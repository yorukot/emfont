-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- These tables predate the Goose migration chain in production. Block writes
-- while values and their backing sequences are reconciled.
LOCK TABLE demo_sentence, font_family, version, static_fonts
    IN SHARE ROW EXCLUSIVE MODE;

UPDATE font_family
SET weights = ARRAY[]::SMALLINT[]
WHERE weights IS NULL;

UPDATE font_family
SET tags = ARRAY[]::TEXT[]
WHERE tags IS NULL;

UPDATE font_family
SET authors = ARRAY[]::TEXT[]
WHERE authors IS NULL;

UPDATE font_family
SET format = 'ttf'
WHERE format IS NULL;

UPDATE static_fonts
SET families = ARRAY[]::TEXT[]
WHERE families IS NULL;

-- +goose StatementBegin
DO $$
DECLARE
    languages_type OID;
    version_start_type OID;
BEGIN
    SELECT atttypid
    INTO languages_type
    FROM pg_attribute
    WHERE attrelid = 'public.font_family'::REGCLASS
      AND attname = 'languages'
      AND attnum > 0
      AND NOT attisdropped;

    IF languages_type = 'json'::REGTYPE THEN
        EXECUTE 'ALTER TABLE public.font_family '
            'ALTER COLUMN languages TYPE JSONB USING languages::JSONB';
    ELSIF languages_type IS DISTINCT FROM 'jsonb'::REGTYPE THEN
        RAISE EXCEPTION 'font_family.languages has unexpected type %',
            format_type(languages_type, NULL);
    END IF;

    SELECT atttypid
    INTO version_start_type
    FROM pg_attribute
    WHERE attrelid = 'public.version'::REGCLASS
      AND attname = 'start'
      AND attnum > 0
      AND NOT attisdropped;

    IF version_start_type = 'timestamp without time zone'::REGTYPE THEN
        -- The legacy PostgreSQL service used UTC. Make that interpretation
        -- explicit instead of depending on the migration session timezone.
        EXECUTE 'ALTER TABLE public.version '
            'ALTER COLUMN start TYPE TIMESTAMPTZ '
            'USING start AT TIME ZONE ''UTC''';
    ELSIF version_start_type IS DISTINCT FROM 'timestamp with time zone'::REGTYPE THEN
        RAISE EXCEPTION 'version.start has unexpected type %',
            format_type(version_start_type, NULL);
    END IF;
END;
$$;
-- +goose StatementEnd

ALTER TABLE font_family
    ALTER COLUMN weights SET DEFAULT ARRAY[]::SMALLINT[],
    ALTER COLUMN weights SET NOT NULL,
    ALTER COLUMN tags SET DEFAULT ARRAY[]::TEXT[],
    ALTER COLUMN tags SET NOT NULL,
    ALTER COLUMN authors SET DEFAULT ARRAY[]::TEXT[],
    ALTER COLUMN authors SET NOT NULL,
    ALTER COLUMN demo_content_id DROP DEFAULT,
    ALTER COLUMN format SET DEFAULT 'ttf',
    ALTER COLUMN format SET NOT NULL;

ALTER TABLE static_fonts
    ALTER COLUMN families SET DEFAULT ARRAY[]::TEXT[],
    ALTER COLUMN families SET NOT NULL;

ALTER TABLE version
    ALTER COLUMN start SET DEFAULT now();

-- The legacy constraints either have a different name or encode a narrower
-- contract than a database created directly from migration 2.
ALTER TABLE font_family
    DROP CONSTRAINT IF EXISTS valid_category,
    DROP CONSTRAINT IF EXISTS fk_demo_content,
    DROP CONSTRAINT IF EXISTS font_family_demo_content_fk,
    DROP CONSTRAINT IF EXISTS font_family_format_check;

ALTER TABLE font_family
    ADD CONSTRAINT font_family_format_check
        CHECK (format IN ('otf', 'ttf', 'woff2')),
    ADD CONSTRAINT font_family_demo_content_fk
        FOREIGN KEY (demo_content_id) REFERENCES demo_sentence(sid) ON DELETE RESTRICT;

-- Explicit-ID legacy seeds can leave these sequences behind table data. Never
-- lower an already-advanced sequence, including one whose rows were deleted.
WITH sequence_state AS (
    SELECT
        sequence_value.last_value,
        sequence_value.is_called,
        (SELECT MAX(sid)::BIGINT FROM demo_sentence) AS table_max
    FROM demo_sentence_sid_seq AS sequence_value
)
SELECT setval(
    'demo_sentence_sid_seq'::REGCLASS,
    GREATEST(last_value, COALESCE(table_max, last_value)),
    is_called OR COALESCE(table_max >= last_value, FALSE)
)
FROM sequence_state;

WITH sequence_state AS (
    SELECT
        sequence_value.last_value,
        sequence_value.is_called,
        (SELECT MAX(bullet)::BIGINT FROM version) AS table_max
    FROM custom_bullet_seq AS sequence_value
)
SELECT setval(
    'custom_bullet_seq'::REGCLASS,
    GREATEST(last_value, COALESCE(table_max, last_value)),
    is_called OR COALESCE(table_max >= last_value, FALSE)
)
FROM sequence_state;

-- +goose Down
-- This repair is intentionally forward-only. Restoring nullable columns and
-- stale sequence state would make both upgraded and fresh databases unsafe.
SELECT TRUE;
