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
WHERE id > sqlc.arg(after_id)::text
  AND (
       sqlc.arg(search)::text = ''
       OR id ILIKE '%' || sqlc.arg(search) || '%'
       OR name ILIKE '%' || sqlc.arg(search) || '%'
       OR COALESCE(name_zh, '') ILIKE '%' || sqlc.arg(search) || '%'
       OR COALESCE(name_en, '') ILIKE '%' || sqlc.arg(search) || '%'
       OR EXISTS (
        SELECT 1
        FROM unnest(COALESCE(authors, ARRAY[]::TEXT[])) AS author
        WHERE author ILIKE '%' || sqlc.arg(search) || '%'
       )
   )
ORDER BY id
LIMIT sqlc.arg(page_limit)::int;

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
WITH matching_artifact AS (
SELECT
    active_artifact.artifact_key,
    active_artifact.kind,
    active_artifact.status,
    active_artifact.family_id,
    active_artifact.weight,
    COALESCE(active_artifact.version, 0)::int AS version,
    COALESCE(active_artifact.pack, '')::text AS pack,
    COALESCE(active_artifact.word_hash, '')::text AS word_hash,
    COALESCE(active_artifact.normalized_word_set, '')::text AS normalized_word_set,
    COALESCE(active_artifact.source_checksum_sha256, '')::text AS source_checksum_sha256,
    active_artifact.builder_version,
    active_artifact.artifact_protocol_version,
    active_artifact.object_key,
    COALESCE(active_artifact.object_version_id, '')::text AS object_version_id,
    active_artifact.content_type,
    active_artifact.size_bytes,
    COALESCE(active_artifact.etag, '')::text AS etag,
    COALESCE(active_artifact.checksum_sha256, '')::text AS checksum_sha256,
    active_artifact.generation,
    COALESCE(active_artifact.failure_code, '')::text AS failure_code,
    0::smallint AS cache_rank
FROM font_artifacts AS active_artifact
WHERE active_artifact.artifact_key = $1
UNION ALL
SELECT
    terminal_failure.artifact_key,
    terminal_failure.kind,
    'failed'::text AS status,
    terminal_failure.family_id,
    terminal_failure.weight,
    COALESCE(terminal_failure.version, 0)::int AS version,
    COALESCE(terminal_failure.pack, '')::text AS pack,
    COALESCE(terminal_failure.word_hash, '')::text AS word_hash,
    COALESCE(terminal_failure.normalized_word_set, '')::text AS normalized_word_set,
    COALESCE(terminal_failure.source_checksum_sha256, '')::text AS source_checksum_sha256,
    terminal_failure.builder_version,
    terminal_failure.artifact_protocol_version,
    ''::text AS object_key,
    ''::text AS object_version_id,
    terminal_failure.content_type,
    0::bigint AS size_bytes,
    ''::text AS etag,
    ''::text AS checksum_sha256,
    0::bigint AS generation,
    terminal_failure.failure_code,
    1::smallint AS cache_rank
FROM font_terminal_failures AS terminal_failure
WHERE terminal_failure.artifact_key = $1
)
SELECT
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
    artifact_protocol_version,
    object_key,
    object_version_id,
    content_type,
    size_bytes,
    etag,
    checksum_sha256,
    generation,
    failure_code
FROM matching_artifact
ORDER BY cache_rank
LIMIT 1;

-- name: LockFontArtifactAdmission :exec
SELECT artifact_count
FROM font_artifact_quota
WHERE singleton
FOR UPDATE;

