package zerocopybitcask

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"
)

func BenchmarkRandomPointReads(b *testing.B) {
	db, keys := buildBenchDB(b, benchRecords(), 1024)
	defer db.Close()

	rng := rand.New(rand.NewSource(1))

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := keys[rng.Intn(len(keys))]
		value, err := db.Get(key)
		if err != nil {
			b.Fatal(err)
		}
		if len(value) != 1024 {
			b.Fatalf("value length = %d", len(value))
		}
	}
}

func BenchmarkPutThroughputManualFsync(b *testing.B) {
	dir := b.TempDir()
	db, err := Open(Options{
		Dir:               dir,
		FsyncPolicy:       FsyncManual,
		MaxActiveFileSize: 1 << 30,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	value := make([]byte, 1024)
	b.SetBytes(int64(len(value)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key-%09d", i))
		value[0] = byte(i)
		if err := db.Put(key, value); err != nil {
			b.Fatal(err)
		}
	}
	if err := db.Sync(); err != nil {
		b.Fatal(err)
	}
}

func BenchmarkPutThroughputEveryWriteFsync(b *testing.B) {
	dir := b.TempDir()
	db, err := Open(Options{
		Dir:               dir,
		FsyncPolicy:       FsyncEveryWrite,
		MaxActiveFileSize: 1 << 30,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	value := make([]byte, 1024)
	b.SetBytes(int64(len(value)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key-%09d", i))
		value[0] = byte(i)
		if err := db.Put(key, value); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRandomReadP99Latency(b *testing.B) {
	db, keys := buildBenchDB(b, benchRecords(), 1024)
	defer db.Close()

	rng := rand.New(rand.NewSource(2))
	latencies := make([]int64, 0, b.N)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		if _, err := db.Get(keys[rng.Intn(len(keys))]); err != nil {
			b.Fatal(err)
		}
		latencies = append(latencies, time.Since(start).Nanoseconds())
	}
	b.StopTimer()

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	if len(latencies) > 0 {
		p50 := latencies[int(float64(len(latencies)-1)*0.50)]
		p95 := latencies[int(float64(len(latencies)-1)*0.95)]
		p99 := latencies[int(float64(len(latencies)-1)*0.99)]
		if p50 < 1 {
			p50 = 1
		}
		if p95 < 1 {
			p95 = 1
		}
		if p99 < 1 {
			p99 = 1
		}
		b.ReportMetric(float64(p50), "p50-ns")
		b.ReportMetric(float64(p95), "p95-ns")
		b.ReportMetric(float64(p99), "p99-ns")
		b.ReportMetric(1e9/float64(p50), "p50-ops/sec")
	}
}

func BenchmarkStartupRecovery(b *testing.B) {
	records := benchRecords()
	dir := b.TempDir()
	db, err := Open(Options{Dir: dir, FsyncPolicy: FsyncManual, MaxActiveFileSize: 1 << 30})
	if err != nil {
		b.Fatal(err)
	}
	value := make([]byte, 512)
	for i := 0; i < records; i++ {
		key := []byte(fmt.Sprintf("recover-%09d", i))
		if err := db.Put(key, value); err != nil {
			b.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		b.Fatal(err)
	}

	var mem runtime.MemStats
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		start := time.Now()
		db, err := Open(Options{Dir: dir, FsyncPolicy: FsyncManual, MaxActiveFileSize: 1 << 30})
		if err != nil {
			b.Fatal(err)
		}
		if err := db.Close(); err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(time.Since(start).Nanoseconds()), "recovery-ns")
	}
	b.StopTimer()
	runtime.ReadMemStats(&mem)
	b.ReportMetric(float64(mem.Alloc), "heap-live-bytes")
}

func BenchmarkMmapVsBufferedRead(b *testing.B) {
	dir := b.TempDir()
	path := dir + string(os.PathSeparator) + "raw.bin"
	data := make([]byte, 1024*1024)
	for i := range data {
		data[i] = byte(i)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		b.Fatal(err)
	}

	region, err := mmapFile(path)
	if err != nil {
		b.Fatal(err)
	}
	defer region.Close()

	file, err := os.Open(path)
	if err != nil {
		b.Fatal(err)
	}
	defer file.Close()

	offsets := make([]int64, 4096)
	for i := range offsets {
		offsets[i] = int64(binary.LittleEndian.Uint32([]byte{
			byte(i), byte(i >> 8), byte(i >> 16), byte(i >> 24),
		}) % uint32(len(data)-4096))
	}

	b.Run("mmap", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			off := offsets[i%len(offsets)]
			chunk := region.data[off : off+4096]
			if len(chunk) != 4096 {
				b.Fatal(len(chunk))
			}
		}
	})

	b.Run("buffered-readat", func(b *testing.B) {
		buf := make([]byte, 4096)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			off := offsets[i%len(offsets)]
			if _, err := file.ReadAt(buf, off); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func buildBenchDB(b *testing.B, records int, valueSize int) (*DB, [][]byte) {
	b.Helper()
	b.StopTimer()
	dir := b.TempDir()
	db, err := Open(Options{
		Dir:               dir,
		FsyncPolicy:       FsyncManual,
		MaxActiveFileSize: 1 << 30,
	})
	if err != nil {
		b.Fatal(err)
	}

	value := make([]byte, valueSize)
	keys := make([][]byte, records)
	for i := 0; i < records; i++ {
		key := []byte(fmt.Sprintf("key-%09d", i))
		keys[i] = key
		value[0] = byte(i)
		if err := db.Put(key, value); err != nil {
			b.Fatal(err)
		}
	}
	if err := db.Sync(); err != nil {
		b.Fatal(err)
	}
	b.StartTimer()
	return db, keys
}

func benchRecords() int {
	raw := os.Getenv("ZCBS_BENCH_RECORDS")
	if raw == "" {
		return 10000
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return 10000
	}
	return n
}
