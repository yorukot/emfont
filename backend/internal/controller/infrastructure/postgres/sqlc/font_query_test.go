package sqlc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFontArtifactAdmissionUsesTransactionalQuotaLedger(t *testing.T) {
	queries := map[string]string{
		"lock":    lockFontArtifactAdmission,
		"create":  createFontArtifact,
		"acquire": acquireFontBuildJob,
		"missing": markFontArtifactMissing,
	}
	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(query, "pg_advisory") {
				t.Fatal("query still uses an advisory lock")
			}
			if !strings.Contains(query, "FROM font_artifact_quota") || !strings.Contains(query, "FOR UPDATE") {
				t.Fatal("query does not lock the transactional quota ledger")
			}
		})
	}
	if strings.Contains(createFontArtifact, "artifact_usage") || strings.Contains(createFontArtifact, "SUM(") {
		t.Fatal("cold admission still aggregates artifact rows")
	}
}

func TestTerminalFailureQueriesAreBoundedFencedAndQuotaNeutral(t *testing.T) {
	for _, fragment := range []string{
		"FROM font_artifact_quota",
		"job.locked_by = $2",
		"job.fence = $3",
		"job.lease_until >= now()",
		"FOR UPDATE OF job",
		"FOR UPDATE OF artifact",
		"COUNT(*) - $4::bigint + 1",
		"ORDER BY terminal_failure.cached_at, terminal_failure.artifact_key",
		"INSERT INTO font_terminal_failures",
		"DELETE FROM font_artifacts AS artifact",
	} {
		if !strings.Contains(failFontBuildJobTerminal, fragment) {
			t.Fatalf("terminal transition query is missing %q", fragment)
		}
	}
	lockOrder := []string{
		"quota AS MATERIALIZED",
		"claimed_job AS MATERIALIZED",
		"candidate AS MATERIALIZED",
		"eviction_candidates AS MATERIALIZED",
		"cached AS",
		"DELETE FROM font_artifacts AS artifact",
	}
	previous := -1
	for _, fragment := range lockOrder {
		index := strings.Index(failFontBuildJobTerminal, fragment)
		if index <= previous {
			t.Fatalf("terminal transition lock/order fragment %q at %d after %d", fragment, index, previous)
		}
		previous = index
	}
	for _, fragment := range []string{
		"FROM font_terminal_failures AS terminal_failure",
		"FOR UPDATE OF terminal_failure",
		"WHEN cached_failure.artifact_key IS NOT NULL THEN 'terminal'",
	} {
		if !strings.Contains(createFontArtifact, fragment) {
			t.Fatalf("artifact creation query is missing terminal-race guard %q", fragment)
		}
	}
	if !strings.Contains(getFontArtifact, "UNION ALL") ||
		!strings.Contains(getFontArtifact, "FROM font_terminal_failures AS terminal_failure") {
		t.Fatal("artifact lookup does not surface terminal-cache entries")
	}
}

func TestFontBuildRetryAfterUsesStateSpecificDeadline(t *testing.T) {
	for _, fragment := range []string{
		"WHEN status = 'running' THEN lease_until",
		"WHEN status = 'failed' THEN next_attempt_at",
	} {
		if !strings.Contains(getFontBuildRetryAfterSeconds, fragment) {
			t.Fatalf("retry-after query is missing %q", fragment)
		}
	}
}