-- name: CreateFontArtifact :one
WITH quota AS MATERIALIZED (
    SELECT artifact_count, accounted_bytes
    FROM font_artifact_quota
    WHERE singleton
    FOR UPDATE
), locked_job AS MATERIALIZED (
    SELECT job.artifact_key
    FROM font_build_jobs AS job
    CROSS JOIN quota
    WHERE job.artifact_key = sqlc.arg(artifact_key)
      AND quota.artifact_count >= 0
    FOR UPDATE OF job
), existing AS MATERIALIZED (
    SELECT
        current_artifact.artifact_key,
        current_artifact.status,
        current_artifact.size_bytes,
        current_artifact.reservation_bytes,
        current_artifact.quota_bytes,
        current_artifact.failure_code,
        (
            current_artifact.kind = sqlc.arg(kind)
            AND current_artifact.family_id = sqlc.arg(family_id)
            AND current_artifact.weight = sqlc.arg(weight)
            AND current_artifact.version IS NOT DISTINCT FROM NULLIF(sqlc.arg(version), 0)
            AND current_artifact.pack IS NOT DISTINCT FROM NULLIF(sqlc.arg(pack), '')
            AND current_artifact.word_hash IS NOT DISTINCT FROM NULLIF(sqlc.arg(word_hash), '')
            AND current_artifact.normalized_word_set IS NOT DISTINCT FROM NULLIF(sqlc.arg(normalized_word_set), '')
            AND current_artifact.source_checksum_sha256 IS NOT DISTINCT FROM NULLIF(sqlc.arg(source_checksum_sha256), '')
            AND current_artifact.builder_version = sqlc.arg(builder_version)
            AND current_artifact.artifact_protocol_version = sqlc.arg(artifact_protocol_version)
            AND current_artifact.content_type = sqlc.arg(content_type)
        )::boolean AS compatible
    FROM font_artifacts AS current_artifact
    CROSS JOIN (
        SELECT COUNT(*)::bigint AS locked_count
        FROM locked_job
    ) AS job_lock
    WHERE current_artifact.artifact_key = sqlc.arg(artifact_key)
      AND job_lock.locked_count >= 0
    FOR UPDATE OF current_artifact
), existing_plan AS MATERIALIZED (
    SELECT
        existing.*,
        CASE
            WHEN existing.status = 'stale'
              OR existing.status IN ('pending', 'running', 'missing')
              OR (existing.status = 'failed' AND existing.failure_code IS NULL)
                THEN GREATEST(existing.reservation_bytes, sqlc.arg(artifact_reservation)::bigint)
            ELSE existing.reservation_bytes
        END::bigint AS target_reservation,
        CASE
            WHEN existing.status = 'stale' THEN
                GREATEST(existing.reservation_bytes, sqlc.arg(artifact_reservation)::bigint)
                    - existing.quota_bytes
            WHEN existing.status IN ('pending', 'running', 'missing') THEN
                GREATEST(existing.reservation_bytes, sqlc.arg(artifact_reservation)::bigint)
                    - existing.quota_bytes
            ELSE 0
        END::bigint AS accounted_increase
    FROM existing
), cached_failure AS MATERIALIZED (
    SELECT
        terminal_failure.artifact_key,
        (
            terminal_failure.kind = sqlc.arg(kind)
            AND terminal_failure.family_id = sqlc.arg(family_id)
            AND terminal_failure.weight = sqlc.arg(weight)
            AND terminal_failure.version IS NOT DISTINCT FROM NULLIF(sqlc.arg(version), 0)
            AND terminal_failure.pack IS NOT DISTINCT FROM NULLIF(sqlc.arg(pack), '')
            AND terminal_failure.word_hash IS NOT DISTINCT FROM NULLIF(sqlc.arg(word_hash), '')
            AND terminal_failure.normalized_word_set IS NOT DISTINCT FROM NULLIF(sqlc.arg(normalized_word_set), '')
            AND terminal_failure.source_checksum_sha256 IS NOT DISTINCT FROM NULLIF(sqlc.arg(source_checksum_sha256), '')
            AND terminal_failure.builder_version = sqlc.arg(builder_version)
            AND terminal_failure.artifact_protocol_version = sqlc.arg(artifact_protocol_version)
            AND terminal_failure.content_type = sqlc.arg(content_type)
        )::boolean AS compatible
    FROM font_terminal_failures AS terminal_failure
    CROSS JOIN (
        SELECT COUNT(*)::bigint AS locked_count
        FROM existing
    ) AS artifact_lock
    WHERE terminal_failure.artifact_key = sqlc.arg(artifact_key)
      AND artifact_lock.locked_count >= 0
    FOR UPDATE OF terminal_failure
), admission AS MATERIALIZED (
    SELECT CASE
        WHEN existing_plan.artifact_key IS NOT NULL AND cached_failure.artifact_key IS NOT NULL THEN 'conflict'
        WHEN existing_plan.artifact_key IS NOT NULL AND NOT existing_plan.compatible THEN 'conflict'
        WHEN cached_failure.artifact_key IS NOT NULL AND NOT cached_failure.compatible THEN 'conflict'
        WHEN cached_failure.artifact_key IS NOT NULL THEN 'terminal'
        WHEN existing_plan.artifact_key IS NOT NULL
             AND existing_plan.accounted_increase > 0
             AND quota.accounted_bytes + existing_plan.accounted_increase >
                 sqlc.arg(max_accounted_bytes)::bigint THEN 'capacity'
        WHEN existing_plan.artifact_key IS NOT NULL THEN 'existing'
        WHEN quota.artifact_count >= sqlc.arg(max_artifacts)::bigint THEN 'capacity'
        WHEN quota.accounted_bytes + sqlc.arg(artifact_reservation)::bigint >
             sqlc.arg(max_accounted_bytes)::bigint THEN 'capacity'
        ELSE 'new'
    END::text AS outcome
    FROM quota
    LEFT JOIN existing_plan ON TRUE
    LEFT JOIN cached_failure ON TRUE
), inserted AS (
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
    artifact_protocol_version,
    object_key,
    content_type,
    reservation_bytes
) SELECT
    sqlc.arg(artifact_key), sqlc.arg(kind), sqlc.arg(status), sqlc.arg(family_id), sqlc.arg(weight),
    NULLIF(sqlc.arg(version), 0), NULLIF(sqlc.arg(pack), ''), NULLIF(sqlc.arg(word_hash), ''),
    NULLIF(sqlc.arg(normalized_word_set), ''), NULLIF(sqlc.arg(source_checksum_sha256), ''),
    sqlc.arg(builder_version), sqlc.arg(artifact_protocol_version), sqlc.arg(object_key), sqlc.arg(content_type),
    sqlc.arg(artifact_reservation)
