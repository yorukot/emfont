package integration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzz"
	miniostore "github.com/emfont/emfont/backend/internal/controller/infrastructure/objectstore/minio"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/postgres"
	httptransport "github.com/emfont/emfont/backend/internal/controller/transport/http"
	domainfont "github.com/emfont/emfont/backend/internal/domain/font"
	"github.com/jackc/pgx/v5/pgxpool"
	miniogo "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const officialTestText = "測試字型ABC"

var officialReferenceTexts = []string{
	officialTestText,
	"繁體中文網頁字型",
	"天地玄黃宇宙洪荒",
	"0123456789AaZz",
	"!?.,:;()[]{}+-=%",
	"台灣香港澳門",
	"日本語かなカナ",
	"café naïve résumé",
	"ㄅㄆㄇㄈ注音",
	"「標點，測試。」",
	"你好世界",
	"快取並行故障恢復",
}

func TestOfficialFontEndToEndMatchesHarfBuzzCLI(t *testing.T) {
	databaseURL := requiredEnv(t, "EMFONT_TEST_DATABASE_URL")
	minioEndpoint := requiredEnv(t, "EMFONT_TEST_MINIO_ENDPOINT")
	fontPath := requiredEnv(t, "EMFONT_TEST_FONT_PATH")
	accessKey := envOrDefault("EMFONT_TEST_MINIO_ACCESS_KEY", "minioadmin")
	secretKey := envOrDefault("EMFONT_TEST_MINIO_SECRET_KEY", "minioadmin")
	workerPath := integrationWorkerPath(t)

	source, err := os.ReadFile(fontPath)
	if err != nil {
		t.Fatalf("read official font: %v", err)
	}
	sourceSum := sha256.Sum256(source)
	sourceHash := hex.EncodeToString(sourceSum[:])
	if expected := os.Getenv("EMFONT_TEST_FONT_SHA256"); expected != "" && !strings.EqualFold(expected, sourceHash) {
		t.Fatalf("official font SHA-256 = %s, want %s", sourceHash, expected)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	familyID := "OfficialNotoSansTC" + suffix
	bucket := "emfont-e2e-" + strings.ToLower(suffix)
	sourceKey := domainfont.OriginalObjectKey(familyID, 400, "ttf")

	minioClient, err := miniogo.New(minioEndpoint, &miniogo.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: false,
	})
	if err != nil {
		t.Fatalf("create MinIO client: %v", err)
	}
	if err := minioClient.MakeBucket(ctx, bucket, miniogo.MakeBucketOptions{}); err != nil {
		t.Fatalf("create MinIO bucket: %v", err)
	}
	if err := minioClient.EnableVersioning(ctx, bucket); err != nil {
		t.Fatalf("enable MinIO bucket versioning: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for object := range minioClient.ListObjects(cleanupCtx, bucket, miniogo.ListObjectsOptions{Recursive: true, WithVersions: true}) {
			if object.Err == nil {
				_ = minioClient.RemoveObject(cleanupCtx, bucket, object.Key, miniogo.RemoveObjectOptions{VersionID: object.VersionID})
			}
		}
		_ = minioClient.RemoveBucket(cleanupCtx, bucket)
	})

	if _, err := minioClient.PutObject(ctx, bucket, sourceKey, bytes.NewReader(source), int64(len(source)), miniogo.PutObjectOptions{
		ContentType:  "font/ttf",
		UserMetadata: map[string]string{"sha256": sourceHash},
	}); err != nil {
		t.Fatalf("upload official source font: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO font_family (id, name, weights, version, format)
		VALUES ($1, $2, ARRAY[400]::SMALLINT[], $3, 'ttf')`,
		familyID, "Official Noto Sans TC "+suffix, "google-fonts-e2e"); err != nil {
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
		INSERT INTO font_sources (family_id, weight, format, object_key, checksum_sha256, size_bytes, source_version)
		VALUES ($1, 400, 'ttf', $2, $3, $4, $5)`,
		familyID, sourceKey, sourceHash, len(source), "google-fonts-e2e"); err != nil {
		t.Fatalf("insert font source: %v", err)
	}

	objects, err := miniostore.New(miniostore.Config{
		Endpoint: minioEndpoint, AccessKey: accessKey, SecretKey: secretKey,
		Bucket: bucket, PresignExpiry: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("create object store: %v", err)
	}
	builderConfig := harfbuzz.DefaultConfig()
	builderConfig.WorkerPath = workerPath
	builder, err := harfbuzz.NewWithConfig(builderConfig)
	if err != nil {
		t.Fatalf("configure font worker: %v", err)
	}
	if err := builder.Available(); err != nil {
		t.Fatalf("font worker is unavailable: %v", err)
	}
	service, err := appfont.NewService(
		postgres.NewFontRepositoryFromPool(pool),
		objects,
		builder,
		appfont.Config{
			BuilderVersion: "official-cli-e2e", BuildLease: 45 * time.Second,
			BuildTimeout: 30 * time.Second, StaticBuildConcurrency: 2, WorkerID: "official-e2e",
		},
	)
	if err != nil {
		t.Fatalf("create font service: %v", err)
	}
	server := httptest.NewServer(httptransport.NewRouter(httptransport.Dependencies{
		APIVersion: "v1", FontService: service, RequestTimeout: 40 * time.Second,
	}))
	defer server.Close()

	firstFont := requestDynamicSubset(t, server.URL, familyID, officialTestText)
	secondFont := requestDynamicSubset(t, server.URL, familyID, officialTestText)
	if !bytes.Equal(firstFont, secondFont) {
		t.Fatal("cache hit returned different WOFF2 bytes")
	}
	fallbackFont, fallbackResponse := requestSubset(t, server.URL, familyID, officialTestText, false)
	if fallbackResponse.BuildMode != domainfont.BuildModeDynamic || !bytes.Equal(firstFont, fallbackFont) {
		t.Fatalf("incomplete static coverage did not fall back to the cached dynamic artifact: response=%#v", fallbackResponse)
	}
	if !bytes.HasPrefix(firstFont, []byte("wOF2")) {
		t.Fatalf("generated font magic = %q, want wOF2", firstFont[:min(4, len(firstFont))])
	}

	generatedSum := sha256.Sum256(firstFont)
	generatedHash := hex.EncodeToString(generatedSum[:])
	referenceFont, referenceHash := officialReference(t, fontPath, officialTestText)
	if !bytes.Equal(firstFont, referenceFont) {
		t.Fatalf("generated WOFF2 SHA-256 = %s, official hb-subset + woff2_compress SHA-256 = %s", generatedHash, referenceHash)
	}
	if requestCount, _ := strconv.Atoi(os.Getenv("EMFONT_TEST_WARM_CACHE_REQUESTS")); requestCount > 0 {
		measureWarmCache(t, server.URL, familyID, officialTestText, requestCount, 16)
	}

	var attempts int
	if err := pool.QueryRow(ctx, `
		SELECT job.attempts
		FROM font_build_jobs AS job
		JOIN font_artifacts AS artifact ON artifact.artifact_key = job.artifact_key
		WHERE artifact.family_id = $1`, familyID).Scan(&attempts); err != nil {
		t.Fatalf("read build attempts: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("build attempts = %d after repeated identical requests, want 1", attempts)
	}

	assertSubsetShapesText(t, firstFont, officialTestText)
	for _, text := range officialReferenceTexts[1:] {
		generated := requestDynamicSubset(t, server.URL, familyID, text)
		reference, referenceHash := officialReference(t, fontPath, text)
		if !bytes.Equal(generated, reference) {
			generatedSum := sha256.Sum256(generated)
			t.Fatalf(
				"generated WOFF2 for %q SHA-256 = %s, official reference SHA-256 = %s",
				text,
				hex.EncodeToString(generatedSum[:]),
				referenceHash,
			)
		}
		assertSubsetShapesText(t, generated, text)
	}
	t.Logf("source SHA-256: %s", sourceHash)
	t.Logf("generated/reference WOFF2 SHA-256: %s", generatedHash)
	t.Logf(
		"generated WOFF2 bytes: %d; build attempts after repeated requests: %d; official reference cases: %d",
		len(firstFont), attempts, len(officialReferenceTexts),
	)
}

func integrationWorkerPath(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("EMFONT_TEST_FONT_WORKER_PATH"); path != "" {
		return path
	}
	versionData, err := exec.Command("pkg-config", "--modversion", "libwoff2enc").Output()
	if err != nil {
		t.Fatalf("query native WOFF2 version for worker build: %v", err)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get integration working directory: %v", err)
	}
	moduleRoot := filepath.Clean(filepath.Join(workingDirectory, "../.."))
	workerPath := filepath.Join(t.TempDir(), "emfont-fontworker")
	command := exec.Command(
		"go", "build", "-trimpath", "-o", workerPath,
		"-ldflags", "-X=github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzznative.woff2Version="+strings.TrimSpace(string(versionData)),
		"./cmd/fontworker",
	)
	command.Dir = moduleRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build integration font worker: %v\n%s", err, output)
	}
	return workerPath
}

func requestDynamicSubset(t *testing.T, baseURL, familyID, text string) []byte {
	t.Helper()
	fontBytes, response := requestSubset(t, baseURL, familyID, text, true)
	if response.BuildMode != domainfont.BuildModeDynamic {
		t.Fatalf("dynamic request response = %#v", response)
	}
	return fontBytes
}

func requestSubset(t *testing.T, baseURL, familyID, text string, minify bool) ([]byte, appfont.GenerateResponse) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"words": text, "min": minify, "weight": 400, "format": "woff2"})
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	response, err := http.Post(baseURL+"/g/"+familyID, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /g: %v", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read /g response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("POST /g status = %d; body=%s", response.StatusCode, responseBody)
	}
	var generated appfont.GenerateResponse
	if err := json.Unmarshal(responseBody, &generated); err != nil {
		t.Fatalf("decode /g response: %v", err)
	}
	if len(generated.Location) != 1 {
		t.Fatalf("generated locations = %#v", generated.Location)
	}
	artifactResponse, err := http.Get(generated.Location[0])
	if err != nil {
		t.Fatalf("GET generated artifact: %v", err)
	}
	defer artifactResponse.Body.Close()
	fontBytes, err := io.ReadAll(artifactResponse.Body)
	if err != nil {
		t.Fatalf("read generated artifact: %v", err)
	}
	if artifactResponse.StatusCode != http.StatusOK {
		t.Fatalf("GET generated artifact status = %d; body=%s", artifactResponse.StatusCode, fontBytes)
	}
	return fontBytes, generated
}