func TestFontArtifactColdAdmissionPlanIntegration(t *testing.T) {
	databaseURL := os.Getenv("EMFONT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EMFONT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(pool.Close)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin plan transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "quota-plan-" + suffix
	if _, err := tx.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::smallint[], 'ttf')`, familyID, "Quota Plan "+suffix); err != nil {
		t.Fatalf("insert plan family: %v", err)
	}

	var artifactRows int64
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM font_artifacts").Scan(&artifactRows); err != nil {
		t.Fatalf("count plan fixture rows: %v", err)
	}
	artifactKey := "quota-plan:" + suffix
	var rawPlan []byte
	err = tx.QueryRow(ctx, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+createFontArtifact,
		artifactKey,
		"dynamic",
		familyID,
		int16(400),
		0,
		"",
		suffix,
		"A",
		"source-"+suffix,
		"quota-plan",
		"v1",
		"font/woff2",
		int64(1024),
		int64(1<<62),
		int64(1_000_000),
		"pending",
		"_generated/quota-plan-"+suffix+".woff2",
	).Scan(&rawPlan)
	if err != nil {
		t.Fatalf("explain cold admission: %v", err)
	}
	var documents []map[string]any
	if err := json.Unmarshal(rawPlan, &documents); err != nil {
		t.Fatalf("decode admission plan: %v", err)
	}
	if len(documents) != 1 {
		t.Fatalf("admission plan documents = %d, want 1", len(documents))
	}
	if node, found := findPlanNode(documents[0], func(node map[string]any) bool {
		return node["Relation Name"] == "font_artifacts" && node["Node Type"] == "Seq Scan"
	}); found && artifactRows >= 100_000 {
		t.Fatalf("cold admission scanned font_artifacts sequentially: %v", node)
	}
	t.Logf(
		"cold admission over %d artifact rows: planning=%vms execution=%vms",
		artifactRows, documents[0]["Planning Time"], documents[0]["Execution Time"],
	)
}

func findPlanNode(value any, predicate func(map[string]any) bool) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if predicate(typed) {
			return typed, true
		}
		for _, child := range typed {
			if node, found := findPlanNode(child, predicate); found {
				return node, true
			}
		}
	case []any:
		for _, child := range typed {
			if node, found := findPlanNode(child, predicate); found {
				return node, true
			}
		}
	}
	return nil, false
}

func BenchmarkFontArtifactColdAdmission(b *testing.B) {
	if os.Getenv("EMFONT_QUOTA_BENCHMARK") != "1" {
		b.Skip("EMFONT_QUOTA_BENCHMARK is not set to 1")
	}
	databaseURL := os.Getenv("EMFONT_TEST_DATABASE_URL")
	if databaseURL == "" {
		b.Skip("EMFONT_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		b.Fatalf("open database: %v", err)
	}
	b.Cleanup(pool.Close)
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "quota-benchmark-" + suffix
	if _, err := pool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::smallint[], 'ttf')`, familyID, "Quota Benchmark "+suffix); err != nil {
		b.Fatalf("insert benchmark family: %v", err)
	}
	b.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID); err != nil {
			b.Errorf("delete benchmark family: %v", err)
		}
	})

	var sequence atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(parallel *testing.PB) {
		for parallel.Next() {
			value := sequence.Add(1)
			artifactKey := fmt.Sprintf("quota-benchmark:%s:%d", suffix, value)
			tx, err := pool.Begin(ctx)
			if err != nil {
				b.Errorf("begin admission transaction: %v", err)
				return
			}
			queries := New(tx)
			if err := queries.LockFontArtifactAdmission(ctx); err != nil {
				_ = tx.Rollback(ctx)
				b.Errorf("lock quota ledger: %v", err)
				return
			}
			result, err := queries.CreateFontArtifact(ctx, CreateFontArtifactParams{
				ArtifactKey: artifactKey, Kind: "dynamic", Status: "pending", FamilyID: familyID,
				Weight: 400, Version: 0, Pack: "", WordHash: strconv.FormatInt(value, 10),
				NormalizedWordSet: "A", SourceChecksumSha256: "source-" + suffix,
				BuilderVersion: "quota-benchmark", ArtifactProtocolVersion: "v1",
				ObjectKey: "_generated/" + artifactKey + ".woff2", ContentType: "font/woff2",
				ArtifactReservation: 1024, MaxAccountedBytes: 1 << 62, MaxArtifacts: 1_000_000,
			})
			if err != nil || result != "admitted" {
				_ = tx.Rollback(ctx)
				b.Errorf("cold admission = %q, %v", result, err)
				return
			}
			if err := tx.Commit(ctx); err != nil {
				b.Errorf("commit admission: %v", err)
				return
			}
		}
	})
}
