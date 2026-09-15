package xslog

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestFileOptionsRejectNegativeValues(t *testing.T) {
	for name, opt := range map[string]FileOption{
		"size":     WithMaxSizeMB(-1),
		"backups":  WithMaxBackups(-1),
		"age":      WithMaxAgeDays(-1),
		"rotation": WithRotationHours(-1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := OpenFile(t.TempDir()+"/app.log", opt); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestSetupRejectsWhitespaceFilePath(t *testing.T) {
	if err := Setup(WithFile("   ")); err == nil || !strings.Contains(err.Error(), "empty log file path") {
		t.Fatalf("error = %v", err)
	}
	_ = Setup(WithWriter(bytes.NewBuffer(nil)))
}

func TestOpenFileWritesAndCloses(t *testing.T) {
	path := t.TempDir() + "/nested/app.log"
	w, err := OpenFile(path, WithMaxSizeMB(1), WithRotationHours(0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("line\n")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterUsesConfiguredLogger(t *testing.T) {
	var out bytes.Buffer
	log := slog.New(NewJSONHandler(WithWriter(&out)))
	NewAdapter(log).Info("adapter message")
	if !strings.Contains(out.String(), "adapter message") {
		t.Fatal(out.String())
	}
}