func measureWarmCache(t *testing.T, baseURL, familyID, text string, requestCount, concurrency int) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"words": text, "min": true, "weight": 400})
	if err != nil {
		t.Fatalf("encode warm-cache request: %v", err)
	}
	transport := &http.Transport{
		MaxIdleConns: concurrency, MaxIdleConnsPerHost: concurrency, MaxConnsPerHost: concurrency,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	endpoint := baseURL + "/g/" + familyID

	for iteration := 0; iteration < 20; iteration++ {
		if err := postGenerate(client, endpoint, body); err != nil {
			t.Fatalf("warm-cache warmup %d: %v", iteration, err)
		}
	}
	sequential := make([]time.Duration, requestCount)
	sequentialStarted := time.Now()
	for iteration := range sequential {
		started := time.Now()
		if err := postGenerate(client, endpoint, body); err != nil {
			t.Fatalf("sequential warm-cache request %d: %v", iteration, err)
		}
		sequential[iteration] = time.Since(started)
	}
	logWarmCacheResult(t, "sequential", sequential, time.Since(sequentialStarted), 1)

	parallel := make([]time.Duration, requestCount)
	jobs := make(chan int)
	errors := make(chan error, requestCount)
	var workers sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for iteration := range jobs {
				started := time.Now()
				if err := postGenerate(client, endpoint, body); err != nil {
					errors <- err
				}
				parallel[iteration] = time.Since(started)
			}
		}()
	}
	parallelStarted := time.Now()
	for iteration := range parallel {
		jobs <- iteration
	}
	close(jobs)
	workers.Wait()
	parallelElapsed := time.Since(parallelStarted)
	close(errors)
	if err := <-errors; err != nil {
		t.Fatalf("parallel warm-cache request: %v", err)
	}
	logWarmCacheResult(t, "parallel", parallel, parallelElapsed, concurrency)
}

