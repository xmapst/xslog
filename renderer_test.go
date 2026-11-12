package xslog

import (
	"log/slog"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSourceWithModuleVersionAndUnknownPC(t *testing.T) {
	info := (renderer{}).source(versionedSourcePC())
	if info.file != "/deps/example/module@v1.2.3/file.go" || info.location != "module/file.go:100" || info.fileName != "file.go" || info.line != 100 {
		t.Fatalf("module source=%+v", info)
	}
	for _, pc := range []uintptr{1, ^uintptr(0)} {
		info := (renderer{}).source(pc)
		if info.file != "" || info.location != "" || info.line != 0 {
			t.Fatalf("unknown PC %x produced source %+v", pc, info)
		}
	}
}

// timeFormat 就是 Go 布局，零值使用默认布局；不属于布局的文字原样输出，没有特殊的关键字。
func TestTimeFormat(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format timeFormat
		want   string
	}{
		{"零值是默认布局", "", "2024-01-02T03:04:05.678Z"},
		{"显式的默认布局", defaultTimeLayout, "2024-01-02T03:04:05.678Z"},
		{"时分秒", "15:04:05", "03:04:05"},
		{"不属于布局的文字原样输出", "time=15h", "time=03h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(tc.format.append([]byte("prefix:"), testTime())); got != "prefix:"+tc.want {
				t.Fatalf("append = %q", got)
			}
			if got := tc.format.text(testTime()); got != tc.want {
				t.Fatalf("text = %q", got)
			}
		})
	}
	if got := timeFormat("").text(testTime().In(time.FixedZone("UTC+8", 8*3600))); got != "2024-01-02T11:04:05.678+08:00" {
		t.Fatal(got)
	}
	// 零值的编码器直接使用默认布局。
	if got := (jsonEncoder{}).appendValue(nil, slog.TimeValue(testTime())); string(got) != `"2024-01-02T03:04:05.678Z"` {
		t.Fatalf("零值 jsonEncoder 的时间 = %s", got)
	}
	if text, block, quoted := (textEncoder{}).render(slog.TimeValue(testTime())); text != "2024-01-02T03:04:05.678Z" || block || !quoted {
		t.Fatalf("零值 textEncoder 的时间 = %q, block=%v, quoted=%v", text, block, quoted)
	}
}

func TestSourceRendering(t *testing.T) {
	rd := renderer{}
	for _, tc := range []struct{ input, want string }{
		{"file.go", "file.go"}, {"dir/file.go", "file.go"},
		{"/root/project/file.go", "project/file.go"}, {"/file.go", "file.go"},
	} {
		if got := rd.trimmedPath(tc.input); got != tc.want {
			t.Fatalf("trimmedPath(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	if got := moduleVersionPattern.ReplaceAllString("module@v1.2.3-rc.1/file.go", ""); got != "module/file.go" {
		t.Fatal(got)
	}
	if rd.source(0) != (sourceInfo{}) || rd.renderSource(0) != (sourceInfo{}) {
		t.Fatal("zero PC has a source")
	}
	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	frame, _ := runtime.CallersFrames(pcs[:]).Next()
	want := sourceInfo{
		location: testSourceLocation(frame.File, frame.Line), file: frame.File, fileName: "renderer_test.go",
		line: frame.Line, function: "TestSourceRendering", pkg: "github.com/xmapst/xslog",
	}
	for range 2 {
		if got := rd.source(pcs[0]); got != want {
			t.Fatalf("source = %+v, want %+v", got, want)
		}
	}
}

func TestSourceFunctionInfo(t *testing.T) {
	for _, tc := range []struct{ function, pkg, name string }{
		{"main.main", "main", "main"},
		{"net/http.(*Server).Serve", "net/http", "(*Server).Serve"},
		{"github.com/example/pkg.Func.func1", "github.com/example/pkg", "Func.func1"},
		{"github.com/example/pkg.(*Type[...]).Method", "github.com/example/pkg", "(*Type[...]).Method"},
		{"github.com/example/pkg.Func[example.com/types.Value].func1", "github.com/example/pkg", "Func[example.com/types.Value].func1"},
		{"gopkg.in/yaml%2ev3.(*Decoder).Decode", "gopkg.in/yaml.v3", "(*Decoder).Decode"},
		{"example.com/pkg%25name.Func", "example.com/pkg%name", "Func"},
		{"example.com/bad%zz.Func", "example.com/bad%zz", "Func"},
		{"", "", ""}, {"unknown", "", "unknown"},
	} {
		if got := (renderer{}).packagePath(tc.function); got != tc.pkg {
			t.Fatalf("packagePath(%q) = %q, want %q", tc.function, got, tc.pkg)
		}
		if got := (renderer{}).functionName(tc.function); got != tc.name {
			t.Fatalf("functionName(%q) = %q, want %q", tc.function, got, tc.name)
		}
	}
}

func TestIndexUnprintable(t *testing.T) {
	type text string
	type data []byte
	rd := renderer{}
	for _, tc := range []struct {
		name, input string
		want        int
	}{
		{"empty", "", -1},
		{"ASCII", "printable \" \\", -1},
		{"tab", "ok\t", -1},
		{"newline", "ok\n", 2},
		{"escape", "ok\x1b", 2},
		{"delete", "ok\x7f", 2},
		{"null", "\t\x00", 1},
		{"Unicode", "ok世界", 2},
		{"invalid UTF-8", "ok\xff", 2},
		{"first byte", "\x1btext", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, got := range []int{
				rd.indexUnprintable(tc.input), rd.indexUnprintable([]byte(tc.input)),
				rd.indexUnprintable(text(tc.input)), rd.indexUnprintable(data(tc.input)),
			} {
				if got != tc.want {
					t.Fatalf("indexUnprintable(%q) = %d, want %d", tc.input, got, tc.want)
				}
			}
		})
	}
}

func TestPlainTextEscaping(t *testing.T) {
	rd := renderer{}
	for _, tc := range []struct{ input, want string }{
		{"printable\t世界", "printable\t世界"},
		{"\n\x1b\x7f\u202e", `\u000a\u001b\u007f\u202e`},
		{"\U000e0001", `\udb40\udc01`}, {"bad\xff", "bad\ufffd"},
	} {
		if got := rd.plainText(tc.input); got != tc.want {
			t.Fatalf("plainText(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
	if got := string(rd.appendRuneEscape(nil, '\U0001f642')); got != `\ud83d\ude42` {
		t.Fatal(got)
	}
	if strings.ContainsRune(rd.plainText("a\x1bb"), '\x1b') {
		t.Fatal("escape character was retained")
	}
}
