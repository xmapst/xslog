package xslog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stressSink 验证每次 Write 都包含一条完整记录，不保留记录正文。
// 除 active 外的字段故意不加锁，以便 race 检查 Handler 的写入同步。
type stressSink struct {
	json    bool
	workers int
	seen    []bool
	count   int
	bytes   int64
	active  atomic.Int64
}

func (w *stressSink) Write(p []byte) (int, error) {
	if w.active.Add(1) != 1 {
		w.active.Add(-1)
		return 0, errors.New("concurrent writes reached the same sink")
	}
	defer w.active.Add(-1)
	if len(p) == 0 || p[len(p)-1] != '\n' {
		return 0, errors.New("record is missing its final LF")
	}
	var id, seq int
	if w.json {
		var decoded struct {
			Message any
			App     string
			PID     int
			Scope   struct {
				Worker struct {
					ID      int
					Seq     *int
					Stack   []string
					Payload struct{ N int }
				}
			}
		}
		if err := json.Unmarshal(p, &decoded); err != nil {
			return 0, err
		}
		worker := decoded.Scope.Worker
		if decoded.App != "stress" || decoded.PID != os.Getpid() || worker.Seq == nil || len(worker.Stack) != 2 || worker.Stack[0] != "first" || worker.Stack[1] != "second" || worker.Payload.N != 7 {
			return 0, fmt.Errorf("record lost metadata or attributes: %q", p)
		}
		id, seq = worker.ID, *worker.Seq
		if seq%13 == 0 {
			lines, ok := decoded.Message.([]any)
			if !ok || len(lines) != 2 || lines[0] != "first" || lines[1] != "second" {
				return 0, errors.New("multiline message lost its array representation")
			}
		} else if decoded.Message != "hello" {
			return 0, errors.New("message changed during concurrent rendering")
		}
	} else {
		for _, field := range []struct {
			key string
			dst *int
		}{{"scope.worker.id=", &id}, {"scope.worker.seq=", &seq}} {
			_, suffix, found := bytes.Cut(p, []byte(field.key))
			if !found {
				return 0, fmt.Errorf("missing %s", field.key)
			}
			end := bytes.IndexAny(suffix, " \t\r\n")
			if end < 0 {
				end = len(suffix)
			}
			value, err := strconv.Atoi(string(suffix[:end]))
			if err != nil {
				return 0, err
			}
			*field.dst = value
		}
		if !bytes.Contains(p, []byte(`app="stress"`)) || !bytes.Contains(p, []byte(`scope.worker.payload={"n":7}`)) || !bytes.Contains(p, []byte(" │ first\n")) || !bytes.Contains(p, []byte(" │ second\n")) {
			return 0, errors.New("text fields or multiline blocks were corrupted")
		}
	}
	if seq < 0 || seq >= len(w.seen) || seq%w.workers != id || w.seen[seq] {
		return 0, fmt.Errorf("invalid or duplicate sequence: worker=%d seq=%d", id, seq)
	}
	w.seen[seq] = true
	w.count++
	w.bytes += int64(len(p))
	return len(p), nil
}

func stressScale(t *testing.T, name string, fallback, maximum int) int {
	t.Helper()
	if value := os.Getenv(name); value != "" {
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 || n > maximum {
			t.Fatalf("%s must be in [1,%d], got %q", name, maximum, value)
		}
		return n
	}
	return fallback
}

