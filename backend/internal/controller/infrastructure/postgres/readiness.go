package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNilPool = errors.New("postgres pool is nil")

// These definitions are the PostgreSQL 16 normalized forms of migrations 9
// and 10. Keeping the authoritative definitions together makes schema changes
// require one deliberate readiness update instead of scattered substring
// checks.
var fontQuotaSchemaDefinition = schemaDefinition{
	QuotaExpression: `CASE
    WHEN (status = ANY (ARRAY['pending'::text, 'running'::text, 'missing'::text])) THEN reservation_bytes
    ELSE size_bytes
END`,
	Triggers: map[string]string{
		"font_artifact_quota_account_delete":     "CREATE TRIGGER font_artifact_quota_account_delete AFTER DELETE ON public.font_artifacts FOR EACH ROW EXECUTE FUNCTION account_font_artifact_quota_after_write()",
		"font_artifact_quota_account_insert":     "CREATE TRIGGER font_artifact_quota_account_insert AFTER INSERT ON public.font_artifacts FOR EACH ROW EXECUTE FUNCTION account_font_artifact_quota_after_write()",
		"font_artifact_quota_account_update":     "CREATE TRIGGER font_artifact_quota_account_update AFTER UPDATE OF status, size_bytes, reservation_bytes ON public.font_artifacts FOR EACH ROW WHEN ((old.quota_bytes IS DISTINCT FROM new.quota_bytes)) EXECUTE FUNCTION account_font_artifact_quota_after_write()",
		"font_artifact_quota_lock_family_delete": "CREATE TRIGGER font_artifact_quota_lock_family_delete BEFORE DELETE ON public.font_family FOR EACH STATEMENT EXECUTE FUNCTION lock_font_artifact_quota_before_write()",
		"font_artifact_quota_lock_insert_delete": "CREATE TRIGGER font_artifact_quota_lock_insert_delete BEFORE INSERT OR DELETE ON public.font_artifacts FOR EACH STATEMENT EXECUTE FUNCTION lock_font_artifact_quota_before_write()",
		"font_artifact_quota_lock_update":        "CREATE TRIGGER font_artifact_quota_lock_update BEFORE UPDATE OF status, size_bytes, reservation_bytes ON public.font_artifacts FOR EACH STATEMENT EXECUTE FUNCTION lock_font_artifact_quota_before_write()",
		"font_artifact_quota_reject_truncate":    "CREATE TRIGGER font_artifact_quota_reject_truncate BEFORE TRUNCATE ON public.font_artifacts FOR EACH STATEMENT EXECUTE FUNCTION reject_font_artifact_truncate()",
	},
	Functions: map[string]string{
		"lock_font_artifact_quota_before_write": `BEGIN
    PERFORM quota.artifact_count
    FROM public.font_artifact_quota AS quota
    WHERE quota.singleton
    FOR UPDATE;

    IF NOT FOUND THEN
        RAISE EXCEPTION 'font artifact quota ledger is missing';
    END IF;
    RETURN NULL;
END;`,
		"account_font_artifact_quota_after_write": `BEGIN
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
END;`,
		"reject_font_artifact_truncate": `BEGIN
    RAISE EXCEPTION 'font_artifacts cannot be truncated while transactional quota accounting is enabled'
        USING ERRCODE = '0A000';
END;`,
	},
	Constraints: map[string]string{
		"font_artifact_quota.font_artifact_quota_accounted_bytes_nonnegative": "CHECK ((accounted_bytes >= 0))",
		"font_artifact_quota.font_artifact_quota_artifact_count_nonnegative":  "CHECK ((artifact_count >= 0))",
		"font_artifact_quota.font_artifact_quota_pkey":                        "PRIMARY KEY (singleton)",
		"font_artifact_quota.font_artifact_quota_singleton_true":              "CHECK (singleton)",
		"font_terminal_failures.font_terminal_failures_failure_code_known":    "CHECK ((failure_code = 'unsupported_codepoints'::text))",
		"font_terminal_failures.font_terminal_failures_family_id_fkey":        "FOREIGN KEY (family_id) REFERENCES font_family(id) ON DELETE CASCADE",
		"font_terminal_failures.font_terminal_failures_kind_check":            "CHECK ((kind = ANY (ARRAY['dynamic'::text, 'static'::text])))",
		"font_terminal_failures.font_terminal_failures_pkey":                  "PRIMARY KEY (artifact_key)",
	},
	Indexes: map[string]string{
		"font_artifact_quota.font_artifact_quota_pkey":               "CREATE UNIQUE INDEX font_artifact_quota_pkey ON public.font_artifact_quota USING btree (singleton)",
		"font_terminal_failures.font_terminal_failures_eviction_idx": "CREATE INDEX font_terminal_failures_eviction_idx ON public.font_terminal_failures USING btree (cached_at, artifact_key)",
		"font_terminal_failures.font_terminal_failures_pkey":         "CREATE UNIQUE INDEX font_terminal_failures_pkey ON public.font_terminal_failures USING btree (artifact_key)",
	},
}

