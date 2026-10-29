package xslog

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DeRuina/timberjack"
	"github.com/klauspost/compress/zstd"
)

func TestOpenFileCannotResolveDeletedWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not allow deleting the current working directory")
	}
	preserveDefaultLogger(t)
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenFile("app.log"); file != nil || err == nil || !strings.Contains(err.Error(), "resolve log path") {
		t.Fatalf("deleted working directory: file=%v error=%v", file, err)
	}
}

func TestOpenFileDirectoryCreationRaceReturnsError(t *testing.T) {
	preserveDefaultLogger(t)
	dir := filepath.Join(t.TempDir(), "new-directory")
	created := false
	// 通过告警输出模拟 Stat 后目标被其他进程创建为文件，不依赖 root 的权限行为。
	slog.SetDefault(slog.New(NewHandler(WithTemplate("${Message:l}"), WithWriter(writeFunc(func(p []byte) (int, error) {
		if bytes.Contains(p, []byte("creating it")) {
			if err := os.WriteFile(dir, nil, 0o600); err != nil {
				return 0, err
			}
			created = true
		}
		return len(p), nil
	})))))
	file, err := OpenFile(filepath.Join(dir, "app.log"))
	if !created || file != nil || err == nil || !strings.Contains(err.Error(), "create log directory") {
		t.Fatalf("directory race: created=%v file=%v error=%v", created, file, err)
	}
}

func TestOpenFileRejectsInvalidOptions(t *testing.T) {
	preserveDefaultLogger(t)
	for _, tc := range []struct {
		name string
		path string
		opts []FileOption
	}{
		{"empty path", "", nil}, {"whitespace path", " \t\n", nil},
		{"size", "app.log", []FileOption{WithMaxSizeMB(-1)}},
		{"backups", "app.log", []FileOption{WithMaxBackups(-1)}},
		{"age", "app.log", []FileOption{WithMaxAgeDays(-1)}},
		{"rotation", "app.log", []FileOption{WithRotationHours(-1)}},
		{"duration overflow", "app.log", []FileOption{WithRotationHours(int(math.MaxInt64/int64(time.Hour)) + 1)}},
		{"compression", "app.log", []FileOption{WithCompression("zip")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path
			if path == "app.log" {
				path = filepath.Join(t.TempDir(), "not-created", "app.log")
			}
			if file, err := OpenFile(path, tc.opts...); err == nil || file != nil {
				t.Fatalf("OpenFile = %v, %v; want nil and an error", file, err)
			}
			if tc.path == "app.log" {
				if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatalf("invalid options created a directory: %v", err)
				}
			}
		})
	}
}

func TestOpenFileOptionDefaultsAndOverrides(t *testing.T) {
	preserveDefaultLogger(t)
	for _, tc := range []struct {
		name               string
		opts               []FileOption
		size, backups, age int
		rotation           time.Duration
		compression        string
	}{
		{"defaults", nil, 50, 7, 7, 24 * time.Hour, CompressionNone},
		{"zeros", []FileOption{nil, WithMaxSizeMB(0), WithMaxBackups(0), WithMaxAgeDays(0), WithRotationHours(0), WithCompression("")}, 0, 0, 0, 0, CompressionNone},
		{"overrides", []FileOption{WithMaxSizeMB(2), WithMaxSizeMB(3), WithMaxBackups(4), WithMaxAgeDays(5), WithRotationHours(6), WithCompression(" GZIP ")}, 3, 4, 5, 6 * time.Hour, CompressionGzip},
		{"zstd", []FileOption{WithCompression(" ZSTD ")}, 50, 7, 7, 24 * time.Hour, CompressionZstd},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.log")
			file, err := OpenFile(path, tc.opts...)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			logger, ok := file.WriteCloser.(*timberjack.Logger)
			if !ok || logger.Filename != path || logger.MaxSize != tc.size || logger.MaxBackups != tc.backups || logger.MaxAge != tc.age || logger.RotationInterval != tc.rotation || logger.Compression != tc.compression || !logger.LocalTime {
				t.Fatalf("file options = %+v", logger)
			}
		})
	}
}