FROM admission
WHERE admission.outcome = 'new'
ON CONFLICT DO NOTHING
RETURNING artifact_key
), updated AS (
UPDATE font_artifacts AS artifact
SET
    status = CASE WHEN artifact.status = 'stale' THEN 'pending' ELSE artifact.status END,
    failure_code = CASE WHEN artifact.status = 'stale' THEN NULL ELSE artifact.failure_code END,
    retired_at = CASE WHEN artifact.status = 'stale' THEN NULL ELSE artifact.retired_at END,
    reservation_bytes = existing_plan.target_reservation,
    updated_at = CASE WHEN artifact.status = 'stale' THEN now() ELSE artifact.updated_at END
FROM existing_plan
JOIN admission ON admission.outcome = 'existing'
WHERE artifact.artifact_key = existing_plan.artifact_key
RETURNING artifact.artifact_key
), reset_reactivated_job AS (
    UPDATE font_build_jobs AS job
    SET
        status = 'pending',
        attempts = 0,
        locked_by = NULL,
        lease_until = NULL,
        next_attempt_at = NULL,
        retryable = TRUE,
        failure_code = NULL,
        error = NULL,
        started_at = NULL,
        completed_at = NULL,
        updated_at = now()
    FROM existing_plan
    JOIN updated USING (artifact_key)
    WHERE existing_plan.status = 'stale'
      AND job.artifact_key = existing_plan.artifact_key
    RETURNING job.artifact_key
)
SELECT CASE
    WHEN admission.outcome = 'capacity' THEN 'capacity'
    WHEN admission.outcome = 'conflict' THEN 'conflict'
    WHEN admission.outcome = 'terminal' THEN 'terminal'
    WHEN admission.outcome = 'new' AND inserted.artifact_key IS NULL THEN 'conflict'
    WHEN admission.outcome = 'existing' AND updated.artifact_key IS NULL THEN 'conflict'
    ELSE 'admitted'
END::text AS result
FROM admission
LEFT JOIN inserted ON TRUE
LEFT JOIN updated ON TRUE;

-- name: MarkFontArtifactReady :one
WITH quota AS MATERIALIZED (
    SELECT artifact_count
    FROM font_artifact_quota
    WHERE singleton
    FOR UPDATE
), claimed_job AS MATERIALIZED (
    SELECT job.artifact_key
    FROM font_build_jobs AS job
    CROSS JOIN quota
    WHERE job.artifact_key = sqlc.arg(artifact_key)
      AND job.status = 'running'
      AND job.locked_by = sqlc.arg(locked_by)
      AND job.fence = sqlc.arg(fence)
      AND job.lease_until >= now()
      AND quota.artifact_count >= 0
    FOR UPDATE OF job
), candidate AS MATERIALIZED (
    SELECT artifact.artifact_key, artifact.reservation_bytes
    FROM font_artifacts AS artifact
    JOIN claimed_job USING (artifact_key)
    WHERE artifact.artifact_key = sqlc.arg(artifact_key)
      AND artifact.status = 'running'
    FOR UPDATE OF artifact
), completed_job AS (
    UPDATE font_build_jobs AS job
    SET
        status = 'ready',
        locked_by = NULL,
        lease_until = NULL,
        next_attempt_at = NULL,
        retryable = TRUE,
        failure_code = NULL,
        error = NULL,
        completed_at = now(),
        updated_at = now()
    FROM candidate
    WHERE job.artifact_key = candidate.artifact_key
      AND sqlc.arg(size_bytes)::bigint <= candidate.reservation_bytes
    RETURNING job.artifact_key
), published AS (
    UPDATE font_artifacts AS artifact
    SET
        status = 'ready',
        object_key = sqlc.arg(object_key),
        object_version_id = NULLIF(sqlc.arg(object_version_id)::text, ''),
        size_bytes = sqlc.arg(size_bytes),
        etag = NULLIF(sqlc.arg(etag)::text, ''),
        checksum_sha256 = NULLIF(sqlc.arg(checksum_sha256)::text, ''),
        generation = sqlc.arg(fence),
        failure_code = NULL,
        retired_at = NULL,
        error = NULL,
        last_verified_at = now(),
        updated_at = now()
    FROM completed_job
    WHERE artifact.artifact_key = completed_job.artifact_key
    RETURNING artifact.artifact_key
)
SELECT CASE
    WHEN EXISTS (SELECT 1 FROM published) THEN 'ready'
    WHEN EXISTS (
        SELECT 1 FROM candidate
        WHERE sqlc.arg(size_bytes)::bigint > candidate.reservation_bytes
    ) THEN 'capacity'
    ELSE 'not_ready'
