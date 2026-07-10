import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";
import { performance } from "node:perf_hooks";
import subsetFont from "subset-font";

const fontPath = process.env.BENCH_FONT;
if (!fontPath) throw new Error("BENCH_FONT is required");

const iterations = Number.parseInt(process.env.BENCH_ITERATIONS || "15", 10);
const warmups = Number.parseInt(process.env.BENCH_WARMUPS || "2", 10);
const parallelJobs = Number.parseInt(process.env.BENCH_PARALLEL_JOBS || "12", 10);
const source = await readFile(fontPath);

const cases = [
	{ name: "small-7", text: "測試字型ABC" },
	{ name: "medium-103", text: codepointRange(0x4e00, 100) + "ABC" },
	{ name: "large-1003", text: codepointRange(0x4e00, 1000) + "ABC" },
];

const results = [];
for (const benchmark of cases) {
	for (let index = 0; index < warmups; index++) {
		await subsetFont(source, benchmark.text, { targetFormat: "woff2" });
	}
	global.gc?.();

	const durations = [];
	let output;
	for (let index = 0; index < iterations; index++) {
		const started = performance.now();
		output = await subsetFont(source, benchmark.text, { targetFormat: "woff2" });
		durations.push(performance.now() - started);
	}

	const parallelStarted = performance.now();
	await Promise.all(
		Array.from({ length: parallelJobs }, () =>
			subsetFont(source, benchmark.text, { targetFormat: "woff2" }),
		),
	);
	const parallelSeconds = (performance.now() - parallelStarted) / 1000;

	results.push({
		case: benchmark.name,
		uniqueCodepoints: new Set(benchmark.text).size,
		iterations,
		meanMs: mean(durations),
		p50Ms: percentile(durations, 0.5),
		p95Ms: percentile(durations, 0.95),
		sequentialOpsPerSecond: 1000 / mean(durations),
		parallelJobs,
		parallelOpsPerSecond: parallelJobs / parallelSeconds,
		outputBytes: output.length,
		outputSHA256: createHash("sha256").update(output).digest("hex"),
	});
}

console.log(
	JSON.stringify(
		{
			engine: "node-subset-font-2.4.0",
			sourceBytes: source.length,
			results,
		},
		null,
		2,
	),
);

function codepointRange(start, length) {
	return Array.from({ length }, (_, index) => String.fromCodePoint(start + index)).join("");
}

function mean(values) {
	return values.reduce((sum, value) => sum + value, 0) / values.length;
}

function percentile(values, quantile) {
	const sorted = [...values].sort((left, right) => left - right);
	return sorted[Math.ceil(sorted.length * quantile) - 1];
}
