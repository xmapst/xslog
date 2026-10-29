package xslog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type trackedWriteCloser struct {
	bytes.Buffer
	closes int
	err    error
}

func (w *trackedWriteCloser) Close() error { w.closes++; return w.err }

func TestSetupUsesDefaultTemplate(t *testing.T) {
	preserveDefaultLogger(t)
	unsetTestEnv(t, "Source")
	for _, opts := range [][]Option{nil, {WithTemplate("")}, {WithTemplate("${Message:l}"), WithTemplate("")}} {
		var out bytes.Buffer
		if err := Setup(append(opts, WithWriter(&out))...); err != nil {
			t.Fatal(err)
		}
		if err := slog.Default().Handler().Handle(context.Background(), testRecord()); err != nil {
			t.Fatal(err)
		}
		if want := "2024-01-02T03:04:05.678Z INFO [] message=\"hello\"\n"; out.String() != want {
			t.Fatalf("default Setup output = %q, want %q", out.String(), want)
		}
	}
}

func TestSetupUsesSharedDynamicLevel(t *testing.T) {
	preserveDefaultLogger(t)
	var first, second bytes.Buffer
	var supplied slog.LevelVar
	supplied.Set(slog.LevelWarn)
	if err := Setup(WithWriter(&first), WithLevel(&supplied), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	old := slog.Default()
	supplied.Set(slog.LevelDebug)
	slog.Info("filtered")
	slog.Warn("warn")
	SetLevel(slog.LevelDebug)
	slog.Debug("debug")
	if first.String() != "warn\ndebug\n" {
		t.Fatal(first.String())
	}
	if err := Setup(WithWriter(&second), WithLevel(slog.LevelError), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	old.Warn("old filtered")
	slog.Error("error")
	if first.String() != "warn\ndebug\n" || second.String() != "error\n" {
		t.Fatalf("first=%q, second=%q", first.String(), second.String())
	}
	SetLevel(slog.LevelDebug)
	old.Debug("old debug")
	if !strings.HasSuffix(first.String(), "old debug\n") {
		t.Fatal("previous global handlers do not share SetLevel")
	}
}

func TestSetupFailuresPreserveLoggerAndFile(t *testing.T) {
	preserveDefaultLogger(t)
	path := filepath.Join(t.TempDir(), "active.log")
	if err := Setup(WithFile(path, WithRotationHours(0)), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	previous, file := slog.Default(), std.file.Load()
	newPath := filepath.Join(t.TempDir(), "not-created", "app.log")
	for _, opts := range [][]Option{
		{WithFile(" \t")},
		{WithFile(newPath, WithMaxSizeMB(-1))},
	} {
		if err := Setup(opts...); err == nil {
			t.Fatal("invalid Setup succeeded")
		}
		if slog.Default() != previous || std.file.Load() != file {
			t.Fatal("failed Setup replaced the logger or current file")
		}
	}
	if _, err := os.Stat(filepath.Dir(newPath)); !os.IsNotExist(err) {
		t.Fatalf("failed Setup created its target directory: %v", err)
	}
	slog.Info("still active")
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "still active\n" || slog.Default() != previous {
		t.Fatalf("active file = %q, error = %v", data, err)
	}
}

func TestSetupAcceptsInvalidJSONAsText(t *testing.T) {
	preserveDefaultLogger(t)
	path := filepath.Join(t.TempDir(), "text.log")
	previous := slog.Default()
	if err := Setup(WithFile(path, WithRotationHours(0)), WithTemplate(`{${Time},"Level":${Level}}`)); err != nil {
		t.Fatal(err)
	}
	if slog.Default() == previous {
		t.Fatal("text fallback did not install the new Logger")
	}
	if err := slog.Default().Handler().Handle(context.Background(), testRecord(slog.Int("count", 2))); err != nil {
		t.Fatal(err)
	}
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "{2024-01-02T03:04:05.678Z,\"Level\":INFO} count=2\n" {
		t.Fatalf("file = %q, error = %v", data, err)
	}
}

func TestSetupWriterAndFileOptionOrder(t *testing.T) {
	preserveDefaultLogger(t)
	var out bytes.Buffer
	unused := filepath.Join(t.TempDir(), "unused", "app.log")
	if err := Setup(WithFile(unused), WithWriter(&out), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	slog.Info("writer")
	if out.String() != "writer\n" {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(filepath.Dir(unused)); !os.IsNotExist(err) {
		t.Fatalf("overridden file was opened: %v", err)
	}
	path := filepath.Join(t.TempDir(), "active.log")
	if err := Setup(WithWriter(&out), WithFile(path, WithRotationHours(0)), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	slog.Info("file")
	if err := Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "file\n" {
		t.Fatalf("file = %q, error = %v", data, err)
	}
}

func TestSetupDoesNotCloseCallerWriter(t *testing.T) {
	preserveDefaultLogger(t)
	writer := new(trackedWriteCloser)
	if err := Setup(WithWriter(writer), WithTemplate("${Message:l}")); err != nil {
		t.Fatal(err)
	}
	slog.Info("hello")
	for range 2 {
		if err := Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := Setup(WithWriter(io.Discard)); err != nil {
		t.Fatal(err)
	}
	if writer.closes != 0 || writer.String() != "hello\n" {
		t.Fatalf("caller writer: closes=%d, data=%q", writer.closes, writer.String())
	}
}

func TestManagerClosesPreviousFileAndReportsCloseErrors(t *testing.T) {
	preserveDefaultLogger(t)
	for _, closeErr := range []error{nil, errors.New("close failed")} {
		old := &trackedWriteCloser{err: closeErr}
		m := &manager{level: new(slog.LevelVar)}
		m.file.Store(&FileOutput{WriteCloser: old})
		var out bytes.Buffer
		if err := m.setup((options{}).apply([]Option{WithWriter(&out), WithTemplate("${Message:l}")})); err != nil {
			t.Fatal(err)
		}
		if old.closes != 1 || m.file.Load() != nil {
			t.Fatalf("previous file close count = %d", old.closes)
		}
		if closeErr != nil && (!strings.Contains(out.String(), "close previous log file") || !strings.Contains(out.String(), closeErr.Error())) {
			t.Fatalf("close warning = %q", out.String())
		}
		if err := m.close(); err != nil || old.closes != 1 {
			t.Fatalf("repeated close = %v, count = %d", err, old.closes)
		}
	}
}

func TestSetupConcurrentLifecycle(t *testing.T) {
	preserveDefaultLogger(t)
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for range 20 {
		wg.Go(func() { errs <- Setup(WithWriter(io.Discard), WithTemplate("${Message:l}")) })
		wg.Go(func() { errs <- Close() })
		wg.Go(func() { SetLevel(slog.LevelDebug); slog.Info("concurrent") })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestWriterDefaults(t *testing.T) {
	if got := (options{}).output(); got != os.Stdout {
		t.Fatal("default output is not stdout")
	}
	o := (options{}).apply([]Option{WithFile("unused"), WithWriter(nil)})
	if o.output() != os.Stdout || o.hasFile {
		t.Fatal("WithWriter(nil) did not override file output")
	}
}