END::text AS result;

-- name: MarkFontArtifactMissing :one
WITH quota AS MATERIALIZED (
    SELECT artifact_count, accounted_bytes
    FROM font_artifact_quota
    WHERE singleton
    FOR UPDATE
), candidate AS MATERIALIZED (
    SELECT artifact.artifact_key, artifact.size_bytes, artifact.reservation_bytes
    FROM font_artifacts AS artifact
    CROSS JOIN quota
    WHERE artifact.artifact_key = sqlc.arg(artifact_key)
      AND artifact.status = 'ready'
      AND artifact.object_key = sqlc.arg(object_key)
      AND artifact.generation = sqlc.arg(generation)
      AND quota.artifact_count >= 0
    FOR UPDATE OF artifact
), decision AS MATERIALIZED (
    SELECT CASE
        WHEN candidate.artifact_key IS NULL THEN 'stale'
        WHEN quota.accounted_bytes
             + candidate.reservation_bytes - candidate.size_bytes >
             sqlc.arg(max_accounted_bytes)::bigint THEN 'capacity'
        ELSE 'marked'
    END::text AS outcome
    FROM quota
    LEFT JOIN candidate ON TRUE
), marked AS (
    UPDATE font_artifacts AS artifact
    SET status = 'missing', failure_code = NULL, error = sqlc.arg(error), updated_at = now()
    FROM candidate
    JOIN decision ON decision.outcome = 'marked'
    WHERE artifact.artifact_key = candidate.artifact_key
    RETURNING artifact.artifact_key
)
SELECT CASE
    WHEN decision.outcome = 'marked' AND marked.artifact_key IS NULL THEN 'stale'
    ELSE decision.outcome
END::text AS result
FROM decision
LEFT JOIN marked ON TRUE;

-- name: TouchFontArtifact :one
WITH touched AS (
    UPDATE font_artifacts AS artifact
    SET updated_at = now(), last_verified_at = now()
    WHERE artifact.artifact_key = sqlc.arg(artifact_key)
      AND artifact.status = 'ready'
      AND artifact.object_key = sqlc.arg(object_key)
      AND artifact.generation = sqlc.arg(generation)
      AND artifact.updated_at < now() - (sqlc.arg(minimum_interval_ms)::bigint * interval '1 millisecond')
    RETURNING artifact.artifact_key
)
SELECT EXISTS (
    SELECT 1
    FROM font_artifacts AS current_artifact
    WHERE current_artifact.artifact_key = sqlc.arg(artifact_key)
      AND current_artifact.status = 'ready'
      AND current_artifact.object_key = sqlc.arg(object_key)
      AND current_artifact.generation = sqlc.arg(generation)
)::boolean AS current;

-- name: GetStaticPackSnapshot :many
WITH current_version AS MATERIALIZED (
    SELECT bullet AS version
    FROM version
    ORDER BY start DESC, bullet DESC
    LIMIT 1
), requested_characters AS MATERIALIZED (
    SELECT DISTINCT requested.character
    FROM unnest($2::text[]) AS requested(character)
), matched_characters AS MATERIALIZED (
    SELECT DISTINCT fonts.char, fonts.pack
    FROM static_fonts AS fonts
    JOIN requested_characters AS requested ON requested.character = fonts.char
    WHERE $1::text = ANY(fonts.families)
), requested_packs AS MATERIALIZED (
    SELECT DISTINCT pack
    FROM matched_characters
), pack_contents AS (
    SELECT
        fonts.pack,
        COALESCE(string_agg(fonts.char, '' ORDER BY fonts.char), '')::text AS characters
    FROM static_fonts AS fonts
    JOIN requested_packs USING (pack)
    WHERE $1::text = ANY(fonts.families)
    GROUP BY fonts.pack
)
SELECT
    current_version.version,
    pack_contents.pack,
    pack_contents.characters,
    NOT EXISTS (
        SELECT 1
        FROM requested_characters AS requested
        WHERE NOT EXISTS (
            SELECT 1
            FROM matched_characters AS matched
            WHERE matched.char = requested.character
        )
    )::boolean AS coverage_complete
