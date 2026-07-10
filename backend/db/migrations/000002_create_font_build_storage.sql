-- +goose Up
CREATE TABLE IF NOT EXISTS demo_sentence (
    sid SERIAL PRIMARY KEY,
    content TEXT,
    language VARCHAR(10)
);

CREATE TABLE IF NOT EXISTS font_family (
    id TEXT PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    name_zh TEXT,
    name_en TEXT,
    weights SMALLINT[] NOT NULL DEFAULT ARRAY[]::SMALLINT[],
    license TEXT,
    version TEXT,
    description TEXT,
    category TEXT,
    family TEXT,
    tags TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    repo_url TEXT,
    authors TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    languages JSONB,
    demo_content_id INT,
    format TEXT NOT NULL DEFAULT 'ttf' CHECK (format IN ('otf', 'ttf', 'woff2')),
    CONSTRAINT font_family_demo_content_fk
        FOREIGN KEY (demo_content_id) REFERENCES demo_sentence(sid) ON DELETE RESTRICT
);

CREATE SEQUENCE IF NOT EXISTS custom_bullet_seq START WITH 100;

CREATE TABLE IF NOT EXISTS version (
    bullet INT PRIMARY KEY DEFAULT nextval('custom_bullet_seq'),
    start TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO version (start)
SELECT now()
WHERE NOT EXISTS (SELECT 1 FROM version);

CREATE TABLE IF NOT EXISTS static_fonts (
    char TEXT PRIMARY KEY,
    pack SMALLINT NOT NULL,
    families TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
    use_count INT NOT NULL DEFAULT 0
);

ALTER TABLE static_fonts ALTER COLUMN char TYPE TEXT;

CREATE INDEX IF NOT EXISTS emfont_backend_static_fonts_pack_idx ON static_fonts (pack);

CREATE TABLE IF NOT EXISTS font_sources (
    id BIGSERIAL PRIMARY KEY,
    family_id TEXT NOT NULL REFERENCES font_family(id) ON DELETE CASCADE,
    weight SMALLINT NOT NULL,
    format TEXT NOT NULL CHECK (format IN ('otf', 'ttf', 'woff2')),
    object_key TEXT NOT NULL,
    checksum_sha256 TEXT,
    size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    source_version TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (family_id, weight)
);

CREATE TABLE IF NOT EXISTS font_artifacts (
    artifact_key TEXT PRIMARY KEY,
    kind TEXT NOT NULL CHECK (kind IN ('dynamic', 'static')),
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'ready', 'failed', 'missing', 'stale')),
    family_id TEXT NOT NULL REFERENCES font_family(id) ON DELETE CASCADE,
    weight SMALLINT NOT NULL,
    version INT,
    pack TEXT,
    word_hash TEXT,
    normalized_word_set TEXT,
    source_checksum_sha256 TEXT,
    builder_version TEXT NOT NULL,
    object_key TEXT NOT NULL,
    content_type TEXT NOT NULL DEFAULT 'font/woff2',
    size_bytes BIGINT NOT NULL DEFAULT 0 CHECK (size_bytes >= 0),
    etag TEXT,
    checksum_sha256 TEXT,
    error TEXT,
    last_verified_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (object_key)
);

CREATE INDEX IF NOT EXISTS font_artifacts_family_weight_idx
    ON font_artifacts (family_id, weight, status);
CREATE INDEX IF NOT EXISTS font_artifacts_status_idx
    ON font_artifacts (status, updated_at);

CREATE TABLE IF NOT EXISTS font_build_jobs (
    id BIGSERIAL PRIMARY KEY,
    artifact_key TEXT NOT NULL UNIQUE REFERENCES font_artifacts(artifact_key) ON DELETE CASCADE,
    status TEXT NOT NULL CHECK (status IN ('pending', 'running', 'ready', 'failed')),
    attempts INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    locked_by TEXT,
    lease_until TIMESTAMPTZ,
    error TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS font_build_jobs_claim_idx
    ON font_build_jobs (status, lease_until, created_at);

-- +goose Down
DROP TABLE IF EXISTS font_build_jobs;
DROP TABLE IF EXISTS font_artifacts;
DROP TABLE IF EXISTS font_sources;
DROP INDEX IF EXISTS emfont_backend_static_fonts_pack_idx;
