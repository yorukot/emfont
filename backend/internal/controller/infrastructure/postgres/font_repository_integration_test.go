package postgres

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	domain "github.com/emfont/emfont/backend/internal/domain/font"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFontRepositoryApplicationRoleWorkflowIntegration(t *testing.T) {
	databaseURL := os.Getenv("EMFONT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EMFONT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open administration pool: %v", err)
	}
	t.Cleanup(adminPool.Close)
	if err := FontSchemaReady(ctx, adminPool); err != nil {
		t.Fatalf("FontSchemaReady as migration owner: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	roleName := "emfont_app_workflow_" + suffix
	quotedRoleName := pgx.Identifier{roleName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE ROLE "+quotedRoleName+" NOLOGIN"); err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "42501" {
			t.Skipf("test database role cannot create the application role: %v", err)
		}
		t.Fatalf("create application role: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = adminPool.Exec(cleanupCtx, "DROP OWNED BY "+quotedRoleName)
		if _, err := adminPool.Exec(cleanupCtx, "DROP ROLE "+quotedRoleName); err != nil {
			t.Errorf("drop application role: %v", err)
		}
	})

	grants := []string{
		"GRANT USAGE ON SCHEMA public TO " + quotedRoleName,
		"GRANT SELECT ON TABLE public.font_family, public.font_sources, public.version, public.static_fonts, public.system_metadata TO " + quotedRoleName,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.font_artifacts TO " + quotedRoleName,
		"GRANT SELECT, INSERT, UPDATE ON TABLE public.font_build_jobs TO " + quotedRoleName,
		"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.font_terminal_failures TO " + quotedRoleName,
		"GRANT SELECT ON TABLE public.font_artifact_quota TO " + quotedRoleName,
		"GRANT UPDATE (singleton) ON TABLE public.font_artifact_quota TO " + quotedRoleName,
		"GRANT USAGE, SELECT ON SEQUENCE public.font_build_jobs_id_seq, public.font_artifact_fence_seq TO " + quotedRoleName,
	}
	for _, grant := range grants {
		if _, err := adminPool.Exec(ctx, grant); err != nil {
			t.Fatalf("grant application role privileges: %v", err)
		}
	}

	familyID := "app-role-workflow-" + suffix
	if _, err := adminPool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`, familyID, "App Role Workflow "+suffix); err != nil {
		t.Fatalf("insert application workflow family: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID); err != nil {
			t.Errorf("delete application workflow family: %v", err)
		}
	})

	appConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatalf("parse application pool config: %v", err)
	}
	appConfig.AfterConnect = func(ctx context.Context, connection *pgx.Conn) error {
		_, err := connection.Exec(ctx, "SET ROLE "+quotedRoleName)
		return err
	}
	appPool, err := pgxpool.NewWithConfig(ctx, appConfig)
	if err != nil {
		t.Fatalf("open application role pool: %v", err)
	}
	t.Cleanup(appPool.Close)
	var currentUser string
	if err := appPool.QueryRow(ctx, "SELECT current_user").Scan(&currentUser); err != nil {
		t.Fatalf("read application current_user: %v", err)
	}
	if currentUser != roleName {
		t.Fatalf("application current_user = %q, want %q", currentUser, roleName)
	}
	if err := FontSchemaReady(ctx, appPool); err != nil {
		t.Fatalf("FontSchemaReady as application role: %v", err)
	}
	var terminalFailureBaseline int64
	if err := adminPool.QueryRow(ctx, "SELECT COUNT(*)::bigint FROM font_terminal_failures").Scan(&terminalFailureBaseline); err != nil {
		t.Fatalf("read terminal failure baseline: %v", err)
	}

	repository := NewFontRepositoryFromPool(appPool, FontRepositoryConfig{
		MaxArtifacts: 1 << 30, MaxAccountedBytes: 1 << 62,
		ArtifactReservation: 1024, MaxTerminalFailures: terminalFailureBaseline + 10,
	})
	artifact := func(label string) domain.Artifact {
		return domain.Artifact{
			Key: "app-role:" + suffix + ":" + label, Kind: domain.BuildModeDynamic, Status: "pending",
			FamilyID: familyID, Weight: 400, WordHash: label, NormalizedWordSet: label,
			SourceChecksum: "app-role-source-" + suffix, BuilderVersion: "app-role-builder",
			ProtocolVersion: domain.ArtifactProtocolVersion,
			ObjectKey:       "_generated/app-role-" + suffix + "-" + label + ".woff2",
			ContentType:     domain.ContentTypeWOFF2,
		}
	}

	terminal := artifact("terminal")
	if err := repository.CreateFontArtifact(ctx, terminal); err != nil {
		t.Fatalf("application role create terminal artifact: %v", err)
	}
	terminalClaim, acquired, err := repository.AcquireBuildJob(ctx, terminal.Key, "app-role-terminal", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("application role acquire terminal artifact = %t, %v", acquired, err)
	}
	if err := repository.FailBuildJobTerminal(
		ctx, terminal.Key, terminalClaim, domain.FailureCodeUnsupportedCodepoints, "unsupported",
	); err != nil {
		t.Fatalf("application role terminal transition: %v", err)
	}
	cached, err := repository.GetFontArtifact(ctx, terminal.Key)
	if err != nil || cached.Status != "failed" || cached.FailureCode != domain.FailureCodeUnsupportedCodepoints {
		t.Fatalf("application role cached terminal artifact = %#v, %v", cached, err)
	}
	if err := repository.CreateFontArtifact(ctx, terminal); !errors.Is(err, domain.ErrTerminalFailureCached) {
		t.Fatalf("application role terminal admission error = %v", err)
	}

	ready := artifact("ready")
	if err := repository.CreateFontArtifact(ctx, ready); err != nil {
		t.Fatalf("application role create ready artifact: %v", err)
	}
	readyClaim, acquired, err := repository.AcquireBuildJob(ctx, ready.Key, "app-role-ready", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("application role acquire ready artifact = %t, %v", acquired, err)
	}
	if err := repository.MarkFontArtifactReady(ctx, ready.Key, readyClaim, domain.ArtifactObject{
		ObjectKey: ready.ObjectKey + ".published", VersionID: "app-role-version",
		SizeBytes: 128, ETag: "app-role-etag", ChecksumSHA256: strings.Repeat("a", 64),
	}); err != nil {
		t.Fatalf("application role publish ready artifact: %v", err)
	}
	if err := FontSchemaReady(ctx, appPool); err != nil {
		t.Fatalf("FontSchemaReady after application workflow: %v", err)
	}
}

func TestFontRepositoryBuildLeaseIntegration(t *testing.T) {
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

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "integration-" + suffix
	if _, err := pool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`, familyID, "Integration "+suffix); err != nil {
		t.Fatalf("insert font family: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID); err != nil {
			t.Errorf("cleanup font family: %v", err)
		}
	})
	if _, err := pool.Exec(ctx, `
		INSERT INTO font_sources (family_id, weight, format, object_key, checksum_sha256)
		VALUES ($1, 400, 'ttf', $2, 'source-checksum')`, familyID, "original-fonts/"+familyID+"/400.ttf"); err != nil {
		t.Fatalf("insert font source: %v", err)
	}

	repository := NewFontRepositoryFromPool(pool)
	pageIDs := []string{"page-a-" + suffix, "page-b-" + suffix, "page-c-" + suffix}
	for index, pageID := range pageIDs {
		if _, err := pool.Exec(ctx, `
			INSERT INTO font_family (id, name, weights, format)
			VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`,
			pageID, "Pagination "+suffix+" "+strconv.Itoa(index)); err != nil {
			t.Fatalf("insert pagination family %d: %v", index, err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = ANY($1::text[])", pageIDs); err != nil {
			t.Errorf("cleanup paginated font families: %v", err)
		}
	})
	firstPage, err := repository.ListFontFamilies(ctx, "Pagination "+suffix, "", 2)
	if err != nil {
		t.Fatalf("ListFontFamilies first page: %v", err)
	}
	if len(firstPage) != 2 || firstPage[0].ID != pageIDs[0] || firstPage[1].ID != pageIDs[1] {
		t.Fatalf("first font page = %#v", firstPage)
	}
	secondPage, err := repository.ListFontFamilies(ctx, "Pagination "+suffix, firstPage[1].ID, 2)
	if err != nil {
		t.Fatalf("ListFontFamilies second page: %v", err)
	}
	if len(secondPage) != 1 || secondPage[0].ID != pageIDs[2] {
		t.Fatalf("second font page = %#v", secondPage)
	}
	family, err := repository.GetFontFamily(ctx, familyID)
	if err != nil {
		t.Fatalf("GetFontFamily: %v", err)
	}
	if len(family.Weights) != 1 || family.Weights[0] != 400 {
		t.Fatalf("family weights = %#v", family.Weights)
	}
	source, err := repository.GetFontSource(ctx, familyID, 400)
	if err != nil {
		t.Fatalf("GetFontSource: %v", err)
	}
	packCharA := "pack-a-" + suffix
	packCharB := "pack-b-" + suffix
	packCharC := "pack-c-" + suffix
	if _, err := pool.Exec(ctx, `
		INSERT INTO static_fonts (char, pack, families)
		VALUES ($1, 7, ARRAY[$3]::text[]), ($2, 7, ARRAY[$3]::text[])`,
		packCharA, packCharB, familyID); err != nil {
		t.Fatalf("insert static pack: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(
			cleanupCtx,
			"DELETE FROM static_fonts WHERE char = ANY($1::text[])",
			[]string{packCharA, packCharB, packCharC},
		); err != nil {
			t.Errorf("cleanup static font rows: %v", err)
		}
	})
	firstSnapshot, err := repository.GetStaticPackSnapshot(ctx, familyID, []string{packCharA})
	if err != nil || len(firstSnapshot) != 1 || firstSnapshot[0].Number != 7 || !firstSnapshot[0].CoverageComplete {
		t.Fatalf("first static snapshot = %#v, %v", firstSnapshot, err)
	}
	partialSnapshot, err := repository.GetStaticPackSnapshot(ctx, familyID, []string{packCharA, "unmapped-" + suffix})
	if err != nil || len(partialSnapshot) != 1 || partialSnapshot[0].CoverageComplete {
		t.Fatalf("partial static snapshot = %#v, %v", partialSnapshot, err)
	}
	firstPackHash := domain.StaticWordHash(firstSnapshot[0].Characters)
	if _, err := pool.Exec(ctx, "UPDATE static_fonts SET char = $1 WHERE char = $2", packCharC, packCharB); err != nil {
		t.Fatalf("mutate static pack without version bump: %v", err)
	}
	secondSnapshot, err := repository.GetStaticPackSnapshot(ctx, familyID, []string{packCharA})
	if err != nil || len(secondSnapshot) != 1 {
		t.Fatalf("second static snapshot = %#v, %v", secondSnapshot, err)
	}
	if secondSnapshot[0].Version != firstSnapshot[0].Version {
		t.Fatalf("static version changed from %d to %d", firstSnapshot[0].Version, secondSnapshot[0].Version)
	}
	if secondPackHash := domain.StaticWordHash(secondSnapshot[0].Characters); secondPackHash == firstPackHash {
		t.Fatalf("static pack hash did not change: %s", secondPackHash)
	}

	artifact := domain.Artifact{
		Key: "dynamic:" + suffix, Kind: domain.BuildModeDynamic, Status: "pending",
		FamilyID: familyID, Weight: 400, WordHash: suffix, NormalizedWordSet: "AB",
		SourceChecksum: source.ChecksumSHA256, BuilderVersion: domain.DefaultBuilderVersion,
		ProtocolVersion: domain.ArtifactProtocolVersion,
		ObjectKey:       "_generated/" + suffix + ".woff2", ContentType: domain.ContentTypeWOFF2,
	}
	if err := repository.CreateFontArtifact(ctx, artifact); err != nil {
		t.Fatalf("CreateFontArtifact: %v", err)
	}
	compatibleArtifact := artifact
	compatibleArtifact.ObjectKey = "_generated/another-unpublished-base.woff2"
	if err := repository.CreateFontArtifact(ctx, compatibleArtifact); err != nil {
		t.Fatalf("compatible CreateFontArtifact: %v", err)
	}
	conflictingArtifact := artifact
	conflictingArtifact.NormalizedWordSet = "different-input"
	if err := repository.CreateFontArtifact(ctx, conflictingArtifact); !errors.Is(err, domain.ErrArtifactConflict) {
		t.Fatalf("conflicting CreateFontArtifact error = %v, want ErrArtifactConflict", err)
	}
	claimA, acquired, err := repository.AcquireBuildJob(ctx, artifact.Key, "worker-a:lease-1", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first AcquireBuildJob = %v, %v; want true, nil", acquired, err)
	}
	_, acquired, err = repository.AcquireBuildJob(ctx, artifact.Key, "worker-b:lease-2", time.Minute)
	if err != nil || acquired {
		t.Fatalf("second AcquireBuildJob = %v, %v; want false, nil", acquired, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE font_build_jobs
		SET lease_until = now() + interval '120 seconds',
			next_attempt_at = now() + interval '300 seconds'
		WHERE artifact_key = $1`, artifact.Key); err != nil {
		t.Fatalf("set distinct running retry timestamps: %v", err)
	}
	runningRetryAfter, err := repository.BuildRetryAfter(ctx, artifact.Key)
	if err != nil || runningRetryAfter < 110*time.Second || runningRetryAfter > 120*time.Second {
		t.Fatalf("running BuildRetryAfter = %s, %v; want lease delay near 120s", runningRetryAfter, err)
	}
	wrongOwner := domain.BuildClaim{Owner: "worker-b:lease-2", Fence: claimA.Fence}
	if err := repository.MarkFontArtifactReady(ctx, artifact.Key, wrongOwner, domain.ArtifactObject{SizeBytes: 1}); !errors.Is(err, domain.ErrBuildNotReady) {
		t.Fatalf("wrong-owner MarkFontArtifactReady error = %v, want ErrBuildNotReady", err)
	}
	publishedKey := "_generated/" + suffix + "-first.woff2"
	if err := repository.MarkFontArtifactReady(ctx, artifact.Key, claimA, domain.ArtifactObject{
		ObjectKey: publishedKey, VersionID: "version-first", SizeBytes: 123,
		ETag: "etag", ChecksumSHA256: "artifact-checksum",
	}); err != nil {
		t.Fatalf("MarkFontArtifactReady: %v", err)
	}
	stored, err := repository.GetFontArtifact(ctx, artifact.Key)
	if err != nil {
		t.Fatalf("GetFontArtifact: %v", err)
	}
	if stored.Status != "ready" || stored.SizeBytes != 123 || stored.ObjectKey != publishedKey ||
		stored.ObjectVersionID != "version-first" || stored.Generation != claimA.Fence {
		t.Fatalf("stored artifact = %#v", stored)
	}
	marked, err := repository.MarkFontArtifactMissing(
		ctx, artifact.Key, artifact.ObjectKey, stored.Generation, "stale observer",
	)
	if err != nil || marked {
		t.Fatalf("stale MarkFontArtifactMissing = %v, %v; want false, nil", marked, err)
	}
	marked, err = repository.MarkFontArtifactMissing(
		ctx, artifact.Key, stored.ObjectKey, stored.Generation, "integration repair",
	)
	if err != nil || !marked {
		t.Fatalf("MarkFontArtifactMissing = %v, %v; want true, nil", marked, err)
	}
	claimC, acquired, err := repository.AcquireBuildJob(ctx, artifact.Key, "worker-shared", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("repair AcquireBuildJob = %v, %v; want true, nil", acquired, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE font_build_jobs SET lease_until = now() - interval '1 second'
		WHERE artifact_key = $1`, artifact.Key); err != nil {
		t.Fatalf("expire build lease: %v", err)
	}
	claimD, acquired, err := repository.AcquireBuildJob(ctx, artifact.Key, "worker-shared", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("takeover AcquireBuildJob = %v, %v; want true, nil", acquired, err)
	}
	if claimD.Fence <= claimC.Fence {
		t.Fatalf("takeover fence = %d, previous fence = %d", claimD.Fence, claimC.Fence)
	}
	if err := repository.MarkFontArtifactReady(ctx, artifact.Key, claimC, domain.ArtifactObject{
		ObjectKey: "_generated/" + suffix + "-late.woff2", SizeBytes: 111,
	}); !errors.Is(err, domain.ErrBuildNotReady) {
		t.Fatalf("late owner MarkFontArtifactReady error = %v, want ErrBuildNotReady", err)
	}
	winningKey := "_generated/" + suffix + "-winner.woff2"
	if err := repository.MarkFontArtifactReady(ctx, artifact.Key, claimD, domain.ArtifactObject{
		ObjectKey: winningKey, VersionID: "version-winner", SizeBytes: 222,
		ETag: "winner-etag", ChecksumSHA256: "winner-checksum",
	}); err != nil {
		t.Fatalf("takeover MarkFontArtifactReady: %v", err)
	}
	stored, err = repository.GetFontArtifact(ctx, artifact.Key)
	if err != nil {
		t.Fatalf("GetFontArtifact after takeover: %v", err)
	}
	if stored.ObjectKey != winningKey || stored.ObjectVersionID != "version-winner" ||
		stored.Generation != claimD.Fence || stored.SizeBytes != 222 {
		t.Fatalf("artifact after late publish = %#v", stored)
	}

	retryArtifact := artifact
	retryArtifact.Key = "dynamic:retry:" + suffix
	retryArtifact.WordHash = "retry-" + suffix
	retryArtifact.ObjectKey = "_generated/retry-" + suffix + ".woff2"
	if err := repository.CreateFontArtifact(ctx, retryArtifact); err != nil {
		t.Fatalf("CreateFontArtifact for retry: %v", err)
	}
	retryClaim, acquired, err := repository.AcquireBuildJob(ctx, retryArtifact.Key, "worker-retry:first", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("retry AcquireBuildJob = %v, %v; want true, nil", acquired, err)
	}
	if err := repository.FailBuildJob(ctx, retryArtifact.Key, retryClaim, "expected failure"); err != nil {
		t.Fatalf("FailBuildJob: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE font_build_jobs
		SET lease_until = now() + interval '250 seconds',
			next_attempt_at = now() + interval '7 seconds'
		WHERE artifact_key = $1`, retryArtifact.Key); err != nil {
		t.Fatalf("set distinct failed retry timestamps: %v", err)
	}
	retryAfter, err := repository.BuildRetryAfter(ctx, retryArtifact.Key)
	if err != nil || retryAfter < 5*time.Second || retryAfter > 7*time.Second {
		t.Fatalf("failed BuildRetryAfter = %s, %v; want next-attempt delay near 7s", retryAfter, err)
	}
	_, acquired, err = repository.AcquireBuildJob(ctx, retryArtifact.Key, "worker-retry:too-soon", time.Minute)
	if err != nil || acquired {
		t.Fatalf("backoff AcquireBuildJob = %v, %v; want false, nil", acquired, err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE font_build_jobs SET next_attempt_at = now() - interval '1 second'
		WHERE artifact_key = $1`, retryArtifact.Key); err != nil {
		t.Fatalf("expire retry backoff: %v", err)
	}
	_, acquired, err = repository.AcquireBuildJob(ctx, retryArtifact.Key, "worker-retry:after-backoff", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("post-backoff AcquireBuildJob = %v, %v; want true, nil", acquired, err)
	}

	terminalArtifact := artifact
	terminalArtifact.Key = "dynamic:terminal:" + suffix
	terminalArtifact.WordHash = "terminal-" + suffix
	terminalArtifact.ObjectKey = "_generated/terminal-" + suffix + ".woff2"
	if err := repository.CreateFontArtifact(ctx, terminalArtifact); err != nil {
		t.Fatalf("CreateFontArtifact for terminal failure: %v", err)
	}
	terminalClaim, acquired, err := repository.AcquireBuildJob(
		ctx, terminalArtifact.Key, "worker-terminal:first", time.Minute,
	)
	if err != nil || !acquired {
		t.Fatalf("terminal AcquireBuildJob = %v, %v; want true, nil", acquired, err)
	}
	if err := repository.FailBuildJobTerminal(
		ctx,
		terminalArtifact.Key,
		terminalClaim,
		domain.FailureCodeUnsupportedCodepoints,
		"unsupported codepoint",
	); err != nil {
		t.Fatalf("FailBuildJobTerminal: %v", err)
	}
	storedTerminal, err := repository.GetFontArtifact(ctx, terminalArtifact.Key)
	if err != nil {
		t.Fatalf("GetFontArtifact terminal failure: %v", err)
	}
	if storedTerminal.Status != "failed" || storedTerminal.FailureCode != domain.FailureCodeUnsupportedCodepoints {
		t.Fatalf("terminal artifact = %#v", storedTerminal)
	}
	if _, acquired, err := repository.AcquireBuildJob(
		ctx, terminalArtifact.Key, "worker-terminal:retry", time.Minute,
	); err != nil || acquired {
		t.Fatalf("terminal retry AcquireBuildJob = %v, %v; want false, nil", acquired, err)
	}
	var activeArtifacts, activeJobs, terminalFailures int
	if err := pool.QueryRow(ctx, `
			SELECT
				(SELECT COUNT(*) FROM font_artifacts WHERE artifact_key = $1),
				(SELECT COUNT(*) FROM font_build_jobs WHERE artifact_key = $1),
				(SELECT COUNT(*) FROM font_terminal_failures WHERE artifact_key = $1)`,
		terminalArtifact.Key,
	).Scan(&activeArtifacts, &activeJobs, &terminalFailures); err != nil {
		t.Fatalf("read terminal cache state: %v", err)
	}
	if activeArtifacts != 0 || activeJobs != 0 || terminalFailures != 1 {
		t.Fatalf("terminal cache state = artifacts %d, jobs %d, failures %d", activeArtifacts, activeJobs, terminalFailures)
	}
	if err := repository.CreateFontArtifact(ctx, terminalArtifact); !errors.Is(err, domain.ErrTerminalFailureCached) {
		t.Fatalf("cached terminal CreateFontArtifact error = %v, want ErrTerminalFailureCached", err)
	}
	if _, err := pool.Exec(ctx, "DELETE FROM font_terminal_failures WHERE artifact_key = $1", terminalArtifact.Key); err != nil {
		t.Fatalf("evict terminal failure: %v", err)
	}
	if err := repository.CreateFontArtifact(ctx, terminalArtifact); err != nil {
		t.Fatalf("recreate evicted terminal artifact: %v", err)
	}
	reactivatedClaim, acquired, err := repository.AcquireBuildJob(
		ctx, terminalArtifact.Key, "worker-terminal:evicted", time.Minute,
	)
	if err != nil || !acquired || reactivatedClaim.Fence <= terminalClaim.Fence {
		t.Fatalf("evicted terminal AcquireBuildJob = %#v, %t, %v", reactivatedClaim, acquired, err)
	}
}

func TestFontRepositoryArtifactCapacityIntegration(t *testing.T) {
	databaseURL := os.Getenv("EMFONT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EMFONT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	firstPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open first database pool: %v", err)
	}
	defer firstPool.Close()
	secondPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open second database pool: %v", err)
	}
	defer secondPool.Close()
	if err := FontSchemaReady(ctx, firstPool); err != nil {
		t.Fatalf("FontSchemaReady: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "capacity-integration-" + suffix
	if _, err := firstPool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`, familyID, "Capacity Integration "+suffix); err != nil {
		t.Fatalf("insert font family: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := firstPool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID); err != nil {
			t.Errorf("cleanup font family: %v", err)
		}
	}()

	const reservation = int64(1024)
	usage := func() (int64, int64) {
		t.Helper()
		var count, accountedBytes, derivedCount, derivedBytes int64
		if err := firstPool.QueryRow(ctx, `
			SELECT
				quota.artifact_count,
				quota.accounted_bytes,
				(SELECT COUNT(*)::bigint FROM font_artifacts),
				(SELECT COALESCE(SUM(quota_bytes), 0)::bigint FROM font_artifacts)
			FROM font_artifact_quota AS quota
			WHERE quota.singleton`).Scan(&count, &accountedBytes, &derivedCount, &derivedBytes); err != nil {
			t.Fatalf("read artifact usage: %v", err)
		}
		if count != derivedCount || accountedBytes != derivedBytes {
			t.Fatalf(
				"quota ledger = (%d, %d), derived usage = (%d, %d)",
				count, accountedBytes, derivedCount, derivedBytes,
			)
		}
		return count, accountedBytes
	}
	artifact := func(label string) domain.Artifact {
		return domain.Artifact{
			Key: "capacity:" + suffix + ":" + label, Kind: domain.BuildModeDynamic, Status: "pending",
			FamilyID: familyID, Weight: 400, WordHash: label, NormalizedWordSet: label,
			SourceChecksum: "source-" + suffix, BuilderVersion: "capacity-integration",
			ProtocolVersion: domain.ArtifactProtocolVersion,
			ObjectKey:       "_generated/capacity-" + suffix + "-" + label + ".woff2",
			ContentType:     domain.ContentTypeWOFF2,
		}
	}
	cleanupArtifacts := func(artifacts ...domain.Artifact) {
		t.Helper()
		keys := make([]string, len(artifacts))
		for index := range artifacts {
			keys[index] = artifacts[index].Key
		}
		if _, err := firstPool.Exec(ctx, "DELETE FROM font_artifacts WHERE artifact_key = ANY($1::text[])", keys); err != nil {
			t.Fatalf("cleanup capacity artifacts: %v", err)
		}
	}
	runConcurrent := func(cfg FontRepositoryConfig, first, second domain.Artifact) (int, int) {
		t.Helper()
		repositories := []*FontRepository{
			NewFontRepositoryFromPool(firstPool, cfg),
			NewFontRepositoryFromPool(secondPool, cfg),
		}
		artifacts := []domain.Artifact{first, second}
		start := make(chan struct{})
		var wait sync.WaitGroup
		results := make([]error, len(repositories))
		for index := range repositories {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				<-start
				results[index] = repositories[index].CreateFontArtifact(ctx, artifacts[index])
			}(index)
		}
		close(start)
		wait.Wait()
		winner, loser := -1, -1
		for index, createErr := range results {
			switch {
			case createErr == nil:
				winner = index
			case errors.Is(createErr, domain.ErrArtifactCapacity):
				loser = index
			default:
				t.Fatalf("concurrent CreateFontArtifact %d error = %v", index, createErr)
			}
		}
		if winner < 0 || loser < 0 {
			t.Fatalf("concurrent admission results = %#v, want one success and one capacity error", results)
		}
		if err := repositories[loser].CreateFontArtifact(ctx, artifacts[winner]); err != nil {
			t.Fatalf("existing identity at capacity: %v", err)
		}
		conflicting := artifacts[winner]
		conflicting.NormalizedWordSet += "-conflict"
		if err := repositories[loser].CreateFontArtifact(ctx, conflicting); !errors.Is(err, domain.ErrArtifactConflict) {
			t.Fatalf("existing conflict at capacity error = %v, want ErrArtifactConflict", err)
		}
		return winner, loser
	}

	t.Run("count", func(t *testing.T) {
		baselineCount, baselineBytes := usage()
		first, second := artifact("count-a"), artifact("count-b")
		runConcurrent(FontRepositoryConfig{
			MaxArtifacts: baselineCount + 1, MaxAccountedBytes: baselineBytes + 3*reservation,
			ArtifactReservation: reservation,
		}, first, second)
		cleanupArtifacts(first, second)
	})

	t.Run("accounted bytes", func(t *testing.T) {
		baselineCount, baselineBytes := usage()
		first, second := artifact("bytes-a"), artifact("bytes-b")
		cfg := FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + reservation + reservation/2,
			ArtifactReservation: reservation,
		}
		winner, loser := runConcurrent(cfg, first, second)
		artifacts := []domain.Artifact{first, second}
		repositories := []*FontRepository{
			NewFontRepositoryFromPool(firstPool, cfg),
			NewFontRepositoryFromPool(secondPool, cfg),
		}
		claim, acquired, err := repositories[winner].AcquireBuildJob(ctx, artifacts[winner].Key, "capacity-ready", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("AcquireBuildJob before ready transition = %t, %v", acquired, err)
		}
		if err := repositories[winner].MarkFontArtifactReady(ctx, artifacts[winner].Key, claim, domain.ArtifactObject{
			ObjectKey: artifacts[winner].ObjectKey + ".published", VersionID: "capacity-version",
			SizeBytes: reservation / 2, ETag: "capacity-etag", ChecksumSHA256: strings.Repeat("a", 64),
		}); err != nil {
			t.Fatalf("MarkFontArtifactReady within reservation: %v", err)
		}
		if err := repositories[loser].CreateFontArtifact(ctx, artifacts[loser]); err != nil {
			t.Fatalf("admit after ready bytes released reservation: %v", err)
		}
		cleanupArtifacts(first, second)
	})
}

func TestFontRepositoryTerminalFailureCacheIntegration(t *testing.T) {
	databaseURL := os.Getenv("EMFONT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EMFONT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	firstPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open first database pool: %v", err)
	}
	defer firstPool.Close()
	secondPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open second database pool: %v", err)
	}
	defer secondPool.Close()
	if err := FontSchemaReady(ctx, firstPool); err != nil {
		t.Fatalf("FontSchemaReady: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "terminal-cache-integration-" + suffix
	if _, err := firstPool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`, familyID, "Terminal Cache Integration "+suffix); err != nil {
		t.Fatalf("insert font family: %v", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := firstPool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID); err != nil {
			t.Errorf("cleanup font family: %v", err)
		}
	}()

	var baselineCount, baselineBytes, baselineFailures int64
	if err := firstPool.QueryRow(ctx, `
		SELECT quota.artifact_count, quota.accounted_bytes,
		       (SELECT COUNT(*)::bigint FROM font_terminal_failures)
		FROM font_artifact_quota AS quota
		WHERE quota.singleton`).Scan(&baselineCount, &baselineBytes, &baselineFailures); err != nil {
		t.Fatalf("read baseline cache usage: %v", err)
	}
	const reservation = int64(1024)
	const localFailureCapacity = int64(3)
	configFor := func(maxArtifacts int64) FontRepositoryConfig {
		return FontRepositoryConfig{
			MaxArtifacts: maxArtifacts, MaxAccountedBytes: baselineBytes + 64*reservation,
			ArtifactReservation: reservation, MaxTerminalFailures: baselineFailures + localFailureCapacity,
		}
	}
	artifact := func(label string) domain.Artifact {
		return domain.Artifact{
			Key: "terminal-cache:" + suffix + ":" + label, Kind: domain.BuildModeDynamic, Status: "pending",
			FamilyID: familyID, Weight: 400, WordHash: label, NormalizedWordSet: label,
			SourceChecksum: "terminal-source-" + suffix, BuilderVersion: "terminal-builder-" + suffix,
			ProtocolVersion: domain.ArtifactProtocolVersion,
			ObjectKey:       "_generated/terminal-cache-" + suffix + "-" + label + ".woff2",
			ContentType:     domain.ContentTypeWOFF2,
		}
	}
	assertQuotaBaseline := func(stage string) {
		t.Helper()
		var count, bytes, derivedCount, derivedBytes int64
		if err := firstPool.QueryRow(ctx, `
			SELECT quota.artifact_count, quota.accounted_bytes,
			       (SELECT COUNT(*)::bigint FROM font_artifacts),
			       (SELECT COALESCE(SUM(quota_bytes), 0)::bigint FROM font_artifacts)
			FROM font_artifact_quota AS quota
			WHERE quota.singleton`).Scan(&count, &bytes, &derivedCount, &derivedBytes); err != nil {
			t.Fatalf("%s: read artifact quota: %v", stage, err)
		}
		if count != baselineCount || bytes != baselineBytes || count != derivedCount || bytes != derivedBytes {
			t.Fatalf("%s: quota = (%d,%d), baseline = (%d,%d), derived = (%d,%d)",
				stage, count, bytes, baselineCount, baselineBytes, derivedCount, derivedBytes)
		}
	}
	failTerminal := func(repository *FontRepository, value domain.Artifact, owner string) domain.BuildClaim {
		t.Helper()
		if err := repository.CreateFontArtifact(ctx, value); err != nil {
			t.Fatalf("create terminal artifact %q: %v", value.Key, err)
		}
		claim, acquired, err := repository.AcquireBuildJob(ctx, value.Key, owner, time.Minute)
		if err != nil || !acquired {
			t.Fatalf("acquire terminal artifact %q = %t, %v", value.Key, acquired, err)
		}
		if err := repository.FailBuildJobTerminal(
			ctx, value.Key, claim, domain.FailureCodeUnsupportedCodepoints, "unsupported codepoint",
		); err != nil {
			t.Fatalf("cache terminal artifact %q: %v", value.Key, err)
		}
		assertQuotaBaseline("after terminal transition")
		return claim
	}

	t.Run("bounded eviction and artifact admission", func(t *testing.T) {
		repository := NewFontRepositoryFromPool(firstPool, configFor(baselineCount+4))
		values := []domain.Artifact{artifact("oldest"), artifact("second"), artifact("third"), artifact("newest")}
		failTerminal(repository, values[0], "terminal-oldest")
		if _, err := firstPool.Exec(ctx, `
			UPDATE font_terminal_failures SET cached_at = '-infinity'::timestamptz
			WHERE artifact_key = $1`, values[0].Key); err != nil {
			t.Fatalf("age oldest terminal failure: %v", err)
		}
		for index := 1; index < len(values); index++ {
			failTerminal(repository, values[index], "terminal-"+strconv.Itoa(index))
		}

		var globalCount, familyCount int64
		if err := firstPool.QueryRow(ctx, `
			SELECT COUNT(*)::bigint,
			       COUNT(*) FILTER (WHERE family_id = $1)::bigint
			FROM font_terminal_failures`, familyID).Scan(&globalCount, &familyCount); err != nil {
			t.Fatalf("read bounded terminal cache: %v", err)
		}
		if globalCount != baselineFailures+localFailureCapacity || familyCount != localFailureCapacity {
			t.Fatalf("terminal counts = global %d, family %d; want %d, %d",
				globalCount, familyCount, baselineFailures+localFailureCapacity, localFailureCapacity)
		}
		if _, err := repository.GetFontArtifact(ctx, values[0].Key); !errors.Is(err, domain.ErrArtifactNotFound) {
			t.Fatalf("oldest GetFontArtifact error = %v, want ErrArtifactNotFound", err)
		}
		cached, err := repository.GetFontArtifact(ctx, values[1].Key)
		if err != nil || cached.Status != "failed" || cached.FailureCode != domain.FailureCodeUnsupportedCodepoints {
			t.Fatalf("cached terminal artifact = %#v, %v", cached, err)
		}
		if err := repository.CreateFontArtifact(ctx, values[1]); !errors.Is(err, domain.ErrTerminalFailureCached) {
			t.Fatalf("cached CreateFontArtifact error = %v, want ErrTerminalFailureCached", err)
		}
		conflicting := values[1]
		conflicting.NormalizedWordSet += "-conflict"
		if err := repository.CreateFontArtifact(ctx, conflicting); !errors.Is(err, domain.ErrArtifactConflict) {
			t.Fatalf("conflicting terminal CreateFontArtifact error = %v, want ErrArtifactConflict", err)
		}

		quotaRepository := NewFontRepositoryFromPool(firstPool, configFor(baselineCount+2))
		ready := []domain.Artifact{artifact("ready-a"), artifact("ready-b")}
		for index, value := range ready {
			if err := quotaRepository.CreateFontArtifact(ctx, value); err != nil {
				t.Fatalf("create ready artifact %d with full terminal cache: %v", index, err)
			}
			claim, acquired, err := quotaRepository.AcquireBuildJob(ctx, value.Key, "ready-"+strconv.Itoa(index), time.Minute)
			if err != nil || !acquired {
				t.Fatalf("acquire ready artifact %d = %t, %v", index, acquired, err)
			}
			if err := quotaRepository.MarkFontArtifactReady(ctx, value.Key, claim, domain.ArtifactObject{
				ObjectKey: value.ObjectKey + ".published", VersionID: "ready-version-" + strconv.Itoa(index),
				SizeBytes: 128, ETag: "ready-etag", ChecksumSHA256: strings.Repeat(strconv.Itoa(index+1), 64),
			}); err != nil {
				t.Fatalf("publish ready artifact %d: %v", index, err)
			}
		}
		if err := quotaRepository.CreateFontArtifact(ctx, artifact("ready-over-limit")); !errors.Is(err, domain.ErrArtifactCapacity) {
			t.Fatalf("over-limit CreateFontArtifact error = %v, want ErrArtifactCapacity", err)
		}
		var readyCount int64
		if err := firstPool.QueryRow(ctx, `
			SELECT COUNT(*)::bigint FROM font_artifacts
			WHERE family_id = $1 AND status = 'ready'`, familyID).Scan(&readyCount); err != nil {
			t.Fatalf("count ready artifacts: %v", err)
		}
		if readyCount != 2 {
			t.Fatalf("ready artifact count = %d, want 2", readyCount)
		}
		if _, err := firstPool.Exec(ctx, "DELETE FROM font_artifacts WHERE artifact_key = ANY($1::text[])", []string{ready[0].Key, ready[1].Key}); err != nil {
			t.Fatalf("delete ready artifacts: %v", err)
		}
		assertQuotaBaseline("after ready artifact cleanup")
	})

	t.Run("concurrent transitions remain bounded", func(t *testing.T) {
		if _, err := firstPool.Exec(ctx, "DELETE FROM font_terminal_failures WHERE family_id = $1", familyID); err != nil {
			t.Fatalf("clear prior terminal failures: %v", err)
		}
		const workers = 8
		cfg := configFor(baselineCount + workers)
		repositories := []*FontRepository{
			NewFontRepositoryFromPool(firstPool, cfg),
			NewFontRepositoryFromPool(secondPool, cfg),
		}
		values := make([]domain.Artifact, workers)
		claims := make([]domain.BuildClaim, workers)
		for index := range values {
			values[index] = artifact("concurrent-" + strconv.Itoa(index))
			repository := repositories[index%len(repositories)]
			if err := repository.CreateFontArtifact(ctx, values[index]); err != nil {
				t.Fatalf("create concurrent terminal artifact %d: %v", index, err)
			}
			claim, acquired, err := repository.AcquireBuildJob(ctx, values[index].Key, "concurrent-"+strconv.Itoa(index), time.Minute)
			if err != nil || !acquired {
				t.Fatalf("acquire concurrent terminal artifact %d = %t, %v", index, acquired, err)
			}
			claims[index] = claim
		}

		start := make(chan struct{})
		results := make([]error, workers)
		var wait sync.WaitGroup
		for index := range values {
			wait.Add(1)
			go func(index int) {
				defer wait.Done()
				<-start
				results[index] = repositories[index%len(repositories)].FailBuildJobTerminal(
					ctx, values[index].Key, claims[index], domain.FailureCodeUnsupportedCodepoints, "concurrent unsupported",
				)
			}(index)
		}
		close(start)
		wait.Wait()
		for index, transitionErr := range results {
			if transitionErr != nil {
				t.Fatalf("concurrent terminal transition %d: %v", index, transitionErr)
			}
		}
		var terminalCount int64
		if err := firstPool.QueryRow(ctx, "SELECT COUNT(*)::bigint FROM font_terminal_failures").Scan(&terminalCount); err != nil {
			t.Fatalf("count concurrent terminal failures: %v", err)
		}
		if terminalCount != baselineFailures+localFailureCapacity {
			t.Fatalf("concurrent terminal count = %d, want %d", terminalCount, baselineFailures+localFailureCapacity)
		}
		assertQuotaBaseline("after concurrent terminal transitions")
	})

	t.Run("admission races terminal transition", func(t *testing.T) {
		if _, err := firstPool.Exec(ctx, "DELETE FROM font_terminal_failures WHERE family_id = $1", familyID); err != nil {
			t.Fatalf("clear terminal failures before admission race: %v", err)
		}
		cfg := configFor(baselineCount + 1)
		terminalRepository := NewFontRepositoryFromPool(firstPool, cfg)
		admissionRepository := NewFontRepositoryFromPool(secondPool, cfg)
		const iterations = 32
		for iteration := 0; iteration < iterations; iteration++ {
			value := artifact("admission-race-" + strconv.Itoa(iteration))
			if err := terminalRepository.CreateFontArtifact(ctx, value); err != nil {
				t.Fatalf("iteration %d create terminal candidate: %v", iteration, err)
			}
			claim, acquired, err := terminalRepository.AcquireBuildJob(
				ctx, value.Key, "admission-race-terminal-"+strconv.Itoa(iteration), time.Minute,
			)
			if err != nil || !acquired {
				t.Fatalf("iteration %d acquire terminal candidate = %t, %v", iteration, acquired, err)
			}

			start := make(chan struct{})
			terminalResult := make(chan error, 1)
			admissionResult := make(chan error, 1)
			go func() {
				<-start
				terminalResult <- terminalRepository.FailBuildJobTerminal(
					ctx, value.Key, claim, domain.FailureCodeUnsupportedCodepoints, "raced unsupported",
				)
			}()
			go func() {
				<-start
				admissionResult <- admissionRepository.CreateFontArtifact(ctx, value)
			}()
			close(start)
			if err := <-terminalResult; err != nil {
				t.Fatalf("iteration %d terminal transition: %v", iteration, err)
			}
			if err := <-admissionResult; err != nil && !errors.Is(err, domain.ErrTerminalFailureCached) {
				t.Fatalf("iteration %d raced admission: %v", iteration, err)
			}

			var artifactCount, terminalCount int64
			if err := firstPool.QueryRow(ctx, `
				SELECT
					(SELECT COUNT(*)::bigint FROM font_artifacts WHERE artifact_key = $1),
					(SELECT COUNT(*)::bigint FROM font_terminal_failures WHERE artifact_key = $1)`,
				value.Key,
			).Scan(&artifactCount, &terminalCount); err != nil {
				t.Fatalf("iteration %d inspect raced state: %v", iteration, err)
			}
			if artifactCount != 0 || terminalCount != 1 {
				t.Fatalf(
					"iteration %d raced state = artifacts %d terminal failures %d, want 0 and 1",
					iteration, artifactCount, terminalCount,
				)
			}
			if _, err := firstPool.Exec(ctx, "DELETE FROM font_terminal_failures WHERE artifact_key = $1", value.Key); err != nil {
				t.Fatalf("iteration %d clear raced terminal failure: %v", iteration, err)
			}
			assertQuotaBaseline("after admission-terminal race")
		}
	})

	t.Run("stale fence cannot move artifact", func(t *testing.T) {
		if _, err := firstPool.Exec(ctx, "DELETE FROM font_terminal_failures WHERE family_id = $1", familyID); err != nil {
			t.Fatalf("clear concurrent terminal failures: %v", err)
		}
		repository := NewFontRepositoryFromPool(firstPool, configFor(baselineCount+1))
		value := artifact("stale-fence")
		if err := repository.CreateFontArtifact(ctx, value); err != nil {
			t.Fatalf("create stale-fence artifact: %v", err)
		}
		staleClaim, acquired, err := repository.AcquireBuildJob(ctx, value.Key, "stale-owner", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("acquire stale claim = %t, %v", acquired, err)
		}
		if _, err := firstPool.Exec(ctx, `
			UPDATE font_build_jobs SET lease_until = now() - interval '1 second'
			WHERE artifact_key = $1`, value.Key); err != nil {
			t.Fatalf("expire stale claim: %v", err)
		}
		winnerClaim, acquired, err := repository.AcquireBuildJob(ctx, value.Key, "winner-owner", time.Minute)
		if err != nil || !acquired || winnerClaim.Fence <= staleClaim.Fence {
			t.Fatalf("acquire winner claim = %#v, %t, %v", winnerClaim, acquired, err)
		}
		if err := repository.FailBuildJobTerminal(
			ctx, value.Key, staleClaim, domain.FailureCodeUnsupportedCodepoints, "stale unsupported",
		); !errors.Is(err, domain.ErrBuildNotReady) {
			t.Fatalf("stale terminal transition error = %v, want ErrBuildNotReady", err)
		}
		var status string
		var terminalCount int64
		if err := firstPool.QueryRow(ctx, `
			SELECT artifact.status,
			       (SELECT COUNT(*)::bigint FROM font_terminal_failures WHERE artifact_key = $1)
			FROM font_artifacts AS artifact
			WHERE artifact.artifact_key = $1`, value.Key).Scan(&status, &terminalCount); err != nil {
			t.Fatalf("read artifact after stale transition: %v", err)
		}
		if status != "running" || terminalCount != 0 {
			t.Fatalf("state after stale transition = status %q, terminal count %d", status, terminalCount)
		}
		if err := repository.FailBuildJobTerminal(
			ctx, value.Key, winnerClaim, domain.FailureCodeUnsupportedCodepoints, "winner unsupported",
		); err != nil {
			t.Fatalf("winner terminal transition: %v", err)
		}
		assertQuotaBaseline("after winner terminal transition")
	})
}

