-- name: GetFontFamily :one
SELECT
    id,
    name,
    COALESCE(name_zh, '')::text AS name_zh,
    COALESCE(name_en, '')::text AS name_en,
    COALESCE(weights, ARRAY[]::SMALLINT[])::SMALLINT[] AS weights,
    COALESCE(license, '')::text AS license,
    COALESCE(version, '')::text AS version,
    COALESCE(description, '')::text AS description,
    COALESCE(category, '')::text AS category,
    COALESCE(family, '')::text AS family,
    COALESCE(tags, ARRAY[]::TEXT[])::TEXT[] AS tags,
    COALESCE(repo_url, '')::text AS repo_url,
    COALESCE(authors, ARRAY[]::TEXT[])::TEXT[] AS authors,
    COALESCE(format, 'ttf')::text AS format,
    COALESCE(demo_content_id, 0)::int AS demo_content_id
FROM font_family
WHERE id = $1;

-- name: ListFontFamilies :many
SELECT
    id,
    name,
    COALESCE(name_zh, '')::text AS name_zh,
    COALESCE(name_en, '')::text AS name_en,
    COALESCE(weights, ARRAY[]::SMALLINT[])::SMALLINT[] AS weights,
    COALESCE(license, '')::text AS license,
    COALESCE(version, '')::text AS version,
    COALESCE(description, '')::text AS description,
    COALESCE(category, '')::text AS category,
    COALESCE(family, '')::text AS family,
    COALESCE(tags, ARRAY[]::TEXT[])::TEXT[] AS tags,
    COALESCE(repo_url, '')::text AS repo_url,
    COALESCE(authors, ARRAY[]::TEXT[])::TEXT[] AS authors,
    COALESCE(format, 'ttf')::text AS format,
    COALESCE(demo_content_id, 0)::int AS demo_content_id
FROM font_family
WHERE $1::text = ''
   OR id ILIKE '%' || $1 || '%'
   OR name ILIKE '%' || $1 || '%'
   OR COALESCE(name_zh, '') ILIKE '%' || $1 || '%'
   OR COALESCE(name_en, '') ILIKE '%' || $1 || '%'
   OR EXISTS (
        SELECT 1
        FROM unnest(COALESCE(authors, ARRAY[]::TEXT[])) AS author
        WHERE author ILIKE '%' || $1 || '%'
   )
ORDER BY id;

-- name: GetFontSource :one
SELECT
    family_id,
    weight,
    format,
    object_key,
    COALESCE(checksum_sha256, '')::text AS checksum_sha256,
    size_bytes,
    COALESCE(source_version, '')::text AS source_version
FROM font_sources
WHERE family_id = $1 AND weight = $2;

-- name: GetFontArtifact :one
SELECT
    artifact_key,
    kind,
    status,
    family_id,
    weight,
    COALESCE(version, 0)::int AS version,
    COALESCE(pack, '')::text AS pack,
    COALESCE(word_hash, '')::text AS word_hash,
    COALESCE(normalized_word_set, '')::text AS normalized_word_set,
    COALESCE(source_checksum_sha256, '')::text AS source_checksum_sha256,
    builder_version,
    object_key,
    content_type,
    size_bytes,
    COALESCE(etag, '')::text AS etag,
    COALESCE(checksum_sha256, '')::text AS checksum_sha256
FROM font_artifacts
WHERE artifact_key = $1;

-- name: CreateFontArtifact :exec
INSERT INTO font_artifacts (
    artifact_key,
    kind,
    status,
    family_id,
    weight,
    version,
    pack,
    word_hash,
    normalized_word_set,
    source_checksum_sha256,
    builder_version,
    object_key,
    content_type
) VALUES (
    $1, $2, $3, $4, $5, NULLIF($6, 0), NULLIF($7, ''), NULLIF($8, ''),
    NULLIF($9, ''), NULLIF($10, ''), $11, $12, $13
)
ON CONFLICT (artifact_key) DO NOTHING;