func TestStressConcurrentRecords(t *testing.T) {
	records := stressScale(t, "XSLOG_STRESS_RECORDS", 2048, 2_000_000)
	workers := stressScale(t, "XSLOG_STRESS_WORKERS", 8, 512)
	t.Logf("go=%s os=%s arch=%s CPUs=%d GOMAXPROCS=%d", runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), runtime.GOMAXPROCS(0))
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	for _, mode := range []string{"text", "json"} {
		t.Run(mode, func(t *testing.T) {
			template := "[${Level:u3}] ${Message:l}"
			if mode == "json" {
				template = `{"time":${Time:unixNano},"level":${Level},"message":${Message},"file":${FileName},"line":${Line},"pid":${PID}}${NewLine}`
			}
			// 同一编译选项同时用于独立 Handler 与派生 Handler。
			option := WithTemplate(template)
			sinks := make([]*stressSink, 4)
			handlers := make([]slog.Handler, len(sinks))
			for i := range sinks {
				sinks[i] = &stressSink{json: mode == "json", workers: workers, seen: make([]bool, records)}
				handlers[i] = NewHandler(WithWriter(sinks[i]), option).WithAttrs([]slog.Attr{slog.String("app", "stress")}).WithGroup("scope").WithGroup("worker")
			}
			var wg sync.WaitGroup
			errs := make(chan error, workers)
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			started := time.Now()
			for id := range workers {
				h := handlers[id%len(handlers)].WithAttrs([]slog.Attr{slog.Int("id", id)})
				wg.Go(func() {
					defer func() {
						if value := recover(); value != nil {
							errs <- fmt.Errorf("unexpected worker panic: %T", value)
						}
					}()
					for seq := id; seq < records; seq += workers {
						r := testRecord(slog.Int("seq", seq), slog.String("stack", "first\r\nsecond\r\n"), slog.String("payload", `{"n":7}`))
						r.PC = pcs[0]
						if seq%13 == 0 {
							r.Message = "first\nsecond"
						}
						if seq%257 == 0 {
							r.AddAttrs(slog.Any("broken", failingMarshaler{}))
						}
						if err := h.Handle(context.Background(), r); err != nil {
							errs <- err
							return
						}
					}
				})
			}
			wg.Wait()
			elapsed := time.Since(started)
			runtime.ReadMemStats(&after)
			close(errs)
			for err := range errs {
				t.Error(err)
			}
			count, written := 0, int64(0)
			for _, sink := range sinks {
				count += sink.count
				written += sink.bytes
			}
			if count != records {
				t.Fatalf("received %d records, want %d", count, records)
			}
			for seq := range records {
				sink := sinks[(seq%workers)%len(sinks)]
				if !sink.seen[seq] {
					t.Fatalf("missing sequence %d", seq)
				}
			}
			t.Logf("records=%d workers=%d sinks=%d elapsed=%s records/s=%.0f bytes=%d (includes synchronous validation)", count, workers, len(sinks), elapsed, float64(count)/elapsed.Seconds(), written)
			t.Logf("allocated/op=%.0f B GC-cycles=%d heap-after=%d B (includes validation and goroutines; heap sampled before forced GC)", float64(after.TotalAlloc-before.TotalAlloc)/float64(count), after.NumGC-before.NumGC, after.HeapAlloc)
		})
	}
}

func TestStressWriterFailuresAndRecovery(t *testing.T) {
	const records, workers = 2048, 8
	var calls, callbacks atomic.Int64
	want := errors.New("injected writer failure")
	h := NewHandler(WithTemplate(`{"message":${Message}}`), WithWriter(writeFunc(func(p []byte) (int, error) {
		n := calls.Add(1)
		if n%17 == 0 {
			return 0, want
		}
		if n%29 == 0 {
			return len(p) / 2, nil
		}
		return len(p), nil
	})), WithErrorHandler(func(error) { callbacks.Add(1) }))
	var failed atomic.Int64
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for range records / workers {
				if err := h.Handle(context.Background(), testRecord()); err != nil {
					if !errors.Is(err, want) && !errors.Is(err, io.ErrShortWrite) {
						t.Errorf("unexpected error: %v", err)
					}
					failed.Add(1)
				}
			}
		})
	}
	wg.Wait()
	expected := int64(records/17 + records/29 - records/(17*29))
	if calls.Load() != records || failed.Load() != expected || callbacks.Load() != expected {
		t.Fatalf("writes=%d failures=%d callbacks=%d; want %d writes, %d failures", calls.Load(), failed.Load(), callbacks.Load(), records, expected)
	}
}

func TestConcurrentLevelAndEnvironmentUpdates(t *testing.T) {
	const name = "XSLOG_STRESS_ENV"
	t.Setenv(name, "initial")
	var output testWriter
	var level slog.LevelVar
	logger := slog.New(NewHandler(WithWriter(&output), WithLevel(&level), WithTemplate(`{"message":${Message},"environment":${XSLOG_STRESS_ENV}}`)))
	logger.Info("hello", "seq", -1)
	stop := make(chan struct{})
	var updates sync.WaitGroup
	updates.Go(func() {
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			default:
			}
			if n%2 == 0 {
				level.Set(slog.LevelDebug)
				_ = os.Setenv(name, "changed")
			} else {
				level.Set(slog.LevelError)
				_ = os.Setenv(name, "")
			}
		}
	})
	var workers sync.WaitGroup
	for id := range 8 {
		workers.Go(func() {
			for n := range 256 {
				logger.Info("hello", "seq", id*256+n)
			}
		})
	}
	workers.Wait()
	close(stop)
	updates.Wait()
	seen := make(map[int]bool)
	for line := range strings.SplitSeq(strings.TrimSuffix(output.String(), "\n"), "\n") {
		var record struct {
			Message, Environment string
			Seq                  int
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil || record.Message != "hello" || seen[record.Seq] || record.Seq < -1 || record.Seq >= 2048 {
			t.Fatalf("record=%q error=%v", line, err)
		}
		if record.Environment != "initial" && record.Environment != "changed" && record.Environment != "" {
			t.Fatalf("unexpected environment=%q", record.Environment)
		}
		seen[record.Seq] = true
	}
	if len(seen) != output.writes {
		t.Fatalf("unique=%d writes=%d", len(seen), output.writes)
	}
}