var fontQuotaSchemaDefinitionJSON = mustNormalizedSchemaDefinitionJSON(fontQuotaSchemaDefinition)

type schemaDefinition struct {
	QuotaExpression string            `json:"quota_expression"`
	Triggers        map[string]string `json:"triggers"`
	Functions       map[string]string `json:"functions"`
	Constraints     map[string]string `json:"constraints"`
	Indexes         map[string]string `json:"indexes"`
}

func normalizedSchemaDefinitionJSON(definition schemaDefinition) (string, error) {
	definition.QuotaExpression = normalizePostgresDefinition(definition.QuotaExpression)
	definition.Triggers = normalizedDefinitionMap(definition.Triggers)
	definition.Functions = normalizedDefinitionMap(definition.Functions)
	definition.Constraints = normalizedDefinitionMap(definition.Constraints)
	definition.Indexes = normalizedDefinitionMap(definition.Indexes)
	encoded, err := json.Marshal(definition)
	if err != nil {
		return "", fmt.Errorf("encode expected font schema definition: %w", err)
	}
	return string(encoded), nil
}

func normalizedDefinitionMap(definitions map[string]string) map[string]string {
	normalized := make(map[string]string, len(definitions))
	for name, value := range definitions {
		normalized[name] = normalizePostgresDefinition(value)
	}
	return normalized
}

func mustNormalizedSchemaDefinitionJSON(definition schemaDefinition) string {
	encoded, err := normalizedSchemaDefinitionJSON(definition)
	if err != nil {
		panic(err)
	}
	return encoded
}

