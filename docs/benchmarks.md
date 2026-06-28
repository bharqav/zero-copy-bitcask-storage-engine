# Benchmark Plan

Run the built-in benchmark suite:

```powershell
$env:ZCBS_BENCH_RECORDS="100000"
go test -run '^$' -bench . -benchmem
```

Use a smaller record count for local smoke tests:

```powershell
$env:ZCBS_BENCH_RECORDS="1000"
go test -run '^$' -bench 'BenchmarkRandomPointReads|BenchmarkMmapVsBufferedRead' -benchmem -benchtime=100ms
```

## Current Built-In Coverage

- Random point-read throughput and allocations.
- Manual-fsync write throughput.
- Every-write-fsync write throughput.
- p50/p95/p99 read latency metrics.
- Startup recovery time and heap usage.
- mmap slicing vs `os.File.ReadAt`.

## External Engine Comparisons

BadgerDB, Pebble, and RocksDB comparisons should be kept behind separate build tags or submodules because they bring large dependency trees and platform-specific build requirements.

Recommended benchmark shape:

- Use the same key distribution as `BenchmarkRandomPointReads`.
- Preload the same number of records and same value size.
- Run read-only random point reads with `b.ReportAllocs`.
- Run write throughput with durability settings documented explicitly.
- Report p50/p95/p99 latency using the same measurement helper.

Suggested package layout:

```text
bench/external/badger_test.go   //go:build badgerbench
bench/external/pebble_test.go   //go:build pebblebench
bench/external/rocks_test.go    //go:build rocksbench
```

This keeps the core storage engine dependency-light while making apples-to-apples comparisons easy to add in a dedicated benchmark environment.