FROM current_version
CROSS JOIN pack_contents
ORDER BY pack_contents.pack;

-- name: AcquireFontBuildJob :one
WITH quota AS MATERIALIZED (
    SELECT artifact_count, accounted_bytes
    FROM font_artifact_quota
    WHERE singleton
    FOR UPDATE
), locked_job AS MATERIALIZED (
    SELECT job.artifact_key
    FROM font_build_jobs AS job
    CROSS JOIN quota
    WHERE job.artifact_key = sqlc.arg(artifact_key)
      AND quota.artifact_count >= 0
    FOR UPDATE OF job
), eligible_artifact AS MATERIALIZED (
    SELECT artifact.artifact_key, artifact.reservation_bytes, artifact.quota_bytes
    FROM font_artifacts AS artifact
    CROSS JOIN (
        SELECT COUNT(*)::bigint AS locked_count
        FROM locked_job
    ) AS job_lock
    WHERE artifact.artifact_key = sqlc.arg(artifact_key)
      AND job_lock.locked_count >= 0
      AND (
          artifact.status IN ('pending', 'running', 'missing')
          OR (artifact.status = 'failed' AND artifact.failure_code IS NULL)
    )
    FOR UPDATE OF artifact
), admitted_artifact AS MATERIALIZED (
    SELECT eligible_artifact.artifact_key
    FROM eligible_artifact
    CROSS JOIN quota
    WHERE quota.artifact_count <= sqlc.arg(max_artifacts)::bigint
      AND quota.accounted_bytes
            + GREATEST(eligible_artifact.reservation_bytes - eligible_artifact.quota_bytes, 0) <=
          sqlc.arg(max_accounted_bytes)::bigint
), leased AS (
    INSERT INTO font_build_jobs (
        artifact_key, status, attempts, fence, locked_by, lease_until, retryable, failure_code, started_at, updated_at
    ) SELECT
        admitted_artifact.artifact_key, 'running', 1, nextval('font_artifact_fence_seq'), sqlc.arg(locked_by),
        now() + (sqlc.arg(lease_duration_ms)::bigint * interval '1 millisecond'), TRUE, NULL, now(), now()
    FROM admitted_artifact
    ON CONFLICT (artifact_key) DO UPDATE
    SET
        status = 'running',
        attempts = font_build_jobs.attempts + 1,
        fence = nextval('font_artifact_fence_seq'),
        locked_by = EXCLUDED.locked_by,
        lease_until = now() + (sqlc.arg(lease_duration_ms)::bigint * interval '1 millisecond'),
        next_attempt_at = NULL,
        retryable = TRUE,
        failure_code = NULL,
        error = NULL,
        started_at = now(),
        completed_at = NULL,
        updated_at = now()
    WHERE EXISTS (SELECT 1 FROM admitted_artifact)
      AND (
          font_build_jobs.status = 'pending'
          OR (
            font_build_jobs.status = 'failed'
            AND font_build_jobs.retryable
            AND COALESCE(font_build_jobs.next_attempt_at, '-infinity'::timestamptz) <= now()
          )
          OR (font_build_jobs.status = 'running' AND font_build_jobs.lease_until < now())
          OR (
            font_build_jobs.status = 'ready'
          )
      )
    RETURNING artifact_key, fence
), marked AS (
    UPDATE font_artifacts AS artifact
    SET status = 'running', failure_code = NULL, retired_at = NULL, error = NULL, updated_at = now()
    FROM leased
    JOIN admitted_artifact USING (artifact_key)
    WHERE artifact.artifact_key = leased.artifact_key
    RETURNING leased.fence
)
SELECT fence FROM marked;

-- name: GetFontBuildRetryAfterSeconds :one
SELECT LEAST(
    300::bigint,
    GREATEST(
        1::bigint,
        COALESCE(CEIL(EXTRACT(EPOCH FROM (
            CASE
                WHEN status = 'running' THEN lease_until
                WHEN status = 'failed' THEN next_attempt_at
                ELSE NULL
            END - now()
        )))::bigint, 1::bigint)
    )
)::bigint AS retry_after_seconds
FROM font_build_jobs
WHERE artifact_key = $1;

