package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestFontSchemaReadinessQueryRequiresMigration10Contract(t *testing.T) {
	requiredFragments := []struct {
		name     string
		query    string
		fragment string
	}{
		{name: "column", fragment: "attribute.attname = 'reservation_bytes'"},
		{name: "bigint type", fragment: "attribute.atttypid = 'pg_catalog.int8'::pg_catalog.regtype"},
		{name: "unmodified bigint", fragment: "attribute.atttypmod = -1"},
		{name: "not null", fragment: "attribute.attnotnull"},
		{name: "ordinary column", fragment: "attribute.attidentity = ''"},
		{name: "not generated", fragment: "attribute.attgenerated = ''"},
		{name: "default", fragment: "pg_catalog.pg_get_expr(default_value.adbin, default_value.adrelid) = '134217728'"},
		{name: "positive constraint", fragment: "constraint_record.conname = 'font_artifacts_reservation_bytes_positive'"},
		{name: "positive expression", fragment: "'CHECK ((reservation_bytes > 0))'"},
		{name: "size constraint", fragment: "constraint_record.conname = 'font_artifacts_size_within_reservation'"},
		{name: "size expression", fragment: "'CHECK ((size_bytes <= reservation_bytes))'"},
		{name: "quota table", fragment: "to_regclass('public.font_artifact_quota') IS NOT NULL"},
		{name: "terminal cache table", fragment: "to_regclass('public.font_terminal_failures') IS NOT NULL"},
		{name: "terminal cache columns", fragment: "SELECT COUNT(*) = 15"},
		{name: "terminal cache timestamp", fragment: "WHEN column_record.column_name = 'cached_at'"},
		{name: "terminal cache failure code", fragment: "'content_type', 'failure_code'"},
		{name: "terminal cache primary key", fragment: "'font_terminal_failures_pkey'"},
		{name: "terminal cache kind constraint", fragment: "'font_terminal_failures_kind_check'"},
		{name: "terminal cache family foreign key", fragment: "'font_terminal_failures_family_id_fkey'"},
		{name: "terminal cache failure constraint", fragment: "'font_terminal_failures_failure_code_known'"},
		{name: "terminal cache eviction index", fragment: "USING btree (cached_at, artifact_key)"},
		{name: "terminal cache select privilege", fragment: "to_regclass('public.font_terminal_failures'), 'SELECT'"},
		{name: "terminal cache insert privilege", fragment: "to_regclass('public.font_terminal_failures'), 'INSERT'"},
		{name: "terminal cache update privilege", fragment: "to_regclass('public.font_terminal_failures'), 'UPDATE'"},
		{name: "terminal cache delete privilege", fragment: "to_regclass('public.font_terminal_failures'), 'DELETE'"},
		{name: "generated quota column", fragment: "attribute.attname = 'quota_bytes'"},
		{name: "stored generated column", fragment: "attribute.attgenerated = 's'"},
		{name: "quota singleton type", fragment: "WHEN 'singleton' THEN column_record.data_type = 'boolean'"},
		{name: "quota counter type", fragment: "ELSE column_record.data_type = 'bigint'"},
		{name: "quota primary key type", fragment: "WHEN 'font_artifact_quota_pkey' THEN constraint_record.contype = 'p'"},
		{name: "quota singleton", query: fontQuotaReadinessQuery, fragment: "COUNT(*) = 1"},
		{name: "quota artifact count", query: fontQuotaReadinessQuery, fragment: "quota.artifact_count = authoritative.artifact_count"},
		{name: "quota accounted bytes", query: fontQuotaReadinessQuery, fragment: "quota.accounted_bytes = authoritative.accounted_bytes"},
		{name: "authoritative artifact count", query: fontQuotaReadinessQuery, fragment: "COUNT(*)::BIGINT AS artifact_count"},
		{name: "authoritative accounted bytes", query: fontQuotaReadinessQuery, fragment: "COALESCE(SUM(artifact.quota_bytes), 0)::BIGINT AS accounted_bytes"},
		{name: "quota triggers", fragment: "COUNT(*) = 6 AND BOOL_AND(trigger_record.tgenabled = 'O')"},
		{name: "quota truncate trigger", fragment: "'font_artifact_quota_reject_truncate'"},
		{name: "quota family delete trigger", fragment: "trigger_record.tgname = 'font_artifact_quota_lock_family_delete'"},
		{name: "quota lock function", fragment: "function_record.proname = 'lock_font_artifact_quota_before_write'"},
		{name: "quota accounting function", fragment: "function_record.proname = 'account_font_artifact_quota_after_write'"},
		{name: "quota truncate rejection function", fragment: "function_record.proname = 'reject_font_artifact_truncate'"},
		{name: "quota select privilege", fragment: "has_table_privilege(current_user, to_regclass('public.font_artifact_quota'), 'SELECT')"},
		{name: "quota superuser control role", fragment: "SELECT role_record.rolsuper"},
		{name: "quota table update denied", fragment: "NOT has_table_privilege(current_user, to_regclass('public.font_artifact_quota'), 'UPDATE')"},
		{name: "quota lock column privilege", fragment: "has_column_privilege(current_user, 'public.font_artifact_quota', 'singleton', 'UPDATE')"},
		{name: "quota counter update denied", fragment: "NOT has_column_privilege(current_user, 'public.font_artifact_quota', 'accounted_bytes', 'UPDATE')"},
		{name: "quota insert denied", fragment: "to_regclass('public.font_artifact_quota'), 'INSERT'"},
		{name: "quota delete denied", fragment: "to_regclass('public.font_artifact_quota'), 'DELETE'"},
		{name: "quota truncate denied", fragment: "to_regclass('public.font_artifact_quota'), 'TRUNCATE'"},
		{name: "exact schema fingerprint", query: fontSchemaSemanticReadinessQuery, fragment: "actual_definition.definition = $1::jsonb"},
		{name: "exact function metadata", query: fontSchemaSemanticReadinessQuery, fragment: "function_record.proconfig = ARRAY['search_path=pg_catalog, public']::text[]"},
		{name: "exact function owner", query: fontSchemaSemanticReadinessQuery, fragment: "function_record.proowner = quota_table.relowner"},
	}

	for _, required := range requiredFragments {
		t.Run(required.name, func(t *testing.T) {
			query := required.query
			if query == "" {
				query = fontSchemaReadinessQuery
			}
			if !strings.Contains(query, required.fragment) {
				t.Fatalf("font schema readiness query does not require %s", required.name)
			}
		})
	}
	if count := strings.Count(fontSchemaReadinessQuery, "constraint_record.contype = 'c'"); count != 5 {
		t.Fatalf("check constraint type assertions = %d, want 5", count)
	}
	if count := strings.Count(fontSchemaReadinessQuery, "constraint_record.convalidated"); count != 4 {
		t.Fatalf("validated constraint assertions = %d, want 4", count)
	}
}

