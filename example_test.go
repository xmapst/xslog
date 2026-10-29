package xslog_test

import (
	"log/slog"
	"os"

	"github.com/xmapst/xslog"
)

func ExampleNewHandler() {
	logger := slog.New(xslog.NewHandler(
		xslog.WithWriter(os.Stdout),
		xslog.WithTemplate("[${Level:u3}] ${Message:l}"),
	)).With("app", "gate")
	logger.Info("ready", "port", 8080)
	// Output: [INF] ready app="gate" port=8080
}

func ExampleWithTemplate() {
	logger := slog.New(xslog.NewHandler(
		xslog.WithWriter(os.Stdout),
		xslog.WithTemplate(`{"severity":${Level:u3},"event":{"message":${Message}}}`),
	)).WithGroup("server")
	logger.Info("ready", "port", 8080)
	// Output: {"severity":"INF","event":{"message":"ready"},"server":{"port":8080}}
}

func ExampleWithTemplate_caller() {
	logger := slog.New(xslog.NewHandler(
		xslog.WithWriter(os.Stdout),
		xslog.WithTemplate("${FileName}: ${Message:l}"),
	))
	logger.Info("ready")
	// Output: example_test.go: ready
}

func ExampleWithTemplate_multiline() {
	logger := slog.New(xslog.NewHandler(
		xslog.WithWriter(os.Stdout),
		xslog.WithTemplate(`{"message":${Message}}`),
	))
	logger.Info("first\nsecond", "stack", "frame1\nframe2")
	// Output:
	// {"message":[
	// 	"first",
	// 	"second"],"stack":[
	// 	"frame1",
	// 	"frame2"]}
}