func TestFontRepositoryPersistedArtifactQuotaIntegration(t *testing.T) {
	databaseURL := os.Getenv("EMFONT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EMFONT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	firstPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open first database pool: %v", err)
	}
	t.Cleanup(firstPool.Close)
	secondPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open second database pool: %v", err)
	}
	t.Cleanup(secondPool.Close)
	if err := FontSchemaReady(ctx, firstPool); err != nil {
		t.Fatalf("FontSchemaReady: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "persisted-quota-" + suffix
	if _, err := firstPool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`, familyID, "Persisted Quota "+suffix); err != nil {
		t.Fatalf("insert font family: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := firstPool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID); err != nil {
			t.Errorf("cleanup font family: %v", err)
		}
	})

	usage := func(tb testing.TB) (int64, int64) {
		tb.Helper()
		var count, accountedBytes, derivedCount, derivedBytes int64
		if err := firstPool.QueryRow(ctx, `
			SELECT
				quota.artifact_count,
				quota.accounted_bytes,
				(SELECT COUNT(*)::bigint FROM font_artifacts),
				(SELECT COALESCE(SUM(quota_bytes), 0)::bigint FROM font_artifacts)
			FROM font_artifact_quota AS quota
			WHERE quota.singleton`).Scan(&count, &accountedBytes, &derivedCount, &derivedBytes); err != nil {
			tb.Fatalf("read persisted artifact usage: %v", err)
		}
		if count != derivedCount || accountedBytes != derivedBytes {
			tb.Fatalf(
				"quota ledger = (%d, %d), derived usage = (%d, %d)",
				count, accountedBytes, derivedCount, derivedBytes,
			)
		}
		return count, accountedBytes
	}
	artifact := func(label string) domain.Artifact {
		return domain.Artifact{
			Key: "persisted-quota:" + suffix + ":" + label, Kind: domain.BuildModeDynamic, Status: "pending",
			FamilyID: familyID, Weight: 400, WordHash: label, NormalizedWordSet: label,
			SourceChecksum: "source-" + suffix, BuilderVersion: "persisted-quota-integration",
			ProtocolVersion: domain.ArtifactProtocolVersion,
			ObjectKey:       "_generated/persisted-quota-" + suffix + "-" + label + ".woff2",
			ContentType:     domain.ContentTypeWOFF2,
		}
	}

	t.Run("truncate fails closed", func(t *testing.T) {
		beforeCount, beforeBytes := usage(t)
		tx, err := firstPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin truncate probe: %v", err)
		}
		_, truncateErr := tx.Exec(ctx, "TRUNCATE TABLE font_artifacts CASCADE")
		if rollbackErr := tx.Rollback(ctx); rollbackErr != nil {
			t.Fatalf("rollback truncate probe: %v", rollbackErr)
		}
		if truncateErr == nil || !strings.Contains(truncateErr.Error(), "cannot be truncated") {
			t.Fatalf("TRUNCATE error = %v, want quota-accounting rejection", truncateErr)
		}
		afterCount, afterBytes := usage(t)
		if afterCount != beforeCount || afterBytes != beforeBytes {
			t.Fatalf("quota changed after rejected TRUNCATE: before=(%d,%d) after=(%d,%d)",
				beforeCount, beforeBytes, afterCount, afterBytes)
		}
	})

	t.Run("family cascade preserves ledger", func(t *testing.T) {
		const cascadeReservation = int64(1024)
		baselineCount, baselineBytes := usage(t)
		cascadeFamilyID := familyID + "-cascade"
		if _, err := firstPool.Exec(ctx, `
			INSERT INTO font_family (id, name, weights, format)
			VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`, cascadeFamilyID, "Quota Cascade "+suffix); err != nil {
			t.Fatalf("insert cascade family: %v", err)
		}
		cascadeArtifact := artifact("cascade")
		cascadeArtifact.FamilyID = cascadeFamilyID
		repository := NewFontRepositoryFromPool(firstPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 10*cascadeReservation,
			ArtifactReservation: cascadeReservation,
		})
		if err := repository.CreateFontArtifact(ctx, cascadeArtifact); err != nil {
			t.Fatalf("create cascade artifact: %v", err)
		}
		if count, bytes := usage(t); count != baselineCount+1 || bytes != baselineBytes+cascadeReservation {
			t.Fatalf("quota after cascade fixture = (%d,%d), want (%d,%d)",
				count, bytes, baselineCount+1, baselineBytes+cascadeReservation)
		}
		if _, err := firstPool.Exec(ctx, "DELETE FROM font_family WHERE id = $1", cascadeFamilyID); err != nil {
			t.Fatalf("delete cascade family: %v", err)
		}
		if count, bytes := usage(t); count != baselineCount || bytes != baselineBytes {
			t.Fatalf("quota after family cascade = (%d,%d), want (%d,%d)",
				count, bytes, baselineCount, baselineBytes)
		}
	})
	cleanupArtifacts := func(tb testing.TB, artifacts ...domain.Artifact) {
		tb.Helper()
		keys := make([]string, len(artifacts))
		for index := range artifacts {
			keys[index] = artifacts[index].Key
		}
		if _, err := firstPool.Exec(ctx, "DELETE FROM font_artifacts WHERE artifact_key = ANY($1::text[])", keys); err != nil {
			tb.Fatalf("cleanup persisted quota artifacts: %v", err)
		}
	}

	t.Run("different pod reservations stay row local", func(t *testing.T) {
		baselineCount, baselineBytes := usage(t)
		first := artifact("pod-a")
		second := artifact("pod-b")
		defer cleanupArtifacts(t, first, second)

		podA := NewFontRepositoryFromPool(firstPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 4096,
			ArtifactReservation: 1024,
		})
		if err := podA.CreateFontArtifact(ctx, first); err != nil {
			t.Fatalf("pod A CreateFontArtifact: %v", err)
		}
		podB := NewFontRepositoryFromPool(secondPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 1024 + 2048,
			ArtifactReservation: 2048,
		})
		if err := podB.CreateFontArtifact(ctx, second); err != nil {
			t.Fatalf("pod B CreateFontArtifact: %v", err)
		}

		var firstReservation, secondReservation int64
		if err := firstPool.QueryRow(ctx, `
			SELECT
				MAX(reservation_bytes) FILTER (WHERE artifact_key = $1),
				MAX(reservation_bytes) FILTER (WHERE artifact_key = $2)
			FROM font_artifacts
			WHERE artifact_key IN ($1, $2)`, first.Key, second.Key).Scan(&firstReservation, &secondReservation); err != nil {
			t.Fatalf("read pod reservations: %v", err)
		}
		if firstReservation != 1024 || secondReservation != 2048 {
			t.Fatalf("persisted reservations = (%d, %d), want (1024, 2048)", firstReservation, secondReservation)
		}
		_, accountedBytes := usage(t)
		if accountedBytes != baselineBytes+3072 {
			t.Fatalf("accounted bytes = %d, want %d", accountedBytes, baselineBytes+3072)
		}
	})

	t.Run("failed reservation is released and retry is re-admitted", func(t *testing.T) {
		baselineCount, baselineBytes := usage(t)
		target := artifact("retry-readmission-target")
		blocker := artifact("retry-readmission-blocker")
		defer cleanupArtifacts(t, target, blocker)

		const reservation = int64(1024)
		setup := NewFontRepositoryFromPool(firstPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 2, MaxAccountedBytes: baselineBytes + reservation,
			ArtifactReservation: reservation,
		})
		if err := setup.CreateFontArtifact(ctx, target); err != nil {
			t.Fatalf("create retry target: %v", err)
		}
		claim, acquired, err := setup.AcquireBuildJob(ctx, target.Key, "quota-retry-initial", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("initial retry target acquire = %t, %v", acquired, err)
		}
		if err := setup.FailBuildJob(ctx, target.Key, claim, "retryable failure"); err != nil {
			t.Fatalf("fail retry target: %v", err)
		}

		var status string
		var reservationBytes, quotaBytes int64
		if err := firstPool.QueryRow(ctx, `
			SELECT status, reservation_bytes, quota_bytes
			FROM font_artifacts
			WHERE artifact_key = $1`, target.Key).Scan(&status, &reservationBytes, &quotaBytes); err != nil {
			t.Fatalf("read failed retry target accounting: %v", err)
		}
		if status != "failed" || reservationBytes != reservation || quotaBytes != 0 {
			t.Fatalf(
				"failed retry target = status %q reservation %d quota %d",
				status, reservationBytes, quotaBytes,
			)
		}
		count, accountedBytes := usage(t)
		if count != baselineCount+1 || accountedBytes != baselineBytes {
			t.Fatalf(
				"failed retry usage = (%d, %d), want (%d, %d)",
				count, accountedBytes, baselineCount+1, baselineBytes,
			)
		}
		if _, err := firstPool.Exec(ctx, `
			UPDATE font_build_jobs SET next_attempt_at = now() - interval '1 second'
			WHERE artifact_key = $1`, target.Key); err != nil {
			t.Fatalf("make retry target due: %v", err)
		}

		countLimited := NewFontRepositoryFromPool(secondPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 1, MaxAccountedBytes: baselineBytes + 10*reservation,
			ArtifactReservation: reservation,
		})
		countClaim, acquired, err := countLimited.AcquireBuildJob(
			ctx, target.Key, "quota-retry-count-limit", time.Minute,
		)
		if err != nil || !acquired {
			t.Fatalf("retry using existing artifact slot = %t, %v; want true, nil", acquired, err)
		}
		count, _ = usage(t)
		if count != baselineCount+1 {
			t.Fatalf("retry artifact count = %d, want %d", count, baselineCount+1)
		}
		if err := countLimited.CreateFontArtifact(ctx, blocker); !errors.Is(err, domain.ErrArtifactCapacity) {
			t.Fatalf("new artifact beyond count cap error = %v, want ErrArtifactCapacity", err)
		}
		if err := countLimited.FailBuildJob(ctx, target.Key, countClaim, "release count-limited retry"); err != nil {
			t.Fatalf("release count-limited retry: %v", err)
		}
		if _, err := firstPool.Exec(ctx, `
			UPDATE font_build_jobs SET next_attempt_at = now() - interval '1 second'
			WHERE artifact_key = $1`, target.Key); err != nil {
			t.Fatalf("make count-limited retry due again: %v", err)
		}

		if err := setup.CreateFontArtifact(ctx, blocker); err != nil {
			t.Fatalf("create retry capacity blocker: %v", err)
		}
		if _, acquired, err := setup.AcquireBuildJob(
			ctx, target.Key, "quota-retry-byte-limit", time.Minute,
		); err != nil || acquired {
			t.Fatalf("retry beyond byte cap = %t, %v; want false, nil", acquired, err)
		}
		if _, err := firstPool.Exec(ctx, "DELETE FROM font_artifacts WHERE artifact_key = $1", blocker.Key); err != nil {
			t.Fatalf("delete retry capacity blocker: %v", err)
		}
		retryClaim, acquired, err := setup.AcquireBuildJob(ctx, target.Key, "quota-retry-admitted", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("retry after capacity release = %t, %v", acquired, err)
		}
		_, accountedBytes = usage(t)
		if accountedBytes != baselineBytes+reservation {
			t.Fatalf("re-admitted retry bytes = %d, want %d", accountedBytes, baselineBytes+reservation)
		}
		if err := setup.FailBuildJob(ctx, target.Key, retryClaim, "release re-admitted reservation"); err != nil {
			t.Fatalf("release re-admitted retry: %v", err)
		}
	})

	t.Run("concurrent retries cannot oversubscribe byte quota", func(t *testing.T) {
		baselineCount, baselineBytes := usage(t)
		first := artifact("retry-race-a")
		second := artifact("retry-race-b")
		defer cleanupArtifacts(t, first, second)

		const reservation = int64(1024)
		setup := NewFontRepositoryFromPool(firstPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 2, MaxAccountedBytes: baselineBytes + 2*reservation,
			ArtifactReservation: reservation,
		})
		artifacts := []domain.Artifact{first, second}
		for index, value := range artifacts {
			if err := setup.CreateFontArtifact(ctx, value); err != nil {
				t.Fatalf("create retry race artifact %d: %v", index, err)
			}
			claim, acquired, err := setup.AcquireBuildJob(ctx, value.Key, "quota-retry-race-setup", time.Minute)
			if err != nil || !acquired {
				t.Fatalf("setup retry race acquire %d = %t, %v", index, acquired, err)
			}
			if err := setup.FailBuildJob(ctx, value.Key, claim, "retry race setup failure"); err != nil {
				t.Fatalf("setup retry race failure %d: %v", index, err)
			}
		}
		if _, err := firstPool.Exec(ctx, `
			UPDATE font_build_jobs
			SET next_attempt_at = now() - interval '1 second'
			WHERE artifact_key = ANY($1::text[])`, []string{first.Key, second.Key}); err != nil {
			t.Fatalf("make retry race jobs due: %v", err)
		}

		cfg := FontRepositoryConfig{
			MaxArtifacts: baselineCount + 2, MaxAccountedBytes: baselineBytes + reservation,
			ArtifactReservation: reservation,
		}
		repositories := []*FontRepository{
			NewFontRepositoryFromPool(firstPool, cfg),
			NewFontRepositoryFromPool(secondPool, cfg),
		}
		type acquireResult struct {
			claim    domain.BuildClaim
			acquired bool
			err      error
		}
		start := make(chan struct{})
		results := make(chan acquireResult, len(repositories))
		for index := range repositories {
			go func(index int) {
				<-start
				claim, acquired, err := repositories[index].AcquireBuildJob(
					ctx, artifacts[index].Key, "quota-retry-race-"+strconv.Itoa(index), time.Minute,
				)
				results <- acquireResult{claim: claim, acquired: acquired, err: err}
			}(index)
		}
		close(start)

		acquiredCount := 0
		var winnerClaim domain.BuildClaim
		var winnerKey string
		for index := 0; index < len(repositories); index++ {
			result := <-results
			if result.err != nil {
				t.Fatalf("concurrent retry acquire error: %v", result.err)
			}
			if result.acquired {
				acquiredCount++
				winnerClaim = result.claim
				winnerKey = ""
				for _, value := range artifacts {
					if result.claim.Owner == "quota-retry-race-0" && value.Key == first.Key ||
						result.claim.Owner == "quota-retry-race-1" && value.Key == second.Key {
						winnerKey = value.Key
					}
				}
			}
		}
		if acquiredCount != 1 || winnerKey == "" {
			t.Fatalf("concurrent retry acquisitions = %d, winner key = %q; want exactly one", acquiredCount, winnerKey)
		}
		_, accountedBytes := usage(t)
		if accountedBytes != baselineBytes+reservation {
			t.Fatalf("concurrent retry bytes = %d, want %d", accountedBytes, baselineBytes+reservation)
		}
		if err := setup.FailBuildJob(ctx, winnerKey, winnerClaim, "release retry race winner"); err != nil {
			t.Fatalf("release retry race winner: %v", err)
		}
	})

	t.Run("reservation upgrade is atomic", func(t *testing.T) {
		baselineCount, baselineBytes := usage(t)
		target := artifact("upgrade-target")
		blocker := artifact("upgrade-blocker")
		defer cleanupArtifacts(t, target, blocker)

		initial := NewFontRepositoryFromPool(firstPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 4096,
			ArtifactReservation: 1024,
		})
		if err := initial.CreateFontArtifact(ctx, target); err != nil {
			t.Fatalf("create upgrade target: %v", err)
		}
		if _, err := firstPool.Exec(ctx, `
			INSERT INTO font_artifacts (
				artifact_key, kind, status, family_id, weight, builder_version,
				artifact_protocol_version, object_key, content_type, size_bytes, reservation_bytes
			) VALUES ($1, 'dynamic', 'ready', $2, 400, 'persisted-quota-integration',
				'v1', $3, 'font/woff2', 1, 1)`, blocker.Key, familyID, blocker.ObjectKey); err != nil {
			t.Fatalf("insert upgrade blocker: %v", err)
		}

		upgrade := NewFontRepositoryFromPool(secondPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 2048,
			ArtifactReservation: 2048,
		})
		if err := upgrade.CreateFontArtifact(ctx, target); !errors.Is(err, domain.ErrArtifactCapacity) {
			t.Fatalf("rejected reservation upgrade error = %v, want ErrArtifactCapacity", err)
		}
		var status string
		var reservationBytes int64
		if err := firstPool.QueryRow(ctx, `
			SELECT status, reservation_bytes FROM font_artifacts WHERE artifact_key = $1`, target.Key,
		).Scan(&status, &reservationBytes); err != nil {
			t.Fatalf("read rejected upgrade target: %v", err)
		}
		if status != "pending" || reservationBytes != 1024 {
			t.Fatalf("rejected upgrade state = %q, %d; want pending, 1024", status, reservationBytes)
		}

		if _, err := firstPool.Exec(ctx, "DELETE FROM font_artifacts WHERE artifact_key = $1", blocker.Key); err != nil {
			t.Fatalf("delete upgrade blocker: %v", err)
		}
		if err := upgrade.CreateFontArtifact(ctx, target); err != nil {
			t.Fatalf("admitted reservation upgrade: %v", err)
		}
		if err := firstPool.QueryRow(ctx, `
			SELECT reservation_bytes FROM font_artifacts WHERE artifact_key = $1`, target.Key,
		).Scan(&reservationBytes); err != nil {
			t.Fatalf("read admitted upgrade target: %v", err)
		}
		if reservationBytes != 2048 {
			t.Fatalf("admitted reservation = %d, want 2048", reservationBytes)
		}
	})

	t.Run("ready size uses persisted reservation", func(t *testing.T) {
		baselineCount, baselineBytes := usage(t)
		large := artifact("ready-large")
		small := artifact("ready-small")
		defer cleanupArtifacts(t, large, small)
		largeReservation := NewFontRepositoryFromPool(firstPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 4096,
			ArtifactReservation: 2048,
		})
		if err := largeReservation.CreateFontArtifact(ctx, large); err != nil {
			t.Fatalf("create large-reservation artifact: %v", err)
		}
		largeClaim, acquired, err := largeReservation.AcquireBuildJob(ctx, large.Key, "quota-ready-large", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("acquire large-reservation artifact = %t, %v", acquired, err)
		}
		localSmall := NewFontRepositoryFromPool(secondPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 4096,
			ArtifactReservation: 1024,
		})
		if err := localSmall.MarkFontArtifactReady(ctx, large.Key, largeClaim, domain.ArtifactObject{
			ObjectKey: large.ObjectKey + ".published", SizeBytes: 1536,
		}); err != nil {
			t.Fatalf("publish within persisted reservation from smaller pod: %v", err)
		}

		if err := localSmall.CreateFontArtifact(ctx, small); err != nil {
			t.Fatalf("create small-reservation artifact: %v", err)
		}
		smallClaim, acquired, err := localSmall.AcquireBuildJob(ctx, small.Key, "quota-ready-small", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("acquire small-reservation artifact = %t, %v", acquired, err)
		}
		if err := largeReservation.MarkFontArtifactReady(ctx, small.Key, smallClaim, domain.ArtifactObject{
			ObjectKey: small.ObjectKey + ".oversized", SizeBytes: 1025,
		}); !errors.Is(err, domain.ErrArtifactCapacity) {
			t.Fatalf("oversized MarkFontArtifactReady error = %v, want ErrArtifactCapacity", err)
		}
		var artifactStatus, jobStatus string
		var sizeBytes int64
		if err := firstPool.QueryRow(ctx, `
			SELECT artifact.status, artifact.size_bytes, job.status
			FROM font_artifacts AS artifact
			JOIN font_build_jobs AS job USING (artifact_key)
			WHERE artifact.artifact_key = $1`, small.Key).Scan(&artifactStatus, &sizeBytes, &jobStatus); err != nil {
			t.Fatalf("read oversized publish state: %v", err)
		}
		if artifactStatus != "running" || sizeBytes != 0 || jobStatus != "running" {
			t.Fatalf("oversized publish state = artifact %q size %d job %q", artifactStatus, sizeBytes, jobStatus)
		}
	})

	t.Run("ready to missing checks projected capacity", func(t *testing.T) {
		baselineCount, baselineBytes := usage(t)
		target := artifact("missing-capacity")
		defer cleanupArtifacts(t, target)
		creator := NewFontRepositoryFromPool(firstPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 2048,
			ArtifactReservation: 1024,
		})
		if err := creator.CreateFontArtifact(ctx, target); err != nil {
			t.Fatalf("create missing-capacity artifact: %v", err)
		}
		claim, acquired, err := creator.AcquireBuildJob(ctx, target.Key, "quota-missing", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("acquire missing-capacity artifact = %t, %v", acquired, err)
		}
		publishedKey := target.ObjectKey + ".published"
		if err := creator.MarkFontArtifactReady(ctx, target.Key, claim, domain.ArtifactObject{
			ObjectKey: publishedKey, SizeBytes: 1,
		}); err != nil {
			t.Fatalf("publish missing-capacity artifact: %v", err)
		}

		rejectingPod := NewFontRepositoryFromPool(secondPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 1023,
			ArtifactReservation: 1,
		})
		marked, err := rejectingPod.MarkFontArtifactMissing(ctx, target.Key, publishedKey, claim.Fence, "object absent")
		if marked || !errors.Is(err, domain.ErrArtifactCapacity) {
			t.Fatalf("rejected MarkFontArtifactMissing = %t, %v; want false, ErrArtifactCapacity", marked, err)
		}
		var status string
		if err := firstPool.QueryRow(ctx, "SELECT status FROM font_artifacts WHERE artifact_key = $1", target.Key).Scan(&status); err != nil {
			t.Fatalf("read rejected missing state: %v", err)
		}
		if status != "ready" {
			t.Fatalf("rejected missing status = %q, want ready", status)
		}

		allowingPod := NewFontRepositoryFromPool(secondPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 1024,
			ArtifactReservation: 1,
		})
		marked, err = allowingPod.MarkFontArtifactMissing(ctx, target.Key, publishedKey, claim.Fence, "object absent")
		if err != nil || !marked {
			t.Fatalf("admitted MarkFontArtifactMissing = %t, %v", marked, err)
		}
		_, accountedBytes := usage(t)
		if accountedBytes != baselineBytes+1024 {
			t.Fatalf("missing accounted bytes = %d, want %d", accountedBytes, baselineBytes+1024)
		}
	})

	t.Run("retirement and stale reactivation preserve quota", func(t *testing.T) {
		baselineCount, baselineBytes := usage(t)
		retired := artifact("retired-ready")
		newcomer := artifact("retired-newcomer")
		defer cleanupArtifacts(t, retired, newcomer)
		creator := NewFontRepositoryFromPool(firstPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 2048,
			ArtifactReservation: 1024,
		})
		if err := creator.CreateFontArtifact(ctx, retired); err != nil {
			t.Fatalf("create retirement artifact: %v", err)
		}
		claim, acquired, err := creator.AcquireBuildJob(ctx, retired.Key, "quota-retirement", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("acquire retirement artifact = %t, %v", acquired, err)
		}
		if err := creator.MarkFontArtifactReady(ctx, retired.Key, claim, domain.ArtifactObject{
			ObjectKey: retired.ObjectKey + ".published", SizeBytes: 10,
		}); err != nil {
			t.Fatalf("publish retirement artifact: %v", err)
		}
		oldest := time.Date(2, time.January, 1, 0, 0, 0, 0, time.UTC)
		if _, err := firstPool.Exec(ctx, "UPDATE font_artifacts SET updated_at = $2 WHERE artifact_key = $1", retired.Key, oldest); err != nil {
			t.Fatalf("age retirement artifact: %v", err)
		}
		if rows, err := creator.RetireFontArtifacts(ctx, time.Date(3, time.January, 1, 0, 0, 0, 0, time.UTC), time.Now(), 1); err != nil || rows != 1 {
			t.Fatalf("RetireFontArtifacts = %d, %v; want 1, nil", rows, err)
		}
		var status, jobStatus string
		var sizeBytes, reservationBytes int64
		if err := firstPool.QueryRow(ctx, `
			SELECT artifact.status, artifact.size_bytes, artifact.reservation_bytes, job.status
			FROM font_artifacts AS artifact
			JOIN font_build_jobs AS job USING (artifact_key)
			WHERE artifact.artifact_key = $1`, retired.Key,
		).Scan(&status, &sizeBytes, &reservationBytes, &jobStatus); err != nil {
			t.Fatalf("read retired artifact: %v", err)
		}
		if status != "stale" || sizeBytes != 10 || reservationBytes != 1024 || jobStatus != "ready" {
			t.Fatalf("retired state = %q size %d reservation %d job %q", status, sizeBytes, reservationBytes, jobStatus)
		}

		newcomerPod := NewFontRepositoryFromPool(secondPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 110,
			ArtifactReservation: 100,
		})
		if err := newcomerPod.CreateFontArtifact(ctx, newcomer); err != nil {
			t.Fatalf("admit after ready artifact retirement: %v", err)
		}
		_, accountedBytes := usage(t)
		if accountedBytes != baselineBytes+110 {
			t.Fatalf("retired accounted bytes = %d, want %d", accountedBytes, baselineBytes+110)
		}

		reactivate := NewFontRepositoryFromPool(firstPool, FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 1024,
			ArtifactReservation: 1024,
		})
		if err := reactivate.CreateFontArtifact(ctx, retired); !errors.Is(err, domain.ErrArtifactCapacity) {
			t.Fatalf("stale reactivation at capacity error = %v, want ErrArtifactCapacity", err)
		}
		if _, acquired, err := reactivate.AcquireBuildJob(ctx, retired.Key, "quota-stale-bypass", time.Minute); err != nil || acquired {
			t.Fatalf("direct stale AcquireBuildJob = %t, %v; want false, nil", acquired, err)
		}
		if err := firstPool.QueryRow(ctx, `
			SELECT artifact.status, job.status
			FROM font_artifacts AS artifact JOIN font_build_jobs AS job USING (artifact_key)
			WHERE artifact.artifact_key = $1`, retired.Key).Scan(&status, &jobStatus); err != nil {
			t.Fatalf("read rejected stale reactivation: %v", err)
		}
		if status != "stale" || jobStatus != "ready" {
			t.Fatalf("rejected stale reactivation state = artifact %q job %q", status, jobStatus)
		}

		if _, err := firstPool.Exec(ctx, "DELETE FROM font_artifacts WHERE artifact_key = $1", newcomer.Key); err != nil {
			t.Fatalf("delete stale reactivation blocker: %v", err)
		}
		if err := reactivate.CreateFontArtifact(ctx, retired); err != nil {
			t.Fatalf("reactivate stale artifact within capacity: %v", err)
		}
		if err := firstPool.QueryRow(ctx, `
			SELECT artifact.status, job.status
			FROM font_artifacts AS artifact JOIN font_build_jobs AS job USING (artifact_key)
			WHERE artifact.artifact_key = $1`, retired.Key).Scan(&status, &jobStatus); err != nil {
			t.Fatalf("read admitted stale reactivation: %v", err)
		}
		if status != "pending" || jobStatus != "pending" {
			t.Fatalf("admitted stale reactivation state = artifact %q job %q", status, jobStatus)
		}
	})

	t.Run("acquire locks job before artifact", func(t *testing.T) {
		baselineCount, baselineBytes := usage(t)
		target := artifact("acquire-lock-order")
		defer cleanupArtifacts(t, target)
		cfg := FontRepositoryConfig{
			MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 1000,
			ArtifactReservation: 100,
		}
		setup := NewFontRepositoryFromPool(firstPool, cfg)
		if err := setup.CreateFontArtifact(ctx, target); err != nil {
			t.Fatalf("create lock-order artifact: %v", err)
		}
		initialClaim, acquired, err := setup.AcquireBuildJob(ctx, target.Key, "quota-lock-order-initial", time.Minute)
		if err != nil || !acquired {
			t.Fatalf("initial lock-order acquire = %t, %v", acquired, err)
		}
		if _, err := firstPool.Exec(ctx, `
			UPDATE font_build_jobs
			SET lease_until = now() - interval '1 second'
			WHERE artifact_key = $1`, target.Key); err != nil {
			t.Fatalf("expire lock-order lease: %v", err)
		}

		tx, err := firstPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin lock-order transaction: %v", err)
		}
		defer func() { _ = tx.Rollback(context.Background()) }()
		var lockedKey string
		if err := tx.QueryRow(ctx, `
			SELECT artifact_key
			FROM font_build_jobs
			WHERE artifact_key = $1
			FOR UPDATE`, target.Key).Scan(&lockedKey); err != nil {
			t.Fatalf("lock build job: %v", err)
		}

		applicationName := "quota-lock-order-" + suffix
		poolConfig, err := pgxpool.ParseConfig(databaseURL)
		if err != nil {
			t.Fatalf("parse lock-order pool config: %v", err)
		}
		poolConfig.ConnConfig.RuntimeParams["application_name"] = applicationName
		poolConfig.MaxConns = 1
		takeoverPool, err := pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			t.Fatalf("open lock-order takeover pool: %v", err)
		}
		defer takeoverPool.Close()
		takeover := NewFontRepositoryFromPool(takeoverPool, cfg)
		type acquireResult struct {
			claim    domain.BuildClaim
			acquired bool
			err      error
		}
		resultChannel := make(chan acquireResult, 1)
		acquireCtx, acquireCancel := context.WithTimeout(ctx, 5*time.Second)
		defer acquireCancel()
		go func() {
			claim, acquired, err := takeover.AcquireBuildJob(
				acquireCtx, target.Key, "quota-lock-order-takeover", time.Minute,
			)
			resultChannel <- acquireResult{claim: claim, acquired: acquired, err: err}
		}()

		waiting := false
		waitDeadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(waitDeadline) {
			if err := firstPool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1
					FROM pg_stat_activity
					WHERE application_name = $1
					  AND state = 'active'
					  AND wait_event_type = 'Lock'
				)`, applicationName).Scan(&waiting); err != nil {
				t.Fatalf("observe lock-order waiter: %v", err)
			}
			if waiting {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !waiting {
			_ = tx.Rollback(ctx)
			acquireCancel()
			result := <-resultChannel
			t.Fatalf("takeover did not wait on job lock; result = %#v", result)
		}

		updateCtx, updateCancel := context.WithTimeout(ctx, time.Second)
		_, updateErr := tx.Exec(updateCtx, `
			UPDATE font_artifacts
			SET updated_at = updated_at
			WHERE artifact_key = $1`, target.Key)
		updateCancel()
		if updateErr != nil {
			_ = tx.Rollback(ctx)
			acquireCancel()
			result := <-resultChannel
			t.Fatalf("artifact was locked before job waiter completed: %v; takeover = %#v", updateErr, result)
		}
		if err := tx.Commit(ctx); err != nil {
			acquireCancel()
			result := <-resultChannel
			t.Fatalf("commit lock-order transaction: %v; takeover = %#v", err, result)
		}
		result := <-resultChannel
		if result.err != nil || !result.acquired || result.claim.Fence <= initialClaim.Fence {
			t.Fatalf("lock-order takeover = %#v, initial fence = %d", result, initialClaim.Fence)
		}
	})

	t.Run("create races ready to missing", func(t *testing.T) {
		for iteration := 0; iteration < 8; iteration++ {
			baselineCount, baselineBytes := usage(t)
			ready := artifact("race-ready-" + strconv.Itoa(iteration))
			created := artifact("race-created-" + strconv.Itoa(iteration))
			setup := NewFontRepositoryFromPool(firstPool, FontRepositoryConfig{
				MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 1000,
				ArtifactReservation: 100,
			})
			if err := setup.CreateFontArtifact(ctx, ready); err != nil {
				t.Fatalf("iteration %d create ready artifact: %v", iteration, err)
			}
			claim, acquired, err := setup.AcquireBuildJob(ctx, ready.Key, "quota-race-"+strconv.Itoa(iteration), time.Minute)
			if err != nil || !acquired {
				t.Fatalf("iteration %d acquire ready artifact = %t, %v", iteration, acquired, err)
			}
			publishedKey := ready.ObjectKey + ".published"
			if err := setup.MarkFontArtifactReady(ctx, ready.Key, claim, domain.ArtifactObject{
				ObjectKey: publishedKey, SizeBytes: 1,
			}); err != nil {
				t.Fatalf("iteration %d publish ready artifact: %v", iteration, err)
			}

			cfg := FontRepositoryConfig{
				MaxArtifacts: baselineCount + 10, MaxAccountedBytes: baselineBytes + 101,
				ArtifactReservation: 100,
			}
			createPod := NewFontRepositoryFromPool(firstPool, cfg)
			missingPod := NewFontRepositoryFromPool(secondPool, cfg)
			start := make(chan struct{})
			var wait sync.WaitGroup
			var createErr, missingErr error
			var marked bool
			wait.Add(2)
			go func() {
				defer wait.Done()
				<-start
				createErr = createPod.CreateFontArtifact(ctx, created)
			}()
			go func() {
				defer wait.Done()
				<-start
				marked, missingErr = missingPod.MarkFontArtifactMissing(
					ctx, ready.Key, publishedKey, claim.Fence, "concurrent object absence",
				)
			}()
			close(start)
			wait.Wait()

			createWon := createErr == nil
			missingWon := missingErr == nil && marked
			if createErr != nil && !errors.Is(createErr, domain.ErrArtifactCapacity) {
				t.Fatalf("iteration %d CreateFontArtifact error: %v", iteration, createErr)
			}
			if missingErr != nil && !errors.Is(missingErr, domain.ErrArtifactCapacity) {
				t.Fatalf("iteration %d MarkFontArtifactMissing error: %v", iteration, missingErr)
			}
			if createWon == missingWon {
				t.Fatalf(
					"iteration %d race results = create %v, missing (%t, %v); want exactly one winner",
					iteration, createErr, marked, missingErr,
				)
			}
			_, accountedBytes := usage(t)
			if accountedBytes > cfg.MaxAccountedBytes {
				t.Fatalf("iteration %d accounted bytes = %d, cap = %d", iteration, accountedBytes, cfg.MaxAccountedBytes)
			}
			cleanupArtifacts(t, ready, created)
		}
	})
}