func normalizePostgresDefinition(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// Readiness reports whether the database dependency is usable.
type Readiness struct {
	Ready     bool
	CheckedAt time.Time
	Latency   time.Duration
	Error     string
}

// Ping verifies that the pool can acquire a connection and round-trip to PostgreSQL.
func Ping(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return ErrNilPool
	}
	return pool.Ping(ctx)
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const fontSchemaReadinessQuery = `
	SELECT to_regclass('public.font_family') IS NOT NULL
	   AND to_regclass('public.system_metadata') IS NOT NULL
	   AND to_regclass('public.version') IS NOT NULL
	   AND to_regclass('public.static_fonts') IS NOT NULL
	   AND to_regclass('public.font_sources') IS NOT NULL
	   AND to_regclass('public.font_artifacts') IS NOT NULL
	   AND to_regclass('public.font_build_jobs') IS NOT NULL
	   AND to_regclass('public.font_artifact_quota') IS NOT NULL
	   AND to_regclass('public.font_terminal_failures') IS NOT NULL
	   AND to_regclass('public.font_artifact_fence_seq') IS NOT NULL
	   AND EXISTS (
	       SELECT 1 FROM information_schema.columns
	       WHERE table_schema = 'public' AND table_name = 'font_artifacts'
	         AND column_name = 'generation' AND data_type = 'bigint'
	   )
	   AND EXISTS (
	       SELECT 1 FROM information_schema.columns
	       WHERE table_schema = 'public' AND table_name = 'font_artifacts'
	         AND column_name = 'artifact_protocol_version' AND data_type = 'text'
	   )
	   AND EXISTS (
	       SELECT 1
	       FROM pg_catalog.pg_attribute AS attribute
	       JOIN pg_catalog.pg_class AS relation
	         ON relation.oid = attribute.attrelid
	       JOIN pg_catalog.pg_namespace AS namespace
	         ON namespace.oid = relation.relnamespace
	       JOIN pg_catalog.pg_attrdef AS generated_value
	         ON generated_value.adrelid = relation.oid
	        AND generated_value.adnum = attribute.attnum
	       WHERE namespace.nspname = 'public'
	         AND relation.relname = 'font_artifacts'
	         AND attribute.attname = 'quota_bytes'
	         AND attribute.atttypid = 'pg_catalog.int8'::pg_catalog.regtype
	         AND attribute.atttypmod = -1
	         AND attribute.attidentity = ''
	         AND attribute.attgenerated = 's'
	         AND pg_catalog.pg_get_expr(generated_value.adbin, generated_value.adrelid) LIKE '%status%'
	         AND pg_catalog.pg_get_expr(generated_value.adbin, generated_value.adrelid) LIKE '%pending%running%missing%'
	         AND pg_catalog.pg_get_expr(generated_value.adbin, generated_value.adrelid) LIKE '%reservation_bytes%'
	         AND pg_catalog.pg_get_expr(generated_value.adbin, generated_value.adrelid) LIKE '%size_bytes%'
	   )
	   AND (
	       SELECT COUNT(*) = 3
	          AND BOOL_AND(
	              CASE column_record.column_name
	                  WHEN 'singleton' THEN column_record.data_type = 'boolean'
	                  ELSE column_record.data_type = 'bigint'
	              END
	          )
	          AND BOOL_AND(column_record.is_nullable = 'NO')
	       FROM information_schema.columns AS column_record
	       WHERE column_record.table_schema = 'public'
	         AND column_record.table_name = 'font_artifact_quota'
	         AND column_record.column_name IN ('singleton', 'artifact_count', 'accounted_bytes')
	   )
	   AND (
	       SELECT COUNT(*) = 4
	          AND BOOL_AND(constraint_record.convalidated)
	          AND BOOL_AND(
	              CASE constraint_record.conname
	                  WHEN 'font_artifact_quota_pkey' THEN constraint_record.contype = 'p'
	                  ELSE constraint_record.contype = 'c'
	              END
	          )
	       FROM pg_catalog.pg_constraint AS constraint_record
	       WHERE constraint_record.conrelid = to_regclass('public.font_artifact_quota')
	         AND constraint_record.conname IN (
	             'font_artifact_quota_pkey',
	             'font_artifact_quota_singleton_true',
	             'font_artifact_quota_artifact_count_nonnegative',
	             'font_artifact_quota_accounted_bytes_nonnegative'
	         )
	   )
	   AND (
	       SELECT COUNT(*) = 15
	          AND BOOL_AND(
	              CASE
	                  WHEN column_record.column_name IN (
	                      'artifact_key', 'kind', 'family_id', 'builder_version',
	                      'artifact_protocol_version', 'content_type', 'failure_code'
	                  ) THEN column_record.data_type = 'text'
	                  WHEN column_record.column_name = 'weight' THEN column_record.data_type = 'smallint'
	                  WHEN column_record.column_name = 'version' THEN column_record.data_type = 'integer'
	                  WHEN column_record.column_name IN (
	                      'pack', 'word_hash', 'normalized_word_set',
	                      'source_checksum_sha256', 'error'
	                  ) THEN column_record.data_type = 'text'
	                  WHEN column_record.column_name = 'cached_at'
	                      THEN column_record.data_type = 'timestamp with time zone'
	                  ELSE FALSE
	              END
	          )
	          AND BOOL_AND(
	              CASE
	                  WHEN column_record.column_name IN (
	                      'version', 'pack', 'word_hash', 'normalized_word_set',
	                      'source_checksum_sha256', 'error'
	                  ) THEN column_record.is_nullable = 'YES'
	                  ELSE column_record.is_nullable = 'NO'
	              END
	          )
	          AND BOOL_AND(column_record.is_identity = 'NO')
	          AND BOOL_AND(column_record.is_generated = 'NEVER')
	          AND BOOL_AND(
	              CASE column_record.column_name
	                  WHEN 'cached_at' THEN column_record.column_default = 'now()'
	                  ELSE column_record.column_default IS NULL
	              END
	          )
	       FROM information_schema.columns AS column_record
	       WHERE column_record.table_schema = 'public'
	         AND column_record.table_name = 'font_terminal_failures'
	   )
	   AND (
	       SELECT COUNT(*) = 4
	          AND BOOL_AND(constraint_record.convalidated)
	          AND BOOL_AND(
	              CASE constraint_record.conname
	                  WHEN 'font_terminal_failures_pkey' THEN
	                      constraint_record.contype = 'p'
	                      AND pg_catalog.pg_get_constraintdef(constraint_record.oid, false) =
	                          'PRIMARY KEY (artifact_key)'
	                  WHEN 'font_terminal_failures_kind_check' THEN
	                      constraint_record.contype = 'c'
	                      AND pg_catalog.pg_get_constraintdef(constraint_record.oid, false) =
	                          'CHECK ((kind = ANY (ARRAY[''dynamic''::text, ''static''::text])))'
	                  WHEN 'font_terminal_failures_family_id_fkey' THEN
	                      constraint_record.contype = 'f'
	                      AND pg_catalog.pg_get_constraintdef(constraint_record.oid, false) =
	                          'FOREIGN KEY (family_id) REFERENCES font_family(id) ON DELETE CASCADE'
	                  WHEN 'font_terminal_failures_failure_code_known' THEN
	                      constraint_record.contype = 'c'
	                      AND pg_catalog.pg_get_constraintdef(constraint_record.oid, false) =
	                          'CHECK ((failure_code = ''unsupported_codepoints''::text))'
	                  ELSE FALSE
	              END
	          )
	       FROM pg_catalog.pg_constraint AS constraint_record
	       WHERE constraint_record.conrelid = to_regclass('public.font_terminal_failures')
	         AND constraint_record.conname IN (
	             'font_terminal_failures_pkey',
	             'font_terminal_failures_kind_check',
	             'font_terminal_failures_family_id_fkey',
	             'font_terminal_failures_failure_code_known'
	         )
	   )
	   AND EXISTS (
	       SELECT 1
	       FROM pg_catalog.pg_index AS index_record
	       JOIN pg_catalog.pg_class AS index_relation
	         ON index_relation.oid = index_record.indexrelid
	       JOIN pg_catalog.pg_namespace AS index_namespace
	         ON index_namespace.oid = index_relation.relnamespace
	       WHERE index_record.indrelid = to_regclass('public.font_terminal_failures')
	         AND index_namespace.nspname = 'public'
	         AND index_relation.relname = 'font_terminal_failures_eviction_idx'
	         AND index_record.indisvalid
	         AND index_record.indisready
	         AND NOT index_record.indisunique
	         AND NOT index_record.indisprimary
	         AND index_record.indnkeyatts = 2
	         AND index_record.indnatts = 2
	         AND index_record.indexprs IS NULL
	         AND index_record.indpred IS NULL
	         AND pg_catalog.pg_get_indexdef(index_record.indexrelid) =
	             'CREATE INDEX font_terminal_failures_eviction_idx ON public.font_terminal_failures USING btree (cached_at, artifact_key)'
	   )
			   AND (
			       SELECT COUNT(*) = 6 AND BOOL_AND(trigger_record.tgenabled = 'O')
			       FROM pg_catalog.pg_trigger AS trigger_record
			       WHERE trigger_record.tgrelid = to_regclass('public.font_artifacts')
			         AND NOT trigger_record.tgisinternal
			         AND trigger_record.tgname IN (
			             'font_artifact_quota_lock_insert_delete',
			             'font_artifact_quota_lock_update',
			             'font_artifact_quota_reject_truncate',
			             'font_artifact_quota_account_insert',
			             'font_artifact_quota_account_delete',
			             'font_artifact_quota_account_update'
			         )
			   )
			   AND EXISTS (
			       SELECT 1
			       FROM pg_catalog.pg_trigger AS trigger_record
			       WHERE trigger_record.tgrelid = to_regclass('public.font_family')
			         AND NOT trigger_record.tgisinternal
			         AND trigger_record.tgname = 'font_artifact_quota_lock_family_delete'
			         AND trigger_record.tgenabled = 'O'
			   )
			   AND EXISTS (
			       SELECT 1
			       FROM pg_catalog.pg_proc AS function_record
			       JOIN pg_catalog.pg_namespace AS namespace
			         ON namespace.oid = function_record.pronamespace
			       WHERE namespace.nspname = 'public'
			         AND function_record.proname = 'lock_font_artifact_quota_before_write'
			         AND function_record.prosecdef
			   )
			   AND EXISTS (
			       SELECT 1
			       FROM pg_catalog.pg_proc AS function_record
			       JOIN pg_catalog.pg_namespace AS namespace
			         ON namespace.oid = function_record.pronamespace
			       WHERE namespace.nspname = 'public'
			         AND function_record.proname = 'reject_font_artifact_truncate'
			         AND function_record.prosecdef
			   )
			   AND EXISTS (
			       SELECT 1
			       FROM pg_catalog.pg_proc AS function_record
			       JOIN pg_catalog.pg_namespace AS namespace
			         ON namespace.oid = function_record.pronamespace
			       WHERE namespace.nspname = 'public'
			         AND function_record.proname = 'account_font_artifact_quota_after_write'
			         AND function_record.prosecdef
			   )
			   AND EXISTS (
			       SELECT 1 FROM information_schema.columns
			       WHERE table_schema = 'public' AND table_name = 'font_artifacts'
			         AND column_name = 'retired_at' AND data_type = 'timestamp with time zone'
			   )
			   AND EXISTS (
			       SELECT 1 FROM information_schema.columns
			       WHERE table_schema = 'public' AND table_name = 'font_artifacts'
			         AND column_name = 'object_version_id' AND data_type = 'text'
			   )
			   AND EXISTS (
			       SELECT 1 FROM information_schema.columns
			       WHERE table_schema = 'public' AND table_name = 'font_artifacts'
			         AND column_name = 'failure_code' AND data_type = 'text'
			   )
			   AND EXISTS (
			       SELECT 1
			       FROM pg_catalog.pg_attribute AS attribute
			       JOIN pg_catalog.pg_class AS relation
			         ON relation.oid = attribute.attrelid
			       JOIN pg_catalog.pg_namespace AS namespace
			         ON namespace.oid = relation.relnamespace
			       JOIN pg_catalog.pg_attrdef AS default_value
			         ON default_value.adrelid = relation.oid
			        AND default_value.adnum = attribute.attnum
			       WHERE namespace.nspname = 'public'
			         AND relation.relname = 'font_artifacts'
			         AND relation.relkind IN ('r', 'p')
			         AND attribute.attname = 'reservation_bytes'
			         AND attribute.attnum > 0
			         AND NOT attribute.attisdropped
			         AND attribute.atttypid = 'pg_catalog.int8'::pg_catalog.regtype
			         AND attribute.atttypmod = -1
			         AND attribute.attnotnull
			         AND attribute.attidentity = ''
			         AND attribute.attgenerated = ''
			         AND pg_catalog.pg_get_expr(default_value.adbin, default_value.adrelid) = '134217728'
			   )
			   AND EXISTS (
			       SELECT 1
			       FROM pg_catalog.pg_constraint AS constraint_record
			       JOIN pg_catalog.pg_class AS relation
			         ON relation.oid = constraint_record.conrelid
			       JOIN pg_catalog.pg_namespace AS namespace
			         ON namespace.oid = relation.relnamespace
			       WHERE namespace.nspname = 'public'
			         AND relation.relname = 'font_artifacts'
			         AND constraint_record.conname = 'font_artifacts_reservation_bytes_positive'
			         AND constraint_record.contype = 'c'
			         AND constraint_record.convalidated
			         AND NOT constraint_record.connoinherit
			         AND pg_catalog.pg_get_constraintdef(constraint_record.oid, false) =
			             'CHECK ((reservation_bytes > 0))'
			   )
			   AND EXISTS (
			       SELECT 1
			       FROM pg_catalog.pg_constraint AS constraint_record
			       JOIN pg_catalog.pg_class AS relation
			         ON relation.oid = constraint_record.conrelid
			       JOIN pg_catalog.pg_namespace AS namespace
			         ON namespace.oid = relation.relnamespace
			       WHERE namespace.nspname = 'public'
			         AND relation.relname = 'font_artifacts'
			         AND constraint_record.conname = 'font_artifacts_size_within_reservation'
			         AND constraint_record.contype = 'c'
			         AND constraint_record.convalidated
			         AND NOT constraint_record.connoinherit
			         AND pg_catalog.pg_get_constraintdef(constraint_record.oid, false) =
			             'CHECK ((size_bytes <= reservation_bytes))'
			   )
		   AND EXISTS (
		       SELECT 1 FROM information_schema.columns
		       WHERE table_schema = 'public' AND table_name = 'font_build_jobs'
		         AND column_name = 'attempts' AND data_type = 'bigint'
		   )
			   AND EXISTS (
			       SELECT 1 FROM information_schema.columns
			       WHERE table_schema = 'public' AND table_name = 'font_build_jobs'
			         AND column_name = 'next_attempt_at' AND data_type = 'timestamp with time zone'
			   )
			   AND EXISTS (
			       SELECT 1 FROM information_schema.columns
			       WHERE table_schema = 'public' AND table_name = 'font_build_jobs'
			         AND column_name = 'fence' AND data_type = 'bigint'
			   )
			   AND EXISTS (
			       SELECT 1 FROM information_schema.columns
			       WHERE table_schema = 'public' AND table_name = 'font_build_jobs'
			         AND column_name = 'retryable' AND data_type = 'boolean'
			   )
			   AND EXISTS (
			       SELECT 1 FROM information_schema.columns
			       WHERE table_schema = 'public' AND table_name = 'font_build_jobs'
			         AND column_name = 'failure_code' AND data_type = 'text'
			   )
			   AND has_table_privilege(current_user, 'public.font_family', 'SELECT')
			   AND has_table_privilege(current_user, 'public.font_sources', 'SELECT')
			   AND has_table_privilege(current_user, 'public.version', 'SELECT')
			   AND has_table_privilege(current_user, 'public.static_fonts', 'SELECT')
			   AND has_table_privilege(current_user, 'public.system_metadata', 'SELECT')
			   AND has_table_privilege(current_user, 'public.font_artifacts', 'SELECT')
			   AND has_table_privilege(current_user, 'public.font_artifacts', 'INSERT')
			   AND has_table_privilege(current_user, 'public.font_artifacts', 'UPDATE')
			   AND has_table_privilege(current_user, 'public.font_artifacts', 'DELETE')
				   AND has_table_privilege(current_user, 'public.font_build_jobs', 'SELECT')
				   AND has_table_privilege(current_user, 'public.font_build_jobs', 'INSERT')
				   AND has_table_privilege(current_user, 'public.font_build_jobs', 'UPDATE')
				   AND has_table_privilege(current_user, to_regclass('public.font_terminal_failures'), 'SELECT')
				   AND has_table_privilege(current_user, to_regclass('public.font_terminal_failures'), 'INSERT')
				   AND has_table_privilege(current_user, to_regclass('public.font_terminal_failures'), 'UPDATE')
				   AND has_table_privilege(current_user, to_regclass('public.font_terminal_failures'), 'DELETE')
				   AND has_table_privilege(current_user, to_regclass('public.font_artifact_quota'), 'SELECT')
			   AND (
			       COALESCE((
			           SELECT role_record.rolsuper
			           FROM pg_catalog.pg_roles AS role_record
			           WHERE role_record.rolname = current_user
			       ), FALSE)
			       OR (
			           NOT has_table_privilege(current_user, to_regclass('public.font_artifact_quota'), 'UPDATE')
			           AND has_column_privilege(current_user, 'public.font_artifact_quota', 'singleton', 'UPDATE')
			           AND NOT has_column_privilege(current_user, 'public.font_artifact_quota', 'artifact_count', 'UPDATE')
			           AND NOT has_column_privilege(current_user, 'public.font_artifact_quota', 'accounted_bytes', 'UPDATE')
			           AND NOT has_table_privilege(current_user, to_regclass('public.font_artifact_quota'), 'INSERT')
			           AND NOT has_table_privilege(current_user, to_regclass('public.font_artifact_quota'), 'DELETE')
			           AND NOT has_table_privilege(current_user, to_regclass('public.font_artifact_quota'), 'TRUNCATE')
			           AND NOT has_table_privilege(current_user, to_regclass('public.font_artifact_quota'), 'REFERENCES')
			           AND NOT has_table_privilege(current_user, to_regclass('public.font_artifact_quota'), 'TRIGGER')
			       )
			   )
			   AND has_sequence_privilege(current_user, 'public.font_build_jobs_id_seq', 'USAGE')
			   AND has_sequence_privilege(current_user, 'public.font_artifact_fence_seq', 'USAGE')`

const fontSchemaSemanticReadinessQuery = `
	WITH actual_definition AS (
	    SELECT jsonb_build_object(
	        'quota_expression', (
	            SELECT btrim(regexp_replace(pg_get_expr(default_value.adbin, default_value.adrelid), '[[:space:]]+', ' ', 'g'))
	            FROM pg_catalog.pg_attribute AS attribute
	            JOIN pg_catalog.pg_attrdef AS default_value
	              ON default_value.adrelid = attribute.attrelid
	             AND default_value.adnum = attribute.attnum
	            WHERE attribute.attrelid = 'public.font_artifacts'::regclass
	              AND attribute.attname = 'quota_bytes'
	              AND attribute.attnum > 0
	              AND NOT attribute.attisdropped
	        ),
	        'triggers', (
	            SELECT COALESCE(
	                jsonb_object_agg(
	                    trigger_record.tgname,
	                    btrim(regexp_replace(pg_get_triggerdef(trigger_record.oid, false), '[[:space:]]+', ' ', 'g'))
	                    ORDER BY trigger_record.tgname
	                ),
	                '{}'::jsonb
	            )
	            FROM pg_catalog.pg_trigger AS trigger_record
	            WHERE trigger_record.tgrelid IN (
	                'public.font_artifacts'::regclass,
	                'public.font_family'::regclass
	            )
	              AND NOT trigger_record.tgisinternal
	        ),
	        'functions', (
	            SELECT COALESCE(
	                jsonb_object_agg(
	                    function_record.proname,
	                    btrim(regexp_replace(function_record.prosrc, '[[:space:]]+', ' ', 'g'))
	                    ORDER BY function_record.proname
	                ),
	                '{}'::jsonb
	            )
	            FROM pg_catalog.pg_proc AS function_record
	            WHERE function_record.pronamespace = 'public'::regnamespace
	              AND function_record.proname IN (
	                  'lock_font_artifact_quota_before_write',
	                  'account_font_artifact_quota_after_write',
	                  'reject_font_artifact_truncate'
	              )
	        ),
	        'constraints', (
	            SELECT COALESCE(
	                jsonb_object_agg(
	                    relation.relname || '.' || constraint_record.conname,
	                    btrim(regexp_replace(pg_get_constraintdef(constraint_record.oid, false), '[[:space:]]+', ' ', 'g'))
	                    ORDER BY relation.relname, constraint_record.conname
	                ),
	                '{}'::jsonb
	            )
	            FROM pg_catalog.pg_constraint AS constraint_record
	            JOIN pg_catalog.pg_class AS relation ON relation.oid = constraint_record.conrelid
	            WHERE constraint_record.conrelid IN (
	                'public.font_artifact_quota'::regclass,
	                'public.font_terminal_failures'::regclass
	            )
	        ),
	        'indexes', (
	            SELECT COALESCE(
	                jsonb_object_agg(
	                    relation.relname || '.' || index_relation.relname,
	                    btrim(regexp_replace(pg_get_indexdef(index_record.indexrelid), '[[:space:]]+', ' ', 'g'))
	                    ORDER BY relation.relname, index_relation.relname
	                ),
	                '{}'::jsonb
	            )
	            FROM pg_catalog.pg_index AS index_record
	            JOIN pg_catalog.pg_class AS relation ON relation.oid = index_record.indrelid
	            JOIN pg_catalog.pg_class AS index_relation ON index_relation.oid = index_record.indexrelid
	            WHERE index_record.indrelid IN (
	                'public.font_artifact_quota'::regclass,
	                'public.font_terminal_failures'::regclass
	            )
	        )
	    ) AS definition
	), function_integrity AS (
	    SELECT COUNT(*) = 3
	       AND BOOL_AND(function_record.prokind = 'f')
	       AND BOOL_AND(function_record.prosecdef)
	       AND BOOL_AND(NOT function_record.proleakproof)
	       AND BOOL_AND(NOT function_record.proisstrict)
	       AND BOOL_AND(NOT function_record.proretset)
	       AND BOOL_AND(function_record.provolatile = 'v')
	       AND BOOL_AND(function_record.proparallel = 'u')
	       AND BOOL_AND(function_record.pronargs = 0)
	       AND BOOL_AND(function_record.prorettype = 'pg_catalog.trigger'::regtype)
	       AND BOOL_AND(language_record.lanname = 'plpgsql')
	       AND BOOL_AND(function_record.proconfig = ARRAY['search_path=pg_catalog, public']::text[])
	       AND BOOL_AND(function_record.proacl IS NULL)
	       AND BOOL_AND(function_record.proowner = quota_table.relowner)
	       AND BOOL_AND(function_record.proowner = artifact_table.relowner)
	       AND BOOL_AND(function_record.proowner = family_table.relowner)
	       AND BOOL_AND(function_record.proowner = authority_table.relowner)
	       AS valid
	    FROM pg_catalog.pg_proc AS function_record
	    JOIN pg_catalog.pg_language AS language_record ON language_record.oid = function_record.prolang
	    CROSS JOIN pg_catalog.pg_class AS quota_table
	    CROSS JOIN pg_catalog.pg_class AS artifact_table
	    CROSS JOIN pg_catalog.pg_class AS family_table
	    CROSS JOIN pg_catalog.pg_class AS authority_table
	    WHERE function_record.pronamespace = 'public'::regnamespace
	      AND function_record.proname IN (
	          'lock_font_artifact_quota_before_write',
	          'account_font_artifact_quota_after_write',
	          'reject_font_artifact_truncate'
	      )
	      AND quota_table.oid = 'public.font_artifact_quota'::regclass
	      AND artifact_table.oid = 'public.font_artifacts'::regclass
	      AND family_table.oid = 'public.font_family'::regclass
	      AND authority_table.oid = 'public.system_metadata'::regclass
	), ownership_integrity AS (
	    SELECT quota_table.relowner = authority_table.relowner
	       AND artifact_table.relowner = authority_table.relowner
	       AND family_table.relowner = authority_table.relowner
	       AND terminal_table.relowner = authority_table.relowner AS valid
	    FROM pg_catalog.pg_class AS quota_table
	    CROSS JOIN pg_catalog.pg_class AS artifact_table
	    CROSS JOIN pg_catalog.pg_class AS family_table
	    CROSS JOIN pg_catalog.pg_class AS terminal_table
	    CROSS JOIN pg_catalog.pg_class AS authority_table
	    WHERE quota_table.oid = 'public.font_artifact_quota'::regclass
	      AND artifact_table.oid = 'public.font_artifacts'::regclass
	      AND family_table.oid = 'public.font_family'::regclass
	      AND terminal_table.oid = 'public.font_terminal_failures'::regclass
	      AND authority_table.oid = 'public.system_metadata'::regclass
	), trigger_integrity AS (
	    SELECT COUNT(*) = 7 AND BOOL_AND(trigger_record.tgenabled = 'O') AS valid
	    FROM pg_catalog.pg_trigger AS trigger_record
	    WHERE trigger_record.tgrelid IN (
	        'public.font_artifacts'::regclass,
	        'public.font_family'::regclass
	    )
	      AND NOT trigger_record.tgisinternal
	), constraint_integrity AS (
	    SELECT COUNT(*) = 8 AND BOOL_AND(constraint_record.convalidated) AS valid
	    FROM pg_catalog.pg_constraint AS constraint_record
	    WHERE constraint_record.conrelid IN (
	        'public.font_artifact_quota'::regclass,
	        'public.font_terminal_failures'::regclass
	    )
	), index_integrity AS (
	    SELECT COUNT(*) = 3
	       AND BOOL_AND(index_record.indisvalid)
	       AND BOOL_AND(index_record.indisready) AS valid
	    FROM pg_catalog.pg_index AS index_record
	    WHERE index_record.indrelid IN (
	        'public.font_artifact_quota'::regclass,
	        'public.font_terminal_failures'::regclass
	    )
	)
	SELECT actual_definition.definition = $1::jsonb
	   AND function_integrity.valid
	   AND ownership_integrity.valid
	   AND trigger_integrity.valid
	   AND constraint_integrity.valid
	   AND index_integrity.valid
	FROM actual_definition
	CROSS JOIN function_integrity
	CROSS JOIN ownership_integrity
	CROSS JOIN trigger_integrity
	CROSS JOIN constraint_integrity
	CROSS JOIN index_integrity`

const fontQuotaReadinessQuery = `
	SELECT COUNT(*) = 1
	   AND BOOL_AND(quota.singleton)
	   AND BOOL_AND(quota.artifact_count = authoritative.artifact_count)
	   AND BOOL_AND(quota.accounted_bytes = authoritative.accounted_bytes)
	FROM public.font_artifact_quota AS quota
	CROSS JOIN (
	    SELECT COUNT(*)::BIGINT AS artifact_count,
	           COALESCE(SUM(artifact.quota_bytes), 0)::BIGINT AS accounted_bytes
	    FROM public.font_artifacts AS artifact
	) AS authoritative`

func FontSchemaReady(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return ErrNilPool
	}
	return fontSchemaReady(ctx, pool)
}