-- name: FailFontBuildJob :execrows
WITH quota AS MATERIALIZED (
    SELECT artifact_count
    FROM font_artifact_quota
    WHERE singleton
    FOR UPDATE
), failed_job AS (
    UPDATE font_build_jobs AS job
    SET
        status = 'failed',
        locked_by = NULL,
        lease_until = NULL,
        next_attempt_at = now() + (
            LEAST(300::bigint, 1::bigint << LEAST(GREATEST(job.attempts - 1, 0), 9)::integer)
            * interval '1 second'
        ),
        retryable = TRUE,
        failure_code = NULL,
        error = sqlc.arg(error),
        completed_at = now(),
        updated_at = now()
    FROM quota
    WHERE job.artifact_key = sqlc.arg(artifact_key)
      AND job.status = 'running'
      AND job.locked_by = sqlc.arg(locked_by)
      AND job.fence = sqlc.arg(fence)
      AND job.lease_until >= now()
      AND quota.artifact_count >= 0
    RETURNING job.artifact_key
)
UPDATE font_artifacts AS artifact
SET status = 'failed', failure_code = NULL, error = sqlc.arg(error), updated_at = now()
WHERE artifact.artifact_key = sqlc.arg(artifact_key)
  AND EXISTS (SELECT 1 FROM failed_job);

-- name: FailFontBuildJobTerminal :execrows
WITH quota AS MATERIALIZED (
    SELECT artifact_count
    FROM font_artifact_quota
    WHERE singleton
    FOR UPDATE
), claimed_job AS MATERIALIZED (
    SELECT job.artifact_key
    FROM font_build_jobs AS job
    CROSS JOIN quota
    WHERE job.artifact_key = sqlc.arg(artifact_key)
      AND job.status = 'running'
      AND job.locked_by = sqlc.arg(locked_by)
      AND job.fence = sqlc.arg(fence)
      AND job.lease_until >= now()
      AND quota.artifact_count >= 0
    FOR UPDATE OF job
), candidate AS MATERIALIZED (
    SELECT
        artifact.artifact_key,
        artifact.kind,
        artifact.family_id,
        artifact.weight,
        artifact.version,
        artifact.pack,
        artifact.word_hash,
        artifact.normalized_word_set,
        artifact.source_checksum_sha256,
        artifact.builder_version,
        artifact.artifact_protocol_version,
        artifact.content_type
    FROM font_artifacts AS artifact
    JOIN claimed_job USING (artifact_key)
    WHERE artifact.artifact_key = sqlc.arg(artifact_key)
      AND artifact.status = 'running'
    FOR UPDATE OF artifact
), eviction_candidates AS MATERIALIZED (
    SELECT terminal_failure.artifact_key
    FROM font_terminal_failures AS terminal_failure
    WHERE EXISTS (SELECT 1 FROM candidate)
    ORDER BY terminal_failure.cached_at, terminal_failure.artifact_key
    LIMIT (
        SELECT GREATEST(
            COUNT(*) - sqlc.arg(max_terminal_failures)::bigint + 1,
            0
        )::bigint
        FROM font_terminal_failures
    )
    FOR UPDATE OF terminal_failure
), evicted AS (
    DELETE FROM font_terminal_failures AS terminal_failure
    USING eviction_candidates
    WHERE terminal_failure.artifact_key = eviction_candidates.artifact_key
    RETURNING terminal_failure.artifact_key
), cached AS (
    INSERT INTO font_terminal_failures (
        artifact_key,
        kind,
        family_id,
        weight,
        version,
        pack,
        word_hash,
        normalized_word_set,
        source_checksum_sha256,
        builder_version,
        artifact_protocol_version,
        content_type,
        failure_code,
        error,
        cached_at
    )
    SELECT
        candidate.artifact_key,
        candidate.kind,
        candidate.family_id,
        candidate.weight,
        candidate.version,
        candidate.pack,
        candidate.word_hash,
        candidate.normalized_word_set,
        candidate.source_checksum_sha256,
        candidate.builder_version,
        candidate.artifact_protocol_version,
        candidate.content_type,
        sqlc.arg(failure_code),
        sqlc.arg(error),
        now()
    FROM candidate
    CROSS JOIN (
        SELECT COUNT(*)::bigint AS evicted_count
        FROM evicted
    ) AS eviction_barrier
    WHERE eviction_barrier.evicted_count >= 0
    ON CONFLICT (artifact_key) DO UPDATE
    SET
        kind = EXCLUDED.kind,
        family_id = EXCLUDED.family_id,
        weight = EXCLUDED.weight,
        version = EXCLUDED.version,
        pack = EXCLUDED.pack,
        word_hash = EXCLUDED.word_hash,
        normalized_word_set = EXCLUDED.normalized_word_set,
        source_checksum_sha256 = EXCLUDED.source_checksum_sha256,
        builder_version = EXCLUDED.builder_version,
        artifact_protocol_version = EXCLUDED.artifact_protocol_version,
        content_type = EXCLUDED.content_type,
        failure_code = EXCLUDED.failure_code,
        error = EXCLUDED.error,
        cached_at = EXCLUDED.cached_at
    RETURNING artifact_key
)
DELETE FROM font_artifacts AS artifact
USING cached
WHERE artifact.artifact_key = cached.artifact_key;