func TestFontRepositoryJobArtifactLockOrderIntegration(t *testing.T) {
	databaseURL := os.Getenv("EMFONT_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EMFONT_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	firstPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open first database pool: %v", err)
	}
	t.Cleanup(firstPool.Close)
	secondPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open second database pool: %v", err)
	}
	t.Cleanup(secondPool.Close)
	if err := FontSchemaReady(ctx, firstPool); err != nil {
		t.Fatalf("FontSchemaReady: %v", err)
	}

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "lock-order-integration-" + suffix
	if _, err := firstPool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`, familyID, "Lock Order Integration "+suffix); err != nil {
		t.Fatalf("insert font family: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := firstPool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID); err != nil {
			t.Errorf("cleanup font family: %v", err)
		}
	})

	repositoryConfig := FontRepositoryConfig{
		MaxArtifacts: 1 << 30, MaxAccountedBytes: 1 << 62, ArtifactReservation: 100,
	}
	artifact := func(label string) domain.Artifact {
		return domain.Artifact{
			Key: "lock-order:" + suffix + ":" + label, Kind: domain.BuildModeDynamic, Status: "pending",
			FamilyID: familyID, Weight: 400, WordHash: label, NormalizedWordSet: label,
			SourceChecksum: "source-" + suffix, BuilderVersion: "lock-order-integration",
			ProtocolVersion: domain.ArtifactProtocolVersion,
			ObjectKey:       "_generated/lock-order-" + suffix + "-" + label + ".woff2",
			ContentType:     domain.ContentTypeWOFF2,
		}
	}
	insertArtifact := func(tb testing.TB, value domain.Artifact, status string, updatedAt time.Time, retiredAt *time.Time) {
		tb.Helper()
		if _, err := firstPool.Exec(ctx, `
			INSERT INTO font_artifacts (
				artifact_key, kind, status, family_id, weight, word_hash, normalized_word_set,
				source_checksum_sha256, builder_version, artifact_protocol_version,
				object_key, content_type, reservation_bytes, updated_at, retired_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 100, $13, $14)`,
			value.Key, value.Kind, status, value.FamilyID, value.Weight, value.WordHash,
			value.NormalizedWordSet, value.SourceChecksum, value.BuilderVersion, value.ProtocolVersion,
			value.ObjectKey, value.ContentType, updatedAt, retiredAt); err != nil {
			tb.Fatalf("insert artifact %q: %v", value.Key, err)
		}
	}
	insertJob := func(tb testing.TB, artifactKey, status, owner string, leaseUntil *time.Time) {
		tb.Helper()
		if _, err := firstPool.Exec(ctx, `
			INSERT INTO font_build_jobs (
				artifact_key, status, attempts, fence, locked_by, lease_until, retryable, updated_at
			) VALUES ($1, $2, 1, 1, NULLIF($3, ''), $4, TRUE, now())`,
			artifactKey, status, owner, leaseUntil); err != nil {
			tb.Fatalf("insert build job %q: %v", artifactKey, err)
		}
	}
	cleanupArtifact := func(tb testing.TB, value domain.Artifact) {
		tb.Helper()
		if _, err := firstPool.Exec(ctx, "DELETE FROM font_artifacts WHERE artifact_key = $1", value.Key); err != nil {
			tb.Fatalf("cleanup artifact %q: %v", value.Key, err)
		}
	}
	newNamedPool := func(tb testing.TB, applicationName string) *pgxpool.Pool {
		tb.Helper()
		config, err := pgxpool.ParseConfig(databaseURL)
		if err != nil {
			tb.Fatalf("parse named pool config: %v", err)
		}
		if config.ConnConfig.RuntimeParams == nil {
			config.ConnConfig.RuntimeParams = make(map[string]string)
		}
		config.ConnConfig.RuntimeParams["application_name"] = applicationName
		config.MaxConns = 1
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			tb.Fatalf("open named database pool: %v", err)
		}
		tb.Cleanup(pool.Close)
		return pool
	}

	t.Run("stale reactivation waits for acquire job lock", func(t *testing.T) {
		target := artifact("controlled-stale-reactivation")
		defer cleanupArtifact(t, target)
		old := time.Now().Add(-48 * time.Hour)
		insertArtifact(t, target, "stale", old, &old)
		insertJob(t, target.Key, "ready", "", nil)

		acquireTx, err := firstPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin acquire-side transaction: %v", err)
		}
		defer func() { _ = acquireTx.Rollback(context.Background()) }()
		if _, err := acquireTx.Exec(ctx, `
			SELECT artifact_key FROM font_build_jobs WHERE artifact_key = $1 FOR UPDATE`, target.Key); err != nil {
			t.Fatalf("lock acquire-side build job: %v", err)
		}

		applicationName := "lock-order-create-" + suffix
		createPool := newNamedPool(t, applicationName)
		createTx, err := createPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin stale reactivation transaction: %v", err)
		}
		defer func() { _ = createTx.Rollback(context.Background()) }()
		createRepository := NewFontRepositoryFromTx(createTx, repositoryConfig)
		createResult := make(chan error, 1)
		go func() {
			createResult <- createRepository.CreateFontArtifact(ctx, target)
		}()

		waiting := false
		waitDeadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(waitDeadline) {
			if err := firstPool.QueryRow(ctx, `
				SELECT EXISTS (
					SELECT 1 FROM pg_stat_activity
					WHERE application_name = $1 AND state = 'active' AND wait_event_type = 'Lock'
				)`, applicationName).Scan(&waiting); err != nil {
				t.Fatalf("observe stale reactivation waiter: %v", err)
			}
			if waiting {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !waiting {
			_ = acquireTx.Rollback(ctx)
			_ = createTx.Rollback(ctx)
			t.Fatalf("stale reactivation did not wait for the acquire-side job lock")
		}

		updateCtx, updateCancel := context.WithTimeout(ctx, 2*time.Second)
		_, updateErr := acquireTx.Exec(updateCtx, `
			UPDATE font_artifacts SET updated_at = updated_at WHERE artifact_key = $1`, target.Key)
		updateCancel()
		if updateErr != nil {
			_ = acquireTx.Rollback(ctx)
			_ = createTx.Rollback(ctx)
			t.Fatalf("stale reactivation locked artifact before job: %v", updateErr)
		}
		if err := acquireTx.Rollback(ctx); err != nil {
			t.Fatalf("release acquire-side locks: %v", err)
		}
		select {
		case err := <-createResult:
			if err != nil {
				t.Fatalf("stale reactivation after job release: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("stale reactivation remained blocked after job release")
		}
		if err := createTx.Commit(ctx); err != nil {
			t.Fatalf("commit stale reactivation: %v", err)
		}

		var artifactStatus, jobStatus string
		if err := firstPool.QueryRow(ctx, `
			SELECT artifact.status, job.status
			FROM font_artifacts AS artifact JOIN font_build_jobs AS job USING (artifact_key)
			WHERE artifact.artifact_key = $1`, target.Key).Scan(&artifactStatus, &jobStatus); err != nil {
			t.Fatalf("read stale reactivation state: %v", err)
		}
		if artifactStatus != "pending" || jobStatus != "pending" {
			t.Fatalf("stale reactivation state = artifact %q job %q", artifactStatus, jobStatus)
		}
	})

	t.Run("retire pending skips acquire job lock", func(t *testing.T) {
		target := artifact("controlled-retire-pending")
		defer cleanupArtifact(t, target)
		old := time.Now().Add(-48 * time.Hour)
		expiredLease := time.Now().Add(-time.Minute)
		insertArtifact(t, target, "pending", old, nil)
		insertJob(t, target.Key, "running", "expired-owner", &expiredLease)

		acquireTx, err := firstPool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin acquire phase transaction: %v", err)
		}
		defer func() { _ = acquireTx.Rollback(context.Background()) }()
		if _, err := acquireTx.Exec(ctx, `
			UPDATE font_build_jobs
			SET locked_by = 'new-owner', lease_until = now() + interval '1 minute', fence = fence + 1
			WHERE artifact_key = $1`, target.Key); err != nil {
			t.Fatalf("lock and advance acquire-side job: %v", err)
		}

		retirePool := newNamedPool(t, "lock-order-retire-"+suffix)
		retireTx, err := retirePool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin retirement transaction: %v", err)
		}
		defer func() { _ = retireTx.Rollback(context.Background()) }()
		retireRepository := NewFontRepositoryFromTx(retireTx)
		type retireResult struct {
			rows int64
			err  error
		}
		retired := make(chan retireResult, 1)
		go func() {
			rows, err := retireRepository.RetireFontArtifacts(ctx, time.Now().Add(-24*time.Hour), time.Now(), 1)
			retired <- retireResult{rows: rows, err: err}
		}()
		select {
		case result := <-retired:
			if result.err != nil || result.rows != 0 {
				t.Fatalf("retirement while acquire held job = %d, %v; want 0, nil", result.rows, result.err)
			}
		case <-time.After(3 * time.Second):
			_ = acquireTx.Rollback(ctx)
			result := <-retired
			t.Fatalf("retirement waited behind acquire job lock: %#v", result)
		}
		if err := retireTx.Commit(ctx); err != nil {
			t.Fatalf("commit skipped retirement: %v", err)
		}

		updateCtx, updateCancel := context.WithTimeout(ctx, 2*time.Second)
		_, updateErr := acquireTx.Exec(updateCtx, `
			UPDATE font_artifacts
			SET status = 'running', updated_at = now()
			WHERE artifact_key = $1`, target.Key)
		updateCancel()
		if updateErr != nil {
			_ = acquireTx.Rollback(ctx)
			t.Fatalf("retirement locked pending artifact after skipping job: %v", updateErr)
		}
		if err := acquireTx.Commit(ctx); err != nil {
			t.Fatalf("commit acquire-side transition: %v", err)
		}

		var artifactStatus, jobStatus, owner string
		if err := firstPool.QueryRow(ctx, `
			SELECT artifact.status, job.status, job.locked_by
			FROM font_artifacts AS artifact JOIN font_build_jobs AS job USING (artifact_key)
			WHERE artifact.artifact_key = $1`, target.Key).Scan(&artifactStatus, &jobStatus, &owner); err != nil {
			t.Fatalf("read acquire-side state: %v", err)
		}
		if artifactStatus != "running" || jobStatus != "running" || owner != "new-owner" {
			t.Fatalf("acquire-side state = artifact %q job %q owner %q", artifactStatus, jobStatus, owner)
		}
	})

	t.Run("high contention state races", func(t *testing.T) {
		createRepository := NewFontRepositoryFromPool(firstPool, repositoryConfig)
		acquireRepository := NewFontRepositoryFromPool(secondPool, repositoryConfig)
		retireRepository := NewFontRepositoryFromPool(firstPool)
		const iterations = 64

		for iteration := 0; iteration < iterations; iteration++ {
			target := artifact("race-new-" + strconv.Itoa(iteration))
			start := make(chan struct{})
			createResult := make(chan error, 1)
			type acquireResult struct {
				acquired bool
				err      error
			}
			acquiredResult := make(chan acquireResult, 1)
			go func() {
				<-start
				createResult <- createRepository.CreateFontArtifact(ctx, target)
			}()
			go func() {
				<-start
				_, acquired, err := acquireRepository.AcquireBuildJob(ctx, target.Key, "new-race", time.Minute)
				acquiredResult <- acquireResult{acquired: acquired, err: err}
			}()
			close(start)
			if err := <-createResult; err != nil {
				t.Fatalf("new artifact race %d create: %v", iteration, err)
			}
			result := <-acquiredResult
			if result.err != nil {
				t.Fatalf("new artifact race %d acquire: %v", iteration, result.err)
			}
			if !result.acquired {
				if _, acquired, err := acquireRepository.AcquireBuildJob(ctx, target.Key, "new-race-retry", time.Minute); err != nil || !acquired {
					t.Fatalf("new artifact race %d eventual acquire = %t, %v", iteration, acquired, err)
				}
			}
			var jobCount int64
			if err := firstPool.QueryRow(ctx, `
				SELECT COUNT(*) FROM font_build_jobs WHERE artifact_key = $1`, target.Key).Scan(&jobCount); err != nil {
				t.Fatalf("new artifact race %d count jobs: %v", iteration, err)
			}
			if jobCount != 1 {
				t.Fatalf("new artifact race %d job count = %d, want 1", iteration, jobCount)
			}
			cleanupArtifact(t, target)
		}

		for iteration := 0; iteration < iterations; iteration++ {
			target := artifact("race-stale-" + strconv.Itoa(iteration))
			old := time.Now().Add(-48 * time.Hour)
			insertArtifact(t, target, "stale", old, &old)
			insertJob(t, target.Key, "ready", "", nil)
			start := make(chan struct{})
			createResult := make(chan error, 1)
			type acquireResult struct {
				acquired bool
				err      error
			}
			acquiredResult := make(chan acquireResult, 1)
			go func() {
				<-start
				createResult <- createRepository.CreateFontArtifact(ctx, target)
			}()
			go func() {
				<-start
				_, acquired, err := acquireRepository.AcquireBuildJob(ctx, target.Key, "stale-race", time.Minute)
				acquiredResult <- acquireResult{acquired: acquired, err: err}
			}()
			close(start)
			if err := <-createResult; err != nil {
				t.Fatalf("stale race %d reactivate: %v", iteration, err)
			}
			result := <-acquiredResult
			if result.err != nil {
				t.Fatalf("stale race %d acquire: %v", iteration, result.err)
			}
			if !result.acquired {
				if _, acquired, err := acquireRepository.AcquireBuildJob(ctx, target.Key, "stale-race-retry", time.Minute); err != nil || !acquired {
					t.Fatalf("stale race %d eventual acquire = %t, %v", iteration, acquired, err)
				}
			}
			cleanupArtifact(t, target)
		}

		for iteration := 0; iteration < iterations; iteration++ {
			target := artifact("race-retire-" + strconv.Itoa(iteration))
			old := time.Date(2, time.January, 1, 0, 0, iteration, 0, time.UTC)
			expiredLease := time.Now().Add(-time.Minute)
			insertArtifact(t, target, "pending", old, nil)
			insertJob(t, target.Key, "running", "expired-race", &expiredLease)
			start := make(chan struct{})
			type acquireResult struct {
				acquired bool
				err      error
			}
			type retireResult struct {
				rows int64
				err  error
			}
			acquiredResult := make(chan acquireResult, 1)
			retiredResult := make(chan retireResult, 1)
			go func() {
				<-start
				_, acquired, err := acquireRepository.AcquireBuildJob(ctx, target.Key, "retire-race", time.Minute)
				acquiredResult <- acquireResult{acquired: acquired, err: err}
			}()
			go func() {
				<-start
				rows, err := retireRepository.RetireFontArtifacts(
					ctx, time.Date(3, time.January, 1, 0, 0, 0, 0, time.UTC), time.Now(), 1,
				)
				retiredResult <- retireResult{rows: rows, err: err}
			}()
			close(start)
			acquireOutcome := <-acquiredResult
			retireOutcome := <-retiredResult
			if acquireOutcome.err != nil || retireOutcome.err != nil {
				t.Fatalf(
					"retire race %d errors = acquire %v, retire %v",
					iteration, acquireOutcome.err, retireOutcome.err,
				)
			}
			var artifactStatus, jobStatus string
			if err := firstPool.QueryRow(ctx, `
				SELECT artifact.status, job.status
				FROM font_artifacts AS artifact JOIN font_build_jobs AS job USING (artifact_key)
				WHERE artifact.artifact_key = $1`, target.Key).Scan(&artifactStatus, &jobStatus); err != nil {
				t.Fatalf("retire race %d read state: %v", iteration, err)
			}
			switch {
			case acquireOutcome.acquired:
				if retireOutcome.rows != 0 || artifactStatus != "running" || jobStatus != "running" {
					t.Fatalf(
						"retire race %d acquire winner = rows %d artifact %q job %q",
						iteration, retireOutcome.rows, artifactStatus, jobStatus,
					)
				}
			case retireOutcome.rows == 1:
				if artifactStatus != "stale" || jobStatus != "failed" {
					t.Fatalf(
						"retire race %d retire winner = artifact %q job %q",
						iteration, artifactStatus, jobStatus,
					)
				}
			default:
				t.Fatalf(
					"retire race %d had no winner: acquired %t rows %d artifact %q job %q",
					iteration, acquireOutcome.acquired, retireOutcome.rows, artifactStatus, jobStatus,
				)
			}
			cleanupArtifact(t, target)
		}
	})
}

func TestFontRepositoryCleanupIntegration(t *testing.T) {
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

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "cleanup-integration-" + suffix
	if _, err := pool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`, familyID, "Cleanup Integration "+suffix); err != nil {
		t.Fatalf("insert font family: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID); err != nil {
			t.Errorf("cleanup font family: %v", err)
		}
	})

	sourceKey := "_generated/cleanup-" + suffix + "/source.ttf"
	if _, err := pool.Exec(ctx, `
		INSERT INTO font_sources (family_id, weight, format, object_key)
		VALUES ($1, 400, 'ttf', $2)`, familyID, sourceKey); err != nil {
		t.Fatalf("insert font source: %v", err)
	}

	now := time.Now().UTC().Truncate(time.Microsecond)
	artifactKeys := map[string]string{
		"old-ready":       "_generated/cleanup-" + suffix + "/old-ready.woff2",
		"fresh-ready":     "_generated/cleanup-" + suffix + "/fresh-ready.woff2",
		"old-running":     "_generated/cleanup-" + suffix + "/old-running.woff2",
		"old-failed":      "_generated/cleanup-" + suffix + "/old-failed.woff2",
		"grace-stale":     "_generated/cleanup-" + suffix + "/grace-stale.woff2",
		"unstamped-stale": "_generated/cleanup-" + suffix + "/unstamped-stale.woff2",
		"expired-stale":   "_generated/cleanup-" + suffix + "/expired-stale.woff2",
	}
	insertArtifact := func(name, status string, updatedAt time.Time, retiredAt *time.Time) {
		t.Helper()
		artifactKey := "cleanup:" + name + ":" + suffix
		if _, err := pool.Exec(ctx, `
			INSERT INTO font_artifacts (
				artifact_key, kind, status, family_id, weight, builder_version,
				object_key, content_type, updated_at, retired_at
			) VALUES ($1, 'dynamic', $2, $3, 400, 'cleanup-integration', $4, 'font/woff2', $5, $6)`,
			artifactKey, status, familyID, artifactKeys[name], updatedAt, retiredAt); err != nil {
			t.Fatalf("insert %s artifact: %v", name, err)
		}
	}
	old := now.Add(-48 * time.Hour)
	graceRetiredAt := now.Add(-30 * time.Minute)
	expiredRetiredAt := now.Add(-3 * time.Hour)
	insertArtifact("old-ready", "ready", old, nil)
	insertArtifact("fresh-ready", "ready", now.Add(-time.Hour), nil)
	insertArtifact("old-running", "running", old, nil)
	insertArtifact("old-failed", "failed", old, nil)
	insertArtifact("grace-stale", "stale", graceRetiredAt, &graceRetiredAt)
	insertArtifact("unstamped-stale", "stale", now.Add(-time.Hour), nil)
	insertArtifact("expired-stale", "stale", expiredRetiredAt, &expiredRetiredAt)

	repository := NewFontRepositoryFromPool(pool)
	current, err := repository.TouchFontArtifact(
		ctx,
		"cleanup:old-ready:"+suffix,
		artifactKeys["old-ready"],
		0,
		time.Minute,
	)
	if err != nil || !current {
		t.Fatalf("TouchFontArtifact = %t, %v; want true, nil", current, err)
	}
	current, err = repository.TouchFontArtifact(
		ctx,
		"cleanup:old-ready:"+suffix,
		artifactKeys["old-ready"],
		1,
		time.Minute,
	)
	if err != nil || current {
		t.Fatalf("stale TouchFontArtifact = %t, %v; want false, nil", current, err)
	}
	var retired int64
	for {
		rows, err := repository.RetireFontArtifacts(ctx, now.Add(-24*time.Hour), now, 1)
		if err != nil {
			t.Fatalf("RetireFontArtifacts: %v", err)
		}
		if rows > 1 {
			t.Fatalf("retired rows = %d, want at most 1", rows)
		}
		retired += rows
		if rows == 0 {
			break
		}
	}
	if retired != 2 {
		t.Fatalf("retired rows = %d, want 2", retired)
	}

	assertArtifactState := func(name, wantStatus string, wantRetired bool) {
		t.Helper()
		var status string
		var retiredAt pgtype.Timestamptz
		err := pool.QueryRow(ctx, `
			SELECT status, retired_at
			FROM font_artifacts
			WHERE artifact_key = $1`, "cleanup:"+name+":"+suffix).Scan(&status, &retiredAt)
		if err != nil {
			t.Fatalf("query %s artifact: %v", name, err)
		}
		if status != wantStatus || retiredAt.Valid != wantRetired {
			t.Fatalf("%s artifact state = %q, %v; want %q, retired=%t", name, status, retiredAt, wantStatus, wantRetired)
		}
	}
	assertArtifactState("old-ready", "ready", false)
	assertArtifactState("old-failed", "stale", true)
	assertArtifactState("unstamped-stale", "stale", true)
	assertArtifactState("fresh-ready", "ready", false)
	assertArtifactState("old-running", "running", false)

	allObjectKeys := []string{sourceKey}
	for _, objectKey := range artifactKeys {
		allObjectKeys = append(allObjectKeys, objectKey)
	}
	slices.Sort(allObjectKeys)
	references, err := repository.FindReferencedFontObjectKeys(ctx, allObjectKeys)
	if err != nil {
		t.Fatalf("FindReferencedFontObjectKeys before deletion: %v", err)
	}
	if !slices.Equal(references, allObjectKeys) {
		t.Fatalf("references before deletion = %#v, want %#v", references, allObjectKeys)
	}

	rows, err := repository.DeleteRetiredFontArtifacts(ctx, now.Add(-time.Hour), 1)
	if err != nil || rows != 1 {
		t.Fatalf("DeleteRetiredFontArtifacts expired = %d, %v; want 1, nil", rows, err)
	}
	rows, err = repository.DeleteRetiredFontArtifacts(ctx, now.Add(-time.Hour), 1)
	if err != nil || rows != 0 {
		t.Fatalf("idempotent DeleteRetiredFontArtifacts = %d, %v; want 0, nil", rows, err)
	}

	reclaimedKey := "cleanup:old-failed:" + suffix
	if err := repository.CreateFontArtifact(ctx, domain.Artifact{
		Key: reclaimedKey, Kind: domain.BuildModeDynamic, Status: "pending",
		FamilyID: familyID, Weight: 400, BuilderVersion: "cleanup-integration",
		ProtocolVersion: "v1", ObjectKey: "_generated/reclaimed-base.woff2",
		ContentType: domain.ContentTypeWOFF2,
	}); err != nil {
		t.Fatalf("CreateFontArtifact reclaimed artifact: %v", err)
	}
	assertArtifactState("old-failed", "pending", false)
	if _, acquired, err := repository.AcquireBuildJob(ctx, reclaimedKey, "cleanup-worker", time.Minute); err != nil || !acquired {
		t.Fatalf("AcquireBuildJob reclaimed artifact = %t, %v; want true, nil", acquired, err)
	}
	assertArtifactState("old-failed", "running", false)

	rows, err = repository.DeleteRetiredFontArtifacts(ctx, now.Add(time.Hour), 10)
	if err != nil || rows != 2 {
		t.Fatalf("DeleteRetiredFontArtifacts after grace = %d, %v; want 2, nil", rows, err)
	}
	rows, err = repository.DeleteRetiredFontArtifacts(ctx, now.Add(time.Hour), 10)
	if err != nil || rows != 0 {
		t.Fatalf("repeated DeleteRetiredFontArtifacts = %d, %v; want 0, nil", rows, err)
	}

	wantReferences := []string{
		sourceKey,
		artifactKeys["fresh-ready"],
		artifactKeys["old-ready"],
		artifactKeys["old-failed"],
		artifactKeys["old-running"],
	}
	slices.Sort(wantReferences)
	references, err = repository.FindReferencedFontObjectKeys(ctx, allObjectKeys)
	if err != nil {
		t.Fatalf("FindReferencedFontObjectKeys after deletion: %v", err)
	}
	if !slices.Equal(references, wantReferences) {
		t.Fatalf("references after deletion = %#v, want %#v", references, wantReferences)
	}
	assertArtifactState("fresh-ready", "ready", false)
}

