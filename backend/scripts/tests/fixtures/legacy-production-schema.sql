-- Reproducible post-migration schema from migrates/001-004. The data covers
-- nullable legacy columns, explicit IDs, legacy-only tables, and timestamps.

CREATE TABLE schemaversion (
    version BIGINT PRIMARY KEY,
    name TEXT,
    md5 TEXT,
    run_at TIMESTAMPTZ
);

CREATE TABLE demo_sentence (
    sid SERIAL PRIMARY KEY,
    content TEXT,
    language VARCHAR(10)
);

CREATE TABLE font_family (
    id TEXT PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    name_zh TEXT DEFAULT NULL,
    name_en TEXT DEFAULT NULL,
    weights SMALLINT[] DEFAULT NULL,
    license TEXT DEFAULT NULL,
    version TEXT DEFAULT NULL,
    description TEXT DEFAULT NULL,
    category TEXT DEFAULT NULL,
    family TEXT DEFAULT NULL,
    tags TEXT[] DEFAULT ARRAY[]::TEXT[],
    repo_url TEXT DEFAULT NULL,
    authors TEXT[] DEFAULT ARRAY[]::TEXT[],
    languages JSON,
    demo_content_id INT DEFAULT 1,
    format TEXT DEFAULT 'ttf' CHECK (format IN ('otf', 'ttf')),
    CONSTRAINT valid_category
        CHECK (category IN ('serif', 'sans-serif', 'monospace', 'cursive', 'fantasy')),
    CONSTRAINT fk_demo_content
        FOREIGN KEY (demo_content_id) REFERENCES demo_sentence(sid) ON DELETE RESTRICT
);

CREATE SEQUENCE custom_bullet_seq START WITH 100;

CREATE TABLE version (
    bullet INT PRIMARY KEY DEFAULT nextval('custom_bullet_seq'),
    start TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE dynamic_fonts (
    id SERIAL PRIMARY KEY,
    family_id TEXT NOT NULL,
    weight SMALLINT,
    use_count INT NOT NULL DEFAULT 1,
    last_use TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    create_time TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    hash CHAR(40) NOT NULL UNIQUE,
    FOREIGN KEY (family_id) REFERENCES font_family(id)
);

CREATE TABLE r2_files (
    prefix TEXT,
    file_name TEXT,
    update_time TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (prefix, file_name)
);

CREATE TABLE static_fonts (
    char VARCHAR(2) PRIMARY KEY,
    pack SMALLINT NOT NULL,
    families TEXT[] DEFAULT ARRAY[]::TEXT[],
    use_count INT NOT NULL DEFAULT 0
);

CREATE TABLE usage_log (
    id SERIAL PRIMARY KEY,
    family_id TEXT NOT NULL,
    weight SMALLINT DEFAULT 400,
    referer TEXT,
    text TEXT NOT NULL,
    min BOOLEAN DEFAULT FALSE,
    request_time TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (family_id) REFERENCES font_family(id)
);

CREATE TABLE admin_users (
    user_id TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    last_login TIMESTAMPTZ DEFAULT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    role TEXT NOT NULL DEFAULT 'admin',
    CONSTRAINT admin_users_role_check CHECK (role IN ('admin', 'super_admin'))
);

INSERT INTO demo_sentence (sid, content, language)
VALUES
    (1, 'legacy sentence one', 'en'),
    (42, 'legacy sentence forty two', 'en');

INSERT INTO font_family (
    id,
    name,
    weights,
    tags,
    authors,
    languages,
    demo_content_id,
    format,
    category
) VALUES
    (
        'legacy-null',
        'Legacy Null',
        NULL,
        NULL,
        NULL,
        '{"Han": 1}'::JSON,
        1,
        NULL,
        NULL
    ),
    (
        'legacy-data',
        'Legacy Data',
        ARRAY[400]::SMALLINT[],
        ARRAY['old']::TEXT[],
        ARRAY['author']::TEXT[],
        '{"Latin": 2}'::JSON,
        42,
        'otf',
        'serif'
    );

INSERT INTO version (bullet, start)
VALUES (175, TIMESTAMP '2024-01-02 03:04:05');

INSERT INTO static_fonts (char, pack, families, use_count)
VALUES
    ('A', 7, NULL, 3),
    ('BC', 8, ARRAY['legacy-data']::TEXT[], 4);

INSERT INTO dynamic_fonts (family_id, weight, hash)
VALUES ('legacy-data', 400, repeat('a', 40));

INSERT INTO r2_files (prefix, file_name)
VALUES ('legacy', 'font.otf');

INSERT INTO usage_log (family_id, text)
VALUES ('legacy-data', 'kept usage');

INSERT INTO admin_users (user_id, password_hash, role)
VALUES ('legacy-admin', 'legacy-password-hash', 'super_admin');

INSERT INTO schemaversion (version, name, md5, run_at)
VALUES
    (0, NULL, NULL, NULL),
    (1, 'init', 'legacy-md5-001', TIMESTAMPTZ '2026-02-20 15:42:19+00'),
    (2, 'insert-seed-data', 'legacy-md5-002', TIMESTAMPTZ '2026-02-20 15:42:20+00'),
    (3, 'create-admin-users', 'legacy-md5-003', TIMESTAMPTZ '2026-02-21 08:00:00+00'),
    (4, 'add-admin-roles', 'legacy-md5-004', TIMESTAMPTZ '2026-02-22 08:00:00+00');

-- The old seed used explicit IDs, which did not advance either sequence.
SELECT setval('demo_sentence_sid_seq', 1, FALSE);
SELECT setval('custom_bullet_seq', 100, FALSE);