-- name: RetireFontArtifacts :execrows
WITH quota AS MATERIALIZED (
    SELECT artifact_count
    FROM font_artifact_quota
    WHERE singleton
    FOR UPDATE
), locked_job_candidates AS MATERIALIZED (
    SELECT job.artifact_key, artifact.updated_at
    FROM font_build_jobs AS job
    JOIN font_artifacts AS artifact ON artifact.artifact_key = job.artifact_key
    CROSS JOIN quota
    WHERE quota.artifact_count >= 0
      AND (
          (
              artifact.status IN ('pending', 'ready', 'failed', 'missing')
              AND artifact.updated_at < sqlc.arg(inactive_before)::timestamptz
          )
          OR (
              artifact.status = 'running'
              AND artifact.updated_at < sqlc.arg(inactive_before)::timestamptz
              AND job.status = 'running'
              AND job.lease_until < now()
          )
          OR (artifact.status = 'stale' AND artifact.retired_at IS NULL)
      )
    ORDER BY artifact.updated_at, artifact.artifact_key
    LIMIT sqlc.arg(batch_size)::int
    FOR UPDATE OF job SKIP LOCKED
), no_job_candidates AS MATERIALIZED (
    SELECT artifact.artifact_key, artifact.updated_at
    FROM font_artifacts AS artifact
    CROSS JOIN quota
    WHERE quota.artifact_count >= 0
      AND (
            (
                artifact.status IN ('pending', 'ready', 'failed', 'missing')
                AND artifact.updated_at < sqlc.arg(inactive_before)::timestamptz
            )
            OR (artifact.status = 'stale' AND artifact.retired_at IS NULL)
        )
      AND NOT EXISTS (
          SELECT 1
          FROM font_build_jobs AS job
          WHERE job.artifact_key = artifact.artifact_key
      )
    ORDER BY artifact.updated_at, artifact.artifact_key
    LIMIT sqlc.arg(batch_size)::int
), selected_candidates AS MATERIALIZED (
    SELECT candidate_pool.artifact_key, candidate_pool.updated_at
    FROM (
        SELECT artifact_key, updated_at FROM locked_job_candidates
        UNION ALL
        SELECT artifact_key, updated_at FROM no_job_candidates
    ) AS candidate_pool
    ORDER BY candidate_pool.updated_at, candidate_pool.artifact_key
    LIMIT sqlc.arg(batch_size)::int
), candidates AS MATERIALIZED (
    SELECT artifact.artifact_key
    FROM font_artifacts AS artifact
    JOIN selected_candidates USING (artifact_key)
    WHERE (
            (
                artifact.status IN ('pending', 'ready', 'failed', 'missing')
                AND artifact.updated_at < sqlc.arg(inactive_before)::timestamptz
            )
            OR (
                artifact.status = 'running'
                AND artifact.updated_at < sqlc.arg(inactive_before)::timestamptz
                AND EXISTS (
                    SELECT 1
                    FROM locked_job_candidates AS locked_job
                    JOIN font_build_jobs AS job USING (artifact_key)
                    WHERE locked_job.artifact_key = artifact.artifact_key
                      AND job.status = 'running'
                      AND job.lease_until < now()
                )
            )
            OR (artifact.status = 'stale' AND artifact.retired_at IS NULL)
        )
      AND (
          EXISTS (
              SELECT 1 FROM locked_job_candidates AS locked_job
              WHERE locked_job.artifact_key = artifact.artifact_key
          )
          OR NOT EXISTS (
              SELECT 1 FROM font_build_jobs AS job
              WHERE job.artifact_key = artifact.artifact_key
          )
      )
    ORDER BY selected_candidates.updated_at, artifact.artifact_key
    FOR UPDATE OF artifact SKIP LOCKED
), retired_jobs AS (
    UPDATE font_build_jobs AS job
    SET
        status = 'failed',
        locked_by = NULL,
        lease_until = NULL,
        next_attempt_at = now(),
        retryable = TRUE,
        failure_code = NULL,
        error = 'expired build retired by cleanup',
        completed_at = now(),
        updated_at = now()
    FROM candidates
    JOIN locked_job_candidates USING (artifact_key)
    WHERE job.artifact_key = candidates.artifact_key
      AND job.status = 'running'
      AND job.lease_until < now()
    RETURNING job.artifact_key
)
UPDATE font_artifacts AS artifact
SET
    status = 'stale',
    retired_at = sqlc.arg(retired_at)::timestamptz,
    updated_at = sqlc.arg(retired_at)::timestamptz