func fontSchemaReady(ctx context.Context, queryer rowQuerier) error {
	var ready bool
	err := queryer.QueryRow(ctx, fontSchemaReadinessQuery).Scan(&ready)
	if err != nil {
		return fmt.Errorf("check font schema: %w", err)
	}
	if !ready {
		return errors.New("font database migrations are not applied")
	}
	if err := queryer.QueryRow(ctx, fontSchemaSemanticReadinessQuery, fontQuotaSchemaDefinitionJSON).Scan(&ready); err != nil {
		return fmt.Errorf("check authoritative font quota schema: %w", err)
	}
	if !ready {
		return errors.New("font database migrations are not applied")
	}
	if err := queryer.QueryRow(ctx, fontQuotaReadinessQuery).Scan(&ready); err != nil {
		return fmt.Errorf("check font artifact quota ledger: %w", err)
	}
	if !ready {
		return errors.New("font artifact quota ledger is missing or inconsistent")
	}
	return nil
}

// CheckReadiness is a status-oriented wrapper around Ping for health endpoints.
func CheckReadiness(ctx context.Context, pool *pgxpool.Pool) Readiness {
	startedAt := time.Now()
	status := Readiness{
		CheckedAt: startedAt,
	}

	err := Ping(ctx, pool)
	status.Latency = time.Since(startedAt)
	if err != nil {
		status.Error = err.Error()
		return status
	}

	status.Ready = true
	return status
}