func postGenerate(client *http.Client, endpoint string, body []byte) error {
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("status = %d; body=%s", response.StatusCode, responseBody)
	}
	return nil
}

func logWarmCacheResult(t *testing.T, mode string, durations []time.Duration, elapsed time.Duration, concurrency int) {
	t.Helper()
	sorted := append([]time.Duration(nil), durations...)
	sort.Slice(sorted, func(left, right int) bool { return sorted[left] < sorted[right] })
	var total time.Duration
	for _, duration := range durations {
		total += duration
	}
	percentile := func(quantile float64) time.Duration {
		index := int(float64(len(sorted))*quantile + 0.999999)
		return sorted[index-1]
	}
	t.Logf(
		"warm cache %s: requests=%d concurrency=%d throughput=%.2f req/s mean=%s p50=%s p95=%s p99=%s",
		mode, len(durations), concurrency, float64(len(durations))/elapsed.Seconds(),
		total/time.Duration(len(durations)), percentile(0.50), percentile(0.95), percentile(0.99),
	)
}

func officialReference(t *testing.T, fontPath, text string) ([]byte, string) {
	t.Helper()
	if _, err := exec.LookPath("hb-subset"); err != nil {
		t.Skip("official hb-subset CLI is not installed")
	}
	if _, err := exec.LookPath("woff2_compress"); err != nil {
		t.Skip("official woff2_compress CLI is not installed")
	}
	normalized, codepoints, err := domainfont.NormalizeWordSet(text)
	if err != nil || normalized == "" {
		t.Fatalf("normalize reference text: %v", err)
	}
	unicodeValues := make([]string, len(codepoints))
	for index, codepoint := range codepoints {
		unicodeValues[index] = fmt.Sprintf("%x", codepoint)
	}
	directory := t.TempDir()
	referenceTTF := filepath.Join(directory, "reference.ttf")
	subset := exec.Command("hb-subset", fontPath, "--unicodes="+strings.Join(unicodeValues, ","), "--output-file="+referenceTTF)
	if output, err := subset.CombinedOutput(); err != nil {
		t.Fatalf("official hb-subset: %v\n%s", err, output)
	}
	compress := exec.Command("woff2_compress", referenceTTF)
	if output, err := compress.CombinedOutput(); err != nil {
		t.Fatalf("official woff2_compress: %v\n%s", err, output)
	}
	reference, err := os.ReadFile(filepath.Join(directory, "reference.woff2"))
	if err != nil {
		t.Fatalf("read official WOFF2 reference: %v", err)
	}
	sum := sha256.Sum256(reference)
	return reference, hex.EncodeToString(sum[:])
}

