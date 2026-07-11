-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- reservation_bytes is charged only while work owns or needs build capacity.
-- A failed artifact keeps its artifact slot and actual bytes, but must be
-- admitted again before another build can reserve capacity.
ALTER TABLE font_artifacts
    ADD COLUMN quota_bytes BIGINT GENERATED ALWAYS AS (
        CASE
            WHEN status IN ('pending', 'running', 'missing') THEN reservation_bytes
            ELSE size_bytes
        END
    ) STORED;

CREATE TABLE font_artifact_quota (
    singleton BOOLEAN NOT NULL DEFAULT TRUE,
    artifact_count BIGINT NOT NULL,
    accounted_bytes BIGINT NOT NULL,
    CONSTRAINT font_artifact_quota_pkey PRIMARY KEY (singleton),
    CONSTRAINT font_artifact_quota_singleton_true CHECK (singleton),
    CONSTRAINT font_artifact_quota_artifact_count_nonnegative CHECK (artifact_count >= 0),
    CONSTRAINT font_artifact_quota_accounted_bytes_nonnegative CHECK (accounted_bytes >= 0)
);

INSERT INTO font_artifact_quota (singleton, artifact_count, accounted_bytes)
SELECT TRUE, COUNT(*)::BIGINT, COALESCE(SUM(quota_bytes), 0)::BIGINT
FROM font_artifacts;

-- +goose StatementBegin
CREATE FUNCTION lock_font_artifact_quota_before_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    PERFORM quota.artifact_count
    FROM public.font_artifact_quota AS quota
    WHERE quota.singleton
    FOR UPDATE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'font artifact quota ledger is missing';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION account_font_artifact_quota_after_write()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        UPDATE public.font_artifact_quota
        SET
            artifact_count = artifact_count + 1,
            accounted_bytes = accounted_bytes + NEW.quota_bytes
        WHERE singleton;
        RETURN NEW;
    ELSIF TG_OP = 'DELETE' THEN
        UPDATE public.font_artifact_quota
        SET
            artifact_count = artifact_count - 1,
            accounted_bytes = accounted_bytes - OLD.quota_bytes
        WHERE singleton;
        RETURN OLD;
    END IF;

    UPDATE public.font_artifact_quota
    SET accounted_bytes = accounted_bytes + NEW.quota_bytes - OLD.quota_bytes
    WHERE singleton;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- A row-trigger ledger cannot account TRUNCATE. Fail closed and require
-- DELETE, whose row triggers preserve the exact counters.
-- +goose StatementBegin
CREATE FUNCTION reject_font_artifact_truncate()
RETURNS TRIGGER
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
BEGIN
    RAISE EXCEPTION 'font_artifacts cannot be truncated while transactional quota accounting is enabled'
        USING ERRCODE = '0A000';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER font_artifact_quota_lock_insert_delete
BEFORE INSERT OR DELETE ON font_artifacts
FOR EACH STATEMENT
EXECUTE FUNCTION lock_font_artifact_quota_before_write();

CREATE TRIGGER font_artifact_quota_lock_update
BEFORE UPDATE OF status, size_bytes, reservation_bytes ON font_artifacts
FOR EACH STATEMENT
EXECUTE FUNCTION lock_font_artifact_quota_before_write();

-- Foreign-key cascade deletes do not fire statement triggers on the child
-- table. Lock at the parent entry point to preserve ledger-first lock order.
CREATE TRIGGER font_artifact_quota_lock_family_delete
BEFORE DELETE ON font_family
FOR EACH STATEMENT
EXECUTE FUNCTION lock_font_artifact_quota_before_write();

CREATE TRIGGER font_artifact_quota_reject_truncate
BEFORE TRUNCATE ON font_artifacts
FOR EACH STATEMENT
EXECUTE FUNCTION reject_font_artifact_truncate();

CREATE TRIGGER font_artifact_quota_account_insert
AFTER INSERT ON font_artifacts
FOR EACH ROW
EXECUTE FUNCTION account_font_artifact_quota_after_write();

CREATE TRIGGER font_artifact_quota_account_delete
AFTER DELETE ON font_artifacts
FOR EACH ROW
EXECUTE FUNCTION account_font_artifact_quota_after_write();

CREATE TRIGGER font_artifact_quota_account_update
AFTER UPDATE OF status, size_bytes, reservation_bytes ON font_artifacts
FOR EACH ROW
WHEN (OLD.quota_bytes IS DISTINCT FROM NEW.quota_bytes)
EXECUTE FUNCTION account_font_artifact_quota_after_write();

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

DROP TRIGGER IF EXISTS font_artifact_quota_account_update ON font_artifacts;
DROP TRIGGER IF EXISTS font_artifact_quota_account_delete ON font_artifacts;
DROP TRIGGER IF EXISTS font_artifact_quota_account_insert ON font_artifacts;
DROP TRIGGER IF EXISTS font_artifact_quota_reject_truncate ON font_artifacts;
DROP TRIGGER IF EXISTS font_artifact_quota_lock_update ON font_artifacts;
DROP TRIGGER IF EXISTS font_artifact_quota_lock_insert_delete ON font_artifacts;
DROP TRIGGER IF EXISTS font_artifact_quota_lock_family_delete ON font_family;

DROP FUNCTION IF EXISTS reject_font_artifact_truncate();
DROP FUNCTION IF EXISTS account_font_artifact_quota_after_write();
DROP FUNCTION IF EXISTS lock_font_artifact_quota_before_write();

DROP TABLE IF EXISTS font_artifact_quota;

ALTER TABLE font_artifacts
    DROP COLUMN IF EXISTS quota_bytes;