func TestFontSchemaReadyFailsClosed(t *testing.T) {
	probeFailure := errors.New("probe failed")
	tests := []struct {
		name        string
		rows        []stubReadinessRow
		wantError   string
		wantWrapped error
	}{
		{name: "ready", rows: []stubReadinessRow{{ready: true}, {ready: true}, {ready: true}}},
		{
			name:      "schema contract false",
			rows:      []stubReadinessRow{{ready: false}},
			wantError: "font database migrations are not applied",
		},
		{
			name:        "probe error",
			rows:        []stubReadinessRow{{err: probeFailure}},
			wantError:   "check font schema: probe failed",
			wantWrapped: probeFailure,
		},
		{
			name:      "authoritative schema contract false",
			rows:      []stubReadinessRow{{ready: true}, {ready: false}},
			wantError: "font database migrations are not applied",
		},
		{
			name:        "authoritative schema probe error",
			rows:        []stubReadinessRow{{ready: true}, {err: probeFailure}},
			wantError:   "check authoritative font quota schema: probe failed",
			wantWrapped: probeFailure,
		},
		{
			name:      "quota ledger missing",
			rows:      []stubReadinessRow{{ready: true}, {ready: true}, {ready: false}},
			wantError: "font artifact quota ledger is missing or inconsistent",
		},
		{
			name:        "quota ledger probe error",
			rows:        []stubReadinessRow{{ready: true}, {ready: true}, {err: probeFailure}},
			wantError:   "check font artifact quota ledger: probe failed",
			wantWrapped: probeFailure,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queryer := &stubReadinessQueryer{rows: test.rows}
			err := fontSchemaReady(context.Background(), queryer)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("fontSchemaReady returned error: %v", err)
				}
			} else if err == nil || err.Error() != test.wantError {
				t.Fatalf("fontSchemaReady error = %v, want %q", err, test.wantError)
			}
			if test.wantWrapped != nil && !errors.Is(err, test.wantWrapped) {
				t.Fatalf("fontSchemaReady error = %v, want wrapped %v", err, test.wantWrapped)
			}
			if queryer.calls != len(test.rows) {
				t.Fatalf("QueryRow calls = %d, want %d", queryer.calls, len(test.rows))
			}
			if queryer.queries[0] != fontSchemaReadinessQuery {
				t.Fatal("fontSchemaReady did not execute the schema readiness query")
			}
			if len(test.rows) >= 2 {
				if queryer.queries[1] != fontSchemaSemanticReadinessQuery {
					t.Fatal("fontSchemaReady did not execute the authoritative schema query")
				}
				if len(queryer.args[1]) != 1 || queryer.args[1][0] != fontQuotaSchemaDefinitionJSON {
					t.Fatalf("authoritative schema args = %#v", queryer.args[1])
				}
			}
			if len(test.rows) == 3 && queryer.queries[2] != fontQuotaReadinessQuery {
				t.Fatal("fontSchemaReady did not execute the quota readiness query")
			}
			if len(queryer.args[0]) != 0 || len(test.rows) == 3 && len(queryer.args[2]) != 0 {
				t.Fatalf("unexpected readiness args = %#v", queryer.args)
			}
		})
	}
}