func assertSubsetShapesText(t *testing.T, font []byte, text string) {
	t.Helper()
	if _, err := exec.LookPath("woff2_decompress"); err != nil {
		t.Skip("woff2_decompress is not installed")
	}
	if _, err := exec.LookPath("hb-shape"); err != nil {
		t.Skip("hb-shape is not installed")
	}
	directory := t.TempDir()
	woff2Path := filepath.Join(directory, "generated.woff2")
	if err := os.WriteFile(woff2Path, font, 0o600); err != nil {
		t.Fatalf("write generated WOFF2: %v", err)
	}
	decompress := exec.Command("woff2_decompress", woff2Path)
	if output, err := decompress.CombinedOutput(); err != nil {
		t.Fatalf("decompress generated WOFF2: %v\n%s", err, output)
	}
	shape := exec.Command("hb-shape", filepath.Join(directory, "generated.ttf"), text)
	output, err := shape.CombinedOutput()
	if err != nil {
		t.Fatalf("shape generated subset: %v\n%s", err, output)
	}
	if bytes.Contains(output, []byte("gid0=")) || bytes.Count(output, []byte("|"))+1 != len([]rune(text)) {
		t.Fatalf("generated subset does not shape all requested characters: %s", output)
	}
}

func requiredEnv(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Skip(key + " is not set")
	}
	return value
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