func TestOpenFileCreatesDirectoriesAndAppends(t *testing.T) {
	preserveDefaultLogger(t)
	path := filepath.Join(t.TempDir(), "nested", "app.log")
	for _, message := range []string{"first\n", "second\n"} {
		file, err := OpenFile(path, WithRotationHours(0))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		if n, err := file.Write([]byte(message)); err != nil || n != len(message) {
			t.Fatalf("Write = %d, %v", n, err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "first\nsecond\n" {
		t.Fatalf("file = %q, error = %v", data, err)
	}
}

func TestOpenFileRejectsUnwritablePaths(t *testing.T) {
	preserveDefaultLogger(t)
	dir := t.TempDir()
	if file, err := OpenFile(dir); err == nil || file != nil {
		t.Fatalf("directory accepted as file: %v, %v", file, err)
	}
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(blocker, "app.log"), filepath.Join(blocker, "nested", "app.log")} {
		if file, err := OpenFile(path); err == nil || file != nil {
			t.Fatalf("file used as parent: %v, %v", file, err)
		}
	}
}

func TestOpenFileRotatesBySize(t *testing.T) {
	preserveDefaultLogger(t)
	for _, compression := range []string{CompressionNone, CompressionGzip, CompressionZstd} {
		t.Run(compression, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "app.log")
			file, err := OpenFile(path, WithMaxSizeMB(1), WithRotationHours(0), WithCompression(compression))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			first, second := bytes.Repeat([]byte("a"), 600<<10), bytes.Repeat([]byte("b"), 600<<10)
			for _, data := range [][]byte{first, second} {
				if n, err := file.Write(data); err != nil || n != len(data) {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			active, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(active, second) {
				t.Fatalf("active file has incorrect content: length=%d, error=%v", len(active), err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 2 {
				t.Fatalf("rotation files = %v, error = %v", entries, err)
			}
			for _, entry := range entries {
				if entry.Name() == "app.log" {
					continue
				}
				backup, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				if compression == CompressionGzip {
					if !strings.HasSuffix(entry.Name(), ".gz") {
						t.Fatalf("backup is not gzip: %q", entry.Name())
					}
					reader, err := gzip.NewReader(bytes.NewReader(backup))
					if err != nil {
						t.Fatal(err)
					}
					backup, err = io.ReadAll(reader)
					_ = reader.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				if compression == CompressionZstd {
					if !strings.HasSuffix(entry.Name(), ".zst") {
						t.Fatalf("backup is not zstd: %q", entry.Name())
					}
					reader, err := zstd.NewReader(bytes.NewReader(backup))
					if err != nil {
						t.Fatal(err)
					}
					backup, err = io.ReadAll(reader)
					reader.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				if !bytes.Equal(backup, first) {
					t.Fatalf("backup length = %d, want %d", len(backup), len(first))
				}
			}
		})
	}
}

func TestOpenFileRotatesByTime(t *testing.T) {
	preserveDefaultLogger(t)
	dir := t.TempDir()
	file, err := OpenFile(filepath.Join(dir, "app.log"), WithRotationHours(1), WithCompression(CompressionNone))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	// 小时选项的单位已单独验证；缩短底层间隔，实际触发时间轮转而不等待一小时。
	file.WriteCloser.(*timberjack.Logger).RotationInterval = time.Millisecond
	if _, err := file.Write([]byte("first\n")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if _, err := file.Write([]byte("second\n")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("time rotation entries=%v error=%v", entries, err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if entry.Name() == "app.log" {
			if string(data) != "second\n" {
				t.Fatalf("active time-rotated file=%q", data)
			}
		} else if !strings.HasSuffix(entry.Name(), "-time.log") || string(data) != "first\n" {
			t.Fatalf("time backup=%s data=%q", entry.Name(), data)
		}
	}
}

func TestOpenFileRetentionAndUnrelatedFiles(t *testing.T) {
	preserveDefaultLogger(t)
	for _, tc := range []struct {
		name     string
		backups  int
		age      int
		keepOld  bool
		keepHour bool
	}{
		{"unlimited", 0, 0, true, true}, {"age", 0, 1, false, true}, {"backups", 1, 0, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			names := []string{
				"app-" + time.Now().Add(-72*time.Hour).Format("2006-01-02-15-04-05") + "-size.log",
				"app-" + time.Now().Add(-time.Hour).Format("2006-01-02-15-04-05") + "-time.log",
				"unrelated.log",
			}
			for _, name := range names {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			file, err := OpenFile(filepath.Join(dir, "app.log"), WithMaxSizeMB(1), WithMaxBackups(tc.backups), WithMaxAgeDays(tc.age), WithRotationHours(0))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			for _, c := range []byte{'a', 'b'} {
				if _, err := file.Write(bytes.Repeat([]byte{c}, 600<<10)); err != nil {
					t.Fatal(err)
				}
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			for i, keep := range []bool{tc.keepOld, tc.keepHour, true} {
				data, err := os.ReadFile(filepath.Join(dir, names[i]))
				if keep && (err != nil || string(data) != names[i]) || !keep && !os.IsNotExist(err) {
					t.Fatalf("retention %s: keep=%v data=%q error=%v", names[i], keep, data, err)
				}
			}
		})
	}
}

func TestOpenFileOversizedRecordAndCloseRecovery(t *testing.T) {
	preserveDefaultLogger(t)
	path := filepath.Join(t.TempDir(), "app.log")
	file, err := OpenFile(path, WithMaxSizeMB(1), WithRotationHours(0))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	var reported error
	h := NewHandler(WithWriter(file), WithTemplate("${Message:l}"), WithErrorHandler(func(err error) { reported = err }))
	r := testRecord()
	r.Message = strings.Repeat("x", 1<<20)
	if err := h.Handle(context.Background(), r); err == nil || !errors.Is(reported, err) {
		t.Fatalf("oversized record error=%v callback=%v", err, reported)
	}
	if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
		t.Fatalf("oversized record wrote partial data: length=%d error=%v", len(data), err)
	}
	if err := h.Handle(context.Background(), testRecord()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	// timberjack 关闭后采用单次打开、追加、关闭；不会重启后台轮转任务。
	if _, err := fmt.Fprint(file, "after-close\n"); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "hello\nafter-close\n" {
		t.Fatalf("file recovery=%q error=%v", data, err)
	}
}