func TestFontRepositoryRetireFontArtifactsBoundsExpiredJobLocksIntegration(t *testing.T) {
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

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "retire-lock-integration-" + suffix
	if _, err := pool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`, familyID, "Retire Lock Integration "+suffix); err != nil {
		t.Fatalf("insert font family: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID); err != nil {
			t.Errorf("cleanup font family: %v", err)
		}
	})

	now := time.Now().UTC().Truncate(time.Microsecond)
	artifactKeys := make([]string, 3)
	for index := range artifactKeys {
		artifactKey := "retire-lock:" + suffix + ":" + strconv.Itoa(index)
		artifactKeys[index] = artifactKey
		updatedAt := now.Add(-48*time.Hour + time.Duration(index)*time.Microsecond)
		if _, err := pool.Exec(ctx, `
			INSERT INTO font_artifacts (
				artifact_key, kind, status, family_id, weight, builder_version,
				object_key, content_type, updated_at
			) VALUES ($1, 'dynamic', 'running', $2, 400, 'retire-lock-integration', $3, 'font/woff2', $4)`,
			artifactKey, familyID, "_generated/retire-lock-"+suffix+"/"+strconv.Itoa(index)+".woff2", updatedAt); err != nil {
			t.Fatalf("insert artifact %d: %v", index, err)
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO font_build_jobs (
				artifact_key, status, attempts, locked_by, lease_until, started_at, updated_at
			) VALUES ($1, 'running', 1, 'retire-lock-test', $2, $3, $3)`,
			artifactKey, now.Add(-time.Minute), updatedAt); err != nil {
			t.Fatalf("insert expired job %d: %v", index, err)
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin retirement transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	repository := NewFontRepositoryFromTx(tx)
	rows, err := repository.RetireFontArtifacts(ctx, now.Add(-24*time.Hour), now, 1)
	if err != nil || rows != 1 {
		t.Fatalf("RetireFontArtifacts = %d, %v; want 1, nil", rows, err)
	}

	lockCheckCtx, lockCheckCancel := context.WithTimeout(context.Background(), time.Second)
	defer lockCheckCancel()
	if _, err := pool.Exec(lockCheckCtx, `
		UPDATE font_build_jobs
		SET updated_at = now()
		WHERE artifact_key = $1`, artifactKeys[2]); err != nil {
		t.Fatalf("expired job beyond batch remained locked: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit retirement transaction: %v", err)
	}

	var staleArtifacts, runningJobs int64
	if err := pool.QueryRow(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE artifact.status = 'stale'),
			COUNT(*) FILTER (WHERE job.status = 'running')
		FROM font_artifacts AS artifact
		JOIN font_build_jobs AS job USING (artifact_key)
		WHERE artifact.artifact_key = ANY($1::text[])`, artifactKeys).Scan(&staleArtifacts, &runningJobs); err != nil {
		t.Fatalf("query retirement states: %v", err)
	}
	if staleArtifacts != 1 || runningJobs != 2 {
		t.Fatalf("retirement states = stale:%d running:%d; want stale:1 running:2", staleArtifacts, runningJobs)
	}
}

func TestFontRepositoryLifecycleFencingIntegration(t *testing.T) {
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

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "lifecycle-integration-" + suffix
	if _, err := pool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], 'ttf')`, familyID, "Lifecycle Integration "+suffix); err != nil {
		t.Fatalf("insert font family: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, "DELETE FROM font_family WHERE id = $1", familyID); err != nil {
			t.Errorf("cleanup font family: %v", err)
		}
	})

	repository := NewFontRepositoryFromPool(pool)
	artifact := domain.Artifact{
		Key: "lifecycle:" + suffix, Kind: domain.BuildModeDynamic, Status: "pending",
		FamilyID: familyID, Weight: 400, WordHash: suffix, NormalizedWordSet: "AB",
		SourceChecksum: "source-" + suffix, BuilderVersion: "lifecycle-integration",
		ProtocolVersion: domain.ArtifactProtocolVersion,
		ObjectKey:       "_generated/lifecycle-" + suffix + ".woff2", ContentType: domain.ContentTypeWOFF2,
	}
	if err := repository.CreateFontArtifact(ctx, artifact); err != nil {
		t.Fatalf("CreateFontArtifact: %v", err)
	}
	firstClaim, acquired, err := repository.AcquireBuildJob(ctx, artifact.Key, "lifecycle:first", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("first AcquireBuildJob = %t, %v", acquired, err)
	}
	if err := repository.MarkFontArtifactReady(ctx, artifact.Key, firstClaim, domain.ArtifactObject{
		ObjectKey: artifact.ObjectKey, VersionID: "version-1", SizeBytes: 10,
		ETag: "etag", ChecksumSHA256: strings.Repeat("1", 64),
	}); err != nil {
		t.Fatalf("first MarkFontArtifactReady: %v", err)
	}

	retiredAt := time.Now().Add(-3 * time.Hour)
	if _, err := pool.Exec(ctx, `
		UPDATE font_artifacts
		SET status = 'stale', retired_at = $2, updated_at = $2
		WHERE artifact_key = $1`, artifact.Key, retiredAt); err != nil {
		t.Fatalf("retire ready artifact: %v", err)
	}
	if err := repository.CreateFontArtifact(ctx, artifact); err != nil {
		t.Fatalf("reclaim stale artifact: %v", err)
	}
	secondClaim, acquired, err := repository.AcquireBuildJob(ctx, artifact.Key, "lifecycle:second", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("ready-job stale-artifact AcquireBuildJob = %t, %v", acquired, err)
	}
	if secondClaim.Fence <= firstClaim.Fence {
		t.Fatalf("second fence = %d, first fence = %d", secondClaim.Fence, firstClaim.Fence)
	}
	if err := repository.MarkFontArtifactReady(ctx, artifact.Key, secondClaim, domain.ArtifactObject{
		ObjectKey: artifact.ObjectKey, VersionID: "version-2", SizeBytes: 10,
		ETag: "etag", ChecksumSHA256: strings.Repeat("1", 64),
	}); err != nil {
		t.Fatalf("second MarkFontArtifactReady: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE font_artifacts
		SET status = 'stale', retired_at = $2, updated_at = $2
		WHERE artifact_key = $1`, artifact.Key, retiredAt); err != nil {
		t.Fatalf("retire artifact before row deletion: %v", err)
	}
	if rows, err := repository.DeleteRetiredFontArtifacts(ctx, time.Now().Add(-time.Hour), 1); err != nil || rows != 1 {
		t.Fatalf("DeleteRetiredFontArtifacts = %d, %v; want 1, nil", rows, err)
	}
	if err := repository.CreateFontArtifact(ctx, artifact); err != nil {
		t.Fatalf("recreate deleted artifact: %v", err)
	}
	thirdClaim, acquired, err := repository.AcquireBuildJob(ctx, artifact.Key, "lifecycle:third", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("third AcquireBuildJob = %t, %v", acquired, err)
	}
	if thirdClaim.Fence <= secondClaim.Fence {
		t.Fatalf("recreated fence = %d, pre-delete fence = %d", thirdClaim.Fence, secondClaim.Fence)
	}
	if err := repository.MarkFontArtifactReady(ctx, artifact.Key, thirdClaim, domain.ArtifactObject{
		ObjectKey: artifact.ObjectKey, VersionID: "version-3", SizeBytes: 10,
		ETag: "etag", ChecksumSHA256: strings.Repeat("1", 64),
	}); err != nil {
		t.Fatalf("third MarkFontArtifactReady: %v", err)
	}
	if marked, err := repository.MarkFontArtifactMissing(
		ctx, artifact.Key, artifact.ObjectKey, secondClaim.Fence, "delayed pre-delete observer",
	); err != nil || marked {
		t.Fatalf("old-generation MarkFontArtifactMissing = %t, %v; want false, nil", marked, err)
	}

	expired := artifact
	expired.Key = "lifecycle:expired:" + suffix
	expired.WordHash = "expired-" + suffix
	expired.ObjectKey = "_generated/lifecycle-expired-" + suffix + ".woff2"
	if err := repository.CreateFontArtifact(ctx, expired); err != nil {
		t.Fatalf("create expired-running artifact: %v", err)
	}
	if _, acquired, err := repository.AcquireBuildJob(ctx, expired.Key, "lifecycle:expired", time.Minute); err != nil || !acquired {
		t.Fatalf("AcquireBuildJob expired-running = %t, %v", acquired, err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if _, err := pool.Exec(ctx, "UPDATE font_artifacts SET updated_at = $2 WHERE artifact_key = $1", expired.Key, old); err != nil {
		t.Fatalf("age running artifact: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE font_build_jobs SET lease_until = $2, updated_at = $2 WHERE artifact_key = $1`, expired.Key, old); err != nil {
		t.Fatalf("expire running job: %v", err)
	}
	if rows, err := repository.RetireFontArtifacts(ctx, time.Now().Add(-24*time.Hour), time.Now(), 10); err != nil || rows != 1 {
		t.Fatalf("RetireFontArtifacts expired-running = %d, %v; want 1, nil", rows, err)
	}
	var artifactStatus, jobStatus string
	if err := pool.QueryRow(ctx, `
		SELECT artifact.status, job.status
		FROM font_artifacts AS artifact
		JOIN font_build_jobs AS job USING (artifact_key)
		WHERE artifact.artifact_key = $1`, expired.Key).Scan(&artifactStatus, &jobStatus); err != nil {
		t.Fatalf("read expired-running state: %v", err)
	}
	if artifactStatus != "stale" || jobStatus != "failed" {
		t.Fatalf("expired-running state = artifact %q, job %q", artifactStatus, jobStatus)
	}
}