func TestFontSchemaReadyCurrentDatabaseIntegration(t *testing.T) {
	databaseURL := os.Getenv("EMFONT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EMFONT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := FontSchemaReady(ctx, pool); err != nil {
		t.Fatalf("FontSchemaReady: %v", err)
	}
}

func TestFontSchemaReadyMigration10Integration(t *testing.T) {
	databaseURL := os.Getenv("EMFONT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EMFONT_TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	controlConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse test database config: %v", err)
	}
	controlPool, err := pgxpool.NewWithConfig(ctx, controlConfig)
	if err != nil {
		t.Fatalf("open control database: %v", err)
	}
	t.Cleanup(controlPool.Close)

	databaseName := fmt.Sprintf("emfont_readiness_%x", time.Now().UnixNano())
	quotedDatabaseName := pgx.Identifier{databaseName}.Sanitize()
	if _, err := controlPool.Exec(ctx, "CREATE DATABASE "+quotedDatabaseName); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "42501" {
			t.Skipf("test database role cannot create the isolated readiness database: %v", err)
		}
		t.Fatalf("create isolated readiness database: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := controlPool.Exec(
			cleanupCtx,
			"DROP DATABASE "+quotedDatabaseName+" WITH (FORCE)",
		); err != nil {
			t.Errorf("drop isolated readiness database: %v", err)
		}
	})

	migrationConfig := controlConfig.ConnConfig.Copy()
	migrationConfig.Database = databaseName
	migrationDB := stdlib.OpenDB(*migrationConfig)
	t.Cleanup(func() {
		if err := migrationDB.Close(); err != nil {
			t.Errorf("close migration database: %v", err)
		}
	})
	if err := migrationDB.PingContext(ctx); err != nil {
		t.Fatalf("ping isolated migration database: %v", err)
	}

	readinessConfig := controlConfig.Copy()
	readinessConfig.ConnConfig.Database = databaseName
	readinessPool, err := pgxpool.NewWithConfig(ctx, readinessConfig)
	if err != nil {
		t.Fatalf("open isolated readiness database: %v", err)
	}
	t.Cleanup(readinessPool.Close)

	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("set goose dialect: %v", err)
	}
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate readiness integration test source")
	}
	migrationsDir := filepath.Clean(filepath.Join(filepath.Dir(filename), "../../../../db/migrations"))

	if err := goose.UpToContext(ctx, migrationDB, migrationsDir, 9); err != nil {
		t.Fatalf("migrate isolated database to version 9: %v", err)
	}
	if err := FontSchemaReady(ctx, readinessPool); err == nil || err.Error() != "font database migrations are not applied" {
		t.Fatalf("FontSchemaReady at migration 9 = %v, want schema not ready", err)
	}

	if err := goose.UpToContext(ctx, migrationDB, migrationsDir, 10); err != nil {
		t.Fatalf("migrate isolated database to version 10: %v", err)
	}
	if err := FontSchemaReady(ctx, readinessPool); err != nil {
		t.Fatalf("FontSchemaReady at migration 10: %v", err)
	}
	if _, err := readinessPool.Exec(ctx, `
		INSERT INTO font_family (id, name)
		VALUES ('readiness-family', 'Readiness Family')
	`); err != nil {
		t.Fatalf("insert readiness font family: %v", err)
	}
	if _, err := readinessPool.Exec(ctx, `
		INSERT INTO font_artifacts (
			artifact_key, kind, status, family_id, weight, builder_version,
			object_key, size_bytes, reservation_bytes
		)
		VALUES (
			'readiness-artifact', 'dynamic', 'ready', 'readiness-family', 400,
			'readiness-builder', '_generated/readiness.woff2', 321, 1024
		)
	`); err != nil {
		t.Fatalf("insert readiness font artifact: %v", err)
	}
	var artifactCount, accountedBytes int64
	if err := readinessPool.QueryRow(ctx, `
		SELECT artifact_count, accounted_bytes
		FROM font_artifact_quota
		WHERE singleton
	`).Scan(&artifactCount, &accountedBytes); err != nil {
		t.Fatalf("read nonzero quota ledger: %v", err)
	}
	if artifactCount != 1 || accountedBytes != 321 {
		t.Fatalf("quota ledger = (%d, %d), want (1, 321)", artifactCount, accountedBytes)
	}
	if err := FontSchemaReady(ctx, readinessPool); err != nil {
		t.Fatalf("FontSchemaReady with nonzero quota ledger: %v", err)
	}

	t.Run("terminal cache application privileges", func(t *testing.T) {
		tx, err := readinessPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin application privilege transaction: %v", err)
		}
		defer func() {
			if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
				t.Errorf("roll back application privilege transaction: %v", err)
			}
		}()

		roleName := fmt.Sprintf("emfont_readiness_role_%x", time.Now().UnixNano())
		quotedRoleName := pgx.Identifier{roleName}.Sanitize()
		statements := []string{
			"CREATE ROLE " + quotedRoleName + " NOLOGIN",
			"GRANT USAGE ON SCHEMA public TO " + quotedRoleName,
			"GRANT SELECT ON TABLE public.font_family, public.font_sources, public.version, public.static_fonts, public.system_metadata TO " + quotedRoleName,
			"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.font_artifacts TO " + quotedRoleName,
			"GRANT SELECT, INSERT, UPDATE ON TABLE public.font_build_jobs TO " + quotedRoleName,
			"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.font_terminal_failures TO " + quotedRoleName,
			"GRANT SELECT ON TABLE public.font_artifact_quota TO " + quotedRoleName,
			"GRANT UPDATE (singleton) ON TABLE public.font_artifact_quota TO " + quotedRoleName,
			"GRANT USAGE ON SEQUENCE public.font_build_jobs_id_seq, public.font_artifact_fence_seq TO " + quotedRoleName,
			"SET LOCAL ROLE " + quotedRoleName,
		}
		for _, statement := range statements {
			if _, err := tx.Exec(ctx, statement); err != nil {
				var postgresError *pgconn.PgError
				if errors.As(err, &postgresError) && postgresError.Code == "42501" {
					t.Skipf("test database role cannot create the readiness privilege role: %v", err)
				}
				t.Fatalf("configure application privilege role: %v", err)
			}
		}
		if err := fontSchemaReady(ctx, tx); err != nil {
			t.Fatalf("fontSchemaReady with application privileges: %v", err)
		}
		if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
			t.Fatalf("reset application privilege role: %v", err)
		}
		if _, err := tx.Exec(ctx, "REVOKE DELETE ON TABLE public.font_terminal_failures FROM "+quotedRoleName); err != nil {
			t.Fatalf("revoke terminal cache delete privilege: %v", err)
		}
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+quotedRoleName); err != nil {
			t.Fatalf("restore application privilege role: %v", err)
		}
		if err := fontSchemaReady(ctx, tx); err == nil || err.Error() != "font database migrations are not applied" {
			t.Fatalf("fontSchemaReady without terminal cache delete privilege = %v, want schema not ready", err)
		}
	})

	for _, privilege := range []string{"INSERT", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER"} {
		privilege := privilege
		t.Run("reject excess quota privilege "+strings.ToLower(privilege), func(t *testing.T) {
			tx, err := readinessPool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin excess privilege transaction: %v", err)
			}
			defer func() { _ = tx.Rollback(context.Background()) }()

			roleName := fmt.Sprintf("emfont_excess_privilege_%s_%x", strings.ToLower(privilege), time.Now().UnixNano())
			quotedRoleName := pgx.Identifier{roleName}.Sanitize()
			statements := []string{
				"CREATE ROLE " + quotedRoleName + " NOLOGIN",
				"GRANT USAGE ON SCHEMA public TO " + quotedRoleName,
				"GRANT SELECT ON TABLE public.font_family, public.font_sources, public.version, public.static_fonts, public.system_metadata TO " + quotedRoleName,
				"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.font_artifacts TO " + quotedRoleName,
				"GRANT SELECT, INSERT, UPDATE ON TABLE public.font_build_jobs TO " + quotedRoleName,
				"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.font_terminal_failures TO " + quotedRoleName,
				"GRANT SELECT ON TABLE public.font_artifact_quota TO " + quotedRoleName,
				"GRANT UPDATE (singleton) ON TABLE public.font_artifact_quota TO " + quotedRoleName,
				"GRANT USAGE ON SEQUENCE public.font_build_jobs_id_seq, public.font_artifact_fence_seq TO " + quotedRoleName,
				"GRANT " + privilege + " ON TABLE public.font_artifact_quota TO " + quotedRoleName,
				"SET LOCAL ROLE " + quotedRoleName,
			}
			for _, statement := range statements {
				if _, err := tx.Exec(ctx, statement); err != nil {
					var postgresError *pgconn.PgError
					if errors.As(err, &postgresError) && postgresError.Code == "42501" {
						t.Skipf("test database role cannot configure excess privilege role: %v", err)
					}
					t.Fatalf("configure excess privilege role: %v", err)
				}
			}
			if err := fontSchemaReady(ctx, tx); err == nil || err.Error() != "font database migrations are not applied" {
				t.Fatalf("fontSchemaReady with quota %s = %v", privilege, err)
			}
		})
	}

	t.Run("quota function owner drift", func(t *testing.T) {
		tx, err := readinessPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin function owner drift transaction: %v", err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()

		roleName := fmt.Sprintf("emfont_function_owner_%x", time.Now().UnixNano())
		quotedRoleName := pgx.Identifier{roleName}.Sanitize()
		if _, err := tx.Exec(ctx, "CREATE ROLE "+quotedRoleName+" NOLOGIN"); err != nil {
			var postgresError *pgconn.PgError
			if errors.As(err, &postgresError) && postgresError.Code == "42501" {
				t.Skipf("test database role cannot create a function owner: %v", err)
			}
			t.Fatalf("create drifted function owner: %v", err)
		}
		if _, err := tx.Exec(ctx,
			"ALTER FUNCTION account_font_artifact_quota_after_write() OWNER TO "+quotedRoleName,
		); err != nil {
			t.Fatalf("change quota function owner: %v", err)
		}
		if err := fontSchemaReady(ctx, tx); err == nil || err.Error() != "font database migrations are not applied" {
			t.Fatalf("fontSchemaReady with wrong function owner = %v", err)
		}
	})

	driftCases := []struct {
		name      string
		statement string
		wantError string
	}{
		{
			name: "quota generated expression semantic drift",
			statement: `DROP TRIGGER font_artifact_quota_account_update ON font_artifacts;
				ALTER TABLE font_artifacts DROP COLUMN quota_bytes;
				ALTER TABLE font_artifacts ADD COLUMN quota_bytes BIGINT GENERATED ALWAYS AS (
					CASE WHEN status IN ('pending', 'running', 'missing')
						THEN reservation_bytes + 1 ELSE size_bytes END
				) STORED;
				CREATE TRIGGER font_artifact_quota_account_update
				AFTER UPDATE OF status, size_bytes, reservation_bytes ON font_artifacts
				FOR EACH ROW
				WHEN (OLD.quota_bytes IS DISTINCT FROM NEW.quota_bytes)
				EXECUTE FUNCTION account_font_artifact_quota_after_write()`,
		},
		{
			name: "quota accounting function body drift",
			statement: `CREATE OR REPLACE FUNCTION account_font_artifact_quota_after_write()
				RETURNS TRIGGER
				LANGUAGE plpgsql
				SECURITY DEFINER
				SET search_path = pg_catalog, public
				AS $$ BEGIN RETURN NEW; END; $$`,
		},
		{
			name: "quota trigger relinked",
			statement: `DROP TRIGGER font_artifact_quota_account_insert ON font_artifacts;
				CREATE FUNCTION unexpected_quota_accounting_function()
				RETURNS TRIGGER
				LANGUAGE plpgsql
				AS $$ BEGIN RETURN NEW; END; $$;
				CREATE TRIGGER font_artifact_quota_account_insert
				AFTER INSERT ON font_artifacts
				FOR EACH ROW
				EXECUTE FUNCTION unexpected_quota_accounting_function()`,
		},
		{
			name: "quota function search path drift",
			statement: `ALTER FUNCTION lock_font_artifact_quota_before_write()
				SET search_path = public`,
		},
		{
			name:      "terminal cache table missing",
			statement: "DROP TABLE font_terminal_failures",
		},
		{
			name:      "terminal cache column drift",
			statement: "ALTER TABLE font_terminal_failures ALTER COLUMN cached_at DROP NOT NULL",
		},
		{
			name:      "terminal cache constraint missing",
			statement: "ALTER TABLE font_terminal_failures DROP CONSTRAINT font_terminal_failures_kind_check",
		},
		{
			name: "terminal cache eviction index drift",
			statement: `DROP INDEX font_terminal_failures_eviction_idx;
				CREATE INDEX font_terminal_failures_eviction_idx
				ON font_terminal_failures (artifact_key, cached_at)`,
		},
		{
			name:      "unexpected quota index",
			statement: "CREATE INDEX unexpected_quota_index ON font_artifact_quota (artifact_count)",
		},
		{
			name: "wrong type",
			statement: `DROP TRIGGER font_artifact_quota_lock_update ON font_artifacts;
				DROP TRIGGER font_artifact_quota_account_update ON font_artifacts;
				ALTER TABLE font_artifacts DROP COLUMN quota_bytes;
				ALTER TABLE font_artifacts ALTER COLUMN reservation_bytes TYPE NUMERIC`,
		},
		{
			name:      "nullable",
			statement: "ALTER TABLE font_artifacts ALTER COLUMN reservation_bytes DROP NOT NULL",
		},
		{
			name:      "wrong default",
			statement: "ALTER TABLE font_artifacts ALTER COLUMN reservation_bytes SET DEFAULT 1",
		},
		{
			name: "unvalidated positive constraint",
			statement: `ALTER TABLE font_artifacts
				DROP CONSTRAINT font_artifacts_reservation_bytes_positive,
				ADD CONSTRAINT font_artifacts_reservation_bytes_positive
				CHECK (reservation_bytes > 0) NOT VALID`,
		},
		{
			name: "wrong size constraint",
			statement: `ALTER TABLE font_artifacts
				DROP CONSTRAINT font_artifacts_size_within_reservation,
				ADD CONSTRAINT font_artifacts_size_within_reservation
					CHECK (size_bytes < reservation_bytes)`,
		},
		{
			name:      "quota trigger disabled",
			statement: "ALTER TABLE font_artifacts DISABLE TRIGGER font_artifact_quota_account_update",
		},
		{
			name:      "quota singleton missing",
			statement: "DELETE FROM font_artifact_quota WHERE singleton",
			wantError: "font artifact quota ledger is missing or inconsistent",
		},
		{
			name:      "quota artifact count mismatch",
			statement: "UPDATE font_artifact_quota SET artifact_count = artifact_count + 1 WHERE singleton",
			wantError: "font artifact quota ledger is missing or inconsistent",
		},
		{
			name:      "quota accounted bytes mismatch",
			statement: "UPDATE font_artifact_quota SET accounted_bytes = accounted_bytes + 1 WHERE singleton",
			wantError: "font artifact quota ledger is missing or inconsistent",
		},
		{
			name: "quota column no longer generated",
			statement: `DROP TRIGGER font_artifact_quota_account_update ON font_artifacts;
				ALTER TABLE font_artifacts DROP COLUMN quota_bytes;
				ALTER TABLE font_artifacts ADD COLUMN quota_bytes BIGINT`,
		},
	}
	for _, drift := range driftCases {
		t.Run(drift.name, func(t *testing.T) {
			tx, err := readinessPool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin schema drift transaction: %v", err)
			}
			defer func() {
				if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
					t.Errorf("roll back schema drift transaction: %v", err)
				}
			}()

			if _, err := tx.Exec(ctx, drift.statement); err != nil {
				t.Fatalf("introduce schema drift: %v", err)
			}
			wantError := drift.wantError
			if wantError == "" {
				wantError = "font database migrations are not applied"
			}
			if err := fontSchemaReady(ctx, tx); err == nil || err.Error() != wantError {
				t.Fatalf("fontSchemaReady for drifted migration 10 schema = %v, want %q", err, wantError)
			}
		})
	}
}

type stubReadinessQueryer struct {
	rows    []stubReadinessRow
	queries []string
	args    [][]any
	calls   int
}

func (queryer *stubReadinessQueryer) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	index := queryer.calls
	queryer.calls++
	queryer.queries = append(queryer.queries, query)
	queryer.args = append(queryer.args, args)
	if index >= len(queryer.rows) {
		return stubReadinessRow{err: errors.New("unexpected readiness query")}
	}
	return queryer.rows[index]
}

type stubReadinessRow struct {
	ready bool
	err   error
}

func (row stubReadinessRow) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != 1 {
		return fmt.Errorf("scan destinations = %d, want 1", len(destinations))
	}
	ready, ok := destinations[0].(*bool)
	if !ok {
		return fmt.Errorf("scan destination = %T, want *bool", destinations[0])
	}
	*ready = row.ready
	return nil
}
