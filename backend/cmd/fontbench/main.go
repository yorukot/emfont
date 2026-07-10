package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	appfont "github.com/emfont/emfont/backend/internal/controller/application/font"
	"github.com/emfont/emfont/backend/internal/controller/infrastructure/fontbuild/harfbuzz"
	domainfont "github.com/emfont/emfont/backend/internal/domain/font"
)

type output struct {
	Engine      string       `json:"engine"`
	SourceBytes int          `json:"sourceBytes"`
	Results     []caseOutput `json:"results"`
}

type caseOutput struct {
	Case                   string  `json:"case"`
	UniqueCodepoints       int     `json:"uniqueCodepoints"`
	Iterations             int     `json:"iterations"`
	MeanMS                 float64 `json:"meanMs"`
	P50MS                  float64 `json:"p50Ms"`
	P95MS                  float64 `json:"p95Ms"`
	SequentialOpsPerSecond float64 `json:"sequentialOpsPerSecond"`
	ParallelJobs           int     `json:"parallelJobs"`
	ParallelOpsPerSecond   float64 `json:"parallelOpsPerSecond"`
	OutputBytes            int     `json:"outputBytes"`
	OutputSHA256           string  `json:"outputSHA256"`
	DistinctOutputHashes   int     `json:"distinctOutputHashes"`
	AllocatedBytesPerOp    uint64  `json:"allocatedBytesPerOp"`
}

type benchmarkCase struct {
	name string
	text string
}

func main() {
	fontPath := flag.String("font", "", "path to the source TTF/OTF")
	iterations := flag.Int("iterations", 15, "sequential iterations per case")
	warmups := flag.Int("warmups", 2, "warmup iterations per case")
	parallelJobs := flag.Int("parallel-jobs", 12, "parallel jobs per case")
	concurrency := flag.Int("concurrency", 4, "parallel worker count")
	outputDir := flag.String("output-dir", "", "optional directory for every measured WOFF2 output")
	flag.Parse()
	if *fontPath == "" {
		fatal("-font is required")
	}
	source, err := os.ReadFile(*fontPath)
	if err != nil {
		fatal("read source font: %v", err)
	}
	builder := harfbuzz.New()
	if err := builder.Available(); err != nil {
		fatal("builder unavailable: %v", err)
	}

	cases := []benchmarkCase{
		{name: "small-7", text: "測試字型ABC"},
		{name: "medium-103", text: codepointRange(0x4e00, 100) + "ABC"},
		{name: "large-1003", text: codepointRange(0x4e00, 1000) + "ABC"},
	}
	if *outputDir != "" {
		if err := os.MkdirAll(*outputDir, 0o755); err != nil {
			fatal("create output directory: %v", err)
		}
	}
	report := output{Engine: builder.Version(), SourceBytes: len(source)}
	for _, benchmark := range cases {
		codepoints := []rune(benchmark.text)
		input := appfont.BuildInput{
			Source: source, Codepoints: codepoints, SourceFormat: "ttf", TargetFormat: domainfont.OutputFormatWOFF2,
		}
		for index := 0; index < *warmups; index++ {
			if _, err := builder.BuildSubset(context.Background(), input); err != nil {
				fatal("warm up %s: %v", benchmark.name, err)
			}
		}
		runtime.GC()
		var before runtime.MemStats
		runtime.ReadMemStats(&before)

		durations := make([]time.Duration, 0, *iterations)
		outputHashes := make(map[[sha256.Size]byte]struct{}, *iterations)
		var result appfont.BuildOutput
		for index := 0; index < *iterations; index++ {
			started := time.Now()
			result, err = builder.BuildSubset(context.Background(), input)
			if err != nil {
				fatal("build %s: %v", benchmark.name, err)
			}
			durations = append(durations, time.Since(started))
			outputHashes[sha256.Sum256(result.Data)] = struct{}{}
			if *outputDir != "" {
				name := fmt.Sprintf("%s-%03d.woff2", benchmark.name, index+1)
				path := filepath.Join(*outputDir, name)
				if err := os.WriteFile(path, result.Data, 0o644); err != nil {
					fatal("write %s: %v", path, err)
				}
			}
		}
		var after runtime.MemStats
		runtime.ReadMemStats(&after)

		parallelStarted := time.Now()
		if err := runParallel(builder, input, *parallelJobs, *concurrency); err != nil {
			fatal("parallel build %s: %v", benchmark.name, err)
		}
		parallelSeconds := time.Since(parallelStarted).Seconds()
		sum := sha256.Sum256(result.Data)
		meanDuration := mean(durations)
		report.Results = append(report.Results, caseOutput{
			Case: benchmark.name, UniqueCodepoints: len(codepoints), Iterations: *iterations,
			MeanMS: milliseconds(meanDuration), P50MS: milliseconds(percentile(durations, 0.5)),
			P95MS:                  milliseconds(percentile(durations, 0.95)),
			SequentialOpsPerSecond: 1 / meanDuration.Seconds(), ParallelJobs: *parallelJobs,
			ParallelOpsPerSecond: float64(*parallelJobs) / parallelSeconds,
			OutputBytes:          len(result.Data), OutputSHA256: hex.EncodeToString(sum[:]),
			DistinctOutputHashes: len(outputHashes),
			AllocatedBytesPerOp:  (after.TotalAlloc - before.TotalAlloc) / uint64(*iterations),
		})
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		fatal("encode report: %v", err)
	}
}

func runParallel(builder *harfbuzz.Builder, input appfont.BuildInput, jobs, concurrency int) error {
	if jobs <= 0 {
		return nil
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	queue := make(chan struct{}, jobs)
	for index := 0; index < jobs; index++ {
		queue <- struct{}{}
	}
	close(queue)

	var group sync.WaitGroup
	var firstErr error
	var errorOnce sync.Once
	for worker := 0; worker < concurrency; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for range queue {
				if _, err := builder.BuildSubset(context.Background(), input); err != nil {
					errorOnce.Do(func() { firstErr = err })
				}
			}
		}()
	}
	group.Wait()
	return firstErr
}

func codepointRange(start, length int) string {
	runes := make([]rune, length)
	for index := range runes {
		runes[index] = rune(start + index)
	}
	return string(runes)
}

func mean(values []time.Duration) time.Duration {
	var total time.Duration
	for _, value := range values {
		total += value
	}
	return total / time.Duration(len(values))
}

func percentile(values []time.Duration, quantile float64) time.Duration {
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(float64(len(sorted))*quantile + 0.999999)
	if index < 1 {
		index = 1
	}
	return sorted[index-1]
}

func milliseconds(value time.Duration) float64 {
	return float64(value) / float64(time.Millisecond)
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