-- name: MarkFontArtifactReady :execrows
UPDATE font_artifacts AS artifact
SET
    status = 'ready',
    size_bytes = $3,
    etag = NULLIF($4, ''),
    checksum_sha256 = NULLIF($5, ''),
    error = NULL,
    last_verified_at = now(),
    updated_at = now()
WHERE artifact.artifact_key = $1
  AND EXISTS (
      SELECT 1
      FROM font_build_jobs AS job
      WHERE job.artifact_key = artifact.artifact_key
        AND job.status = 'running'
        AND job.locked_by = $2
        AND job.lease_until >= now()
  );

-- name: MarkFontArtifactMissing :exec
UPDATE font_artifacts
SET status = 'missing', error = $2, updated_at = now()
WHERE artifact_key = $1;

-- name: MarkFontArtifactFailed :execrows
UPDATE font_artifacts AS artifact
SET status = 'failed', error = $3, updated_at = now()
WHERE artifact.artifact_key = $1
  AND EXISTS (
      SELECT 1
      FROM font_build_jobs AS job
      WHERE job.artifact_key = artifact.artifact_key
        AND job.status = 'running'
        AND job.locked_by = $2
        AND job.lease_until >= now()
  );

-- name: GetCurrentStaticVersion :one
SELECT bullet
FROM version
ORDER BY start DESC, bullet DESC
LIMIT 1;

-- name: FindStaticPacks :many
SELECT DISTINCT pack
FROM static_fonts
WHERE $1::text = ANY(families)
  AND char = ANY($2::text[])
ORDER BY pack;

-- name: GetStaticPackCharacters :one
SELECT COALESCE(string_agg(char, '' ORDER BY char), '')::text AS characters
FROM static_fonts
WHERE pack = $1 AND $2::text = ANY(families);

-- name: AcquireFontBuildJob :one
WITH leased AS (
    INSERT INTO font_build_jobs (
        artifact_key, status, attempts, locked_by, lease_until, started_at, updated_at
    ) VALUES (
        $1, 'running', 1, $2, now() + ($3::bigint * interval '1 millisecond'), now(), now()
    )
    ON CONFLICT (artifact_key) DO UPDATE
    SET
        status = 'running',
        attempts = font_build_jobs.attempts + 1,
        locked_by = EXCLUDED.locked_by,
        lease_until = now() + ($3::bigint * interval '1 millisecond'),
        error = NULL,
        started_at = now(),
        completed_at = NULL,
        updated_at = now()
    WHERE font_build_jobs.status IN ('pending', 'failed')
       OR (font_build_jobs.status = 'running' AND font_build_jobs.lease_until < now())
       OR (
            font_build_jobs.status = 'ready'
            AND EXISTS (
                SELECT 1 FROM font_artifacts AS current_artifact
                WHERE current_artifact.artifact_key = font_build_jobs.artifact_key
                  AND current_artifact.status IN ('missing', 'failed', 'stale')
            )
       )
    RETURNING artifact_key
), marked AS (
    UPDATE font_artifacts
    SET status = 'running', error = NULL, updated_at = now()
    WHERE artifact_key = $1 AND EXISTS (SELECT 1 FROM leased)
    RETURNING artifact_key
)
SELECT EXISTS(SELECT 1 FROM marked)::boolean AS acquired;

-- name: CompleteFontBuildJob :execrows
UPDATE font_build_jobs
SET
    status = 'ready',
    locked_by = NULL,
    lease_until = NULL,
    error = NULL,
    completed_at = now(),
    updated_at = now()
WHERE artifact_key = $1
  AND status = 'running'
  AND locked_by = $2
  AND lease_until >= now();

-- name: FailFontBuildJob :execrows
UPDATE font_build_jobs
SET
    status = 'failed',
    locked_by = NULL,
    lease_until = NULL,
    error = $3,
    completed_at = now(),
    updated_at = now()
WHERE artifact_key = $1 AND status = 'running' AND locked_by = $2;