FROM candidates
WHERE artifact.artifact_key = candidates.artifact_key
  AND (
        (
            artifact.status IN ('pending', 'ready', 'failed', 'missing')
            AND artifact.updated_at < sqlc.arg(inactive_before)::timestamptz
        )
        OR (
            artifact.status = 'running'
            AND artifact.updated_at < sqlc.arg(inactive_before)::timestamptz
            AND EXISTS (
                SELECT 1 FROM retired_jobs WHERE retired_jobs.artifact_key = artifact.artifact_key
            )
        )
        OR (artifact.status = 'stale' AND artifact.retired_at IS NULL)
    );

-- name: DeleteRetiredFontArtifacts :execrows
WITH quota AS MATERIALIZED (
    SELECT artifact_count
    FROM font_artifact_quota
    WHERE singleton
    FOR UPDATE
), locked_job_candidates AS MATERIALIZED (
    SELECT job.artifact_key, artifact.retired_at
    FROM font_build_jobs AS job
    JOIN font_artifacts AS artifact ON artifact.artifact_key = job.artifact_key
    CROSS JOIN quota
    WHERE artifact.status = 'stale'
      AND artifact.retired_at < sqlc.arg(retired_before)::timestamptz
      AND quota.artifact_count >= 0
    ORDER BY artifact.retired_at, artifact.artifact_key
    LIMIT sqlc.arg(batch_size)::int
    FOR UPDATE OF job SKIP LOCKED
), no_job_candidates AS MATERIALIZED (
    SELECT artifact.artifact_key, artifact.retired_at
    FROM font_artifacts AS artifact
    CROSS JOIN quota
    WHERE quota.artifact_count >= 0
      AND artifact.status = 'stale'
      AND artifact.retired_at < sqlc.arg(retired_before)::timestamptz
      AND NOT EXISTS (
          SELECT 1
          FROM font_build_jobs AS job
          WHERE job.artifact_key = artifact.artifact_key
      )
    ORDER BY artifact.retired_at, artifact.artifact_key
    LIMIT sqlc.arg(batch_size)::int
), selected_candidates AS MATERIALIZED (
    SELECT candidate_pool.artifact_key, candidate_pool.retired_at
    FROM (
        SELECT artifact_key, retired_at FROM locked_job_candidates
        UNION ALL
        SELECT artifact_key, retired_at FROM no_job_candidates
    ) AS candidate_pool
    ORDER BY candidate_pool.retired_at, candidate_pool.artifact_key
    LIMIT sqlc.arg(batch_size)::int
), candidates AS MATERIALIZED (
    SELECT artifact.artifact_key
    FROM font_artifacts AS artifact
    JOIN selected_candidates USING (artifact_key)
    WHERE artifact.status = 'stale'
      AND artifact.retired_at < sqlc.arg(retired_before)::timestamptz
      AND (
          EXISTS (
              SELECT 1 FROM locked_job_candidates AS locked_job
              WHERE locked_job.artifact_key = artifact.artifact_key
          )
          OR NOT EXISTS (
              SELECT 1 FROM font_build_jobs AS job
              WHERE job.artifact_key = artifact.artifact_key
          )
      )
    ORDER BY selected_candidates.retired_at, artifact.artifact_key
    FOR UPDATE OF artifact SKIP LOCKED
)
DELETE FROM font_artifacts AS artifact
USING candidates
WHERE artifact.artifact_key = candidates.artifact_key
  AND artifact.status = 'stale'
  AND artifact.retired_at < sqlc.arg(retired_before)::timestamptz;

-- name: FindReferencedFontObjectKeys :many
SELECT referenced.object_key
FROM (
    SELECT artifact.object_key
    FROM font_artifacts AS artifact
    WHERE artifact.object_key = ANY(sqlc.arg(object_keys)::text[])
    UNION
    SELECT source.object_key
    FROM font_sources AS source
    WHERE source.object_key = ANY(sqlc.arg(object_keys)::text[])
) AS referenced
ORDER BY referenced.object_key;
