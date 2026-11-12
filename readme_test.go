package xslog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// README「格式动词」一章用表格列出通用动词、Time 和 Level 的示例。
// 每个示例都用同一条记录实际渲染：文本模板得到 text，JSON 模板 {"v":<占位符>} 得到 json，
// 再检查 README 中确有一行同时写出模板和输出，使文档与实现保持一致。
// 修改动词行为或示例时，先更新这里的期望值，再同步 README。

// readmeRow 是 README 示例表格的一行。
type readmeRow struct {
	template string // 模板，如 ${Message:u}
	text     string // 文本模板的输出，不含记录末尾补齐的 LF
	json     string // 同一占位符在 JSON 模板 {"v":…} 中输出的值，空表示该行不展示 JSON
}

// readmeLevelRow 是 Level 缩写表的一行：format 是 README 展示的格式，same 中的格式必须输出相同的结果，
// 空字符串表示省略格式；want 依次对应表格各列的级别。
type readmeLevelRow struct {
	format string
	same   []string
	want   []string
}

func readmeBase() slog.Record {
	return slog.NewRecord(testTime(), slog.LevelInfo, "Hello World", 0)
}

func readmeLevel(level slog.Level) slog.Record {
	r := readmeBase()
	r.Level = level
	return r
}

// readmeSite 的调用位置固定为 /srv/app/internal/gate/gate.go 第 100 行的 (*gate).serve。
func readmeSite() slog.Record {
	r := readmeBase()
	r.PC = new(gate).serve()
	return r
}

func readmeMultiline() slog.Record {
	return slog.NewRecord(testTime(), slog.LevelInfo, "first line\nsecond line", 0)
}

func readmeCST() slog.Record {
	return slog.NewRecord(testTime().In(time.FixedZone("CST", 8*3600)), slog.LevelInfo, "Hello World", 0)
}

// readmeSections 与 README 中的示例表格一一对应。
var readmeSections = []struct {
	name   string
	record func() slog.Record
	rows   []readmeRow
}{
	{"引号动词", readmeBase, []readmeRow{
		{`${Message}`, `"Hello World"`, `"Hello World"`},
		{`${Message:l}`, `Hello World`, `"Hello World"`},
		{`${Message:lj}`, `Hello World`, `"Hello World"`},
		{`${Message:j}`, `"Hello World"`, `"Hello World"`},
		{`${APP_ENV}`, `"Prod"`, `"Prod"`},
		{`${APP_ENV:l}`, `Prod`, `"Prod"`},
		{`${Level}`, `INFO`, `"INFO"`},
		{`${Level:j}`, `"INFO"`, `"INFO"`},
	}},
	{"大小写动词", readmeBase, []readmeRow{
		{`${Message:u}`, `"HELLO WORLD"`, `"HELLO WORLD"`},
		{`${Message:w}`, `"hello world"`, `"hello world"`},
		{`${Message:t0}`, `"Hello world"`, `"Hello world"`},
		{`${Message:u3}`, `"HELLO WORLD"`, `"HELLO WORLD"`},
		{`${APP_ENV:u}`, `"PROD"`, `"PROD"`},
		{`${Level:w}`, `info`, `"info"`},
	}},
	{"连写", readmeBase, []readmeRow{
		{`${Message:lu}`, `HELLO WORLD`, `"HELLO WORLD"`},
		{`${Message:lw}`, `hello world`, `"hello world"`},
		{`${Message:lt0}`, `Hello world`, `"Hello world"`},
		{`${Message:ju}`, `"HELLO WORLD"`, `"HELLO WORLD"`},
		{`${APP_ENV:lu}`, `PROD`, `"PROD"`},
		{`${Level:lu3}`, `INF`, `"INF"`},
		{`${Level:ju}`, `"INFO"`, `"INFO"`},
	}},
	{"不是动词的写法", readmeBase, []readmeRow{
		{`${Message:ul}`, `"Hello World"`, `"Hello World"`},
		{`${Message:jl}`, `"Hello World"`, `"Hello World"`},
		{`${Message:lt}`, `"Hello World"`, `"Hello World"`},
		{`${Message:W}`, `"Hello World"`, `"Hello World"`},
		{`${Message:u+3}`, `"Hello World"`, `"Hello World"`},
		{`${Message:uabc}`, `"Hello World"`, `"Hello World"`},
	}},
	{"调用信息和进程信息", readmeSite, []readmeRow{
		{`${FileName}`, `gate.go`, `"gate.go"`},
		{`${FileName:u}`, `GATE.GO`, `"GATE.GO"`},
		{`${FileName:t0}`, `Gate.go`, `"Gate.go"`},
		{`${FileName:j}`, `"gate.go"`, `"gate.go"`},
		{`${Source}`, `gate/gate.go:100`, `"gate/gate.go:100"`},
		{`${Source:u}`, `GATE/GATE.GO:100`, `"GATE/GATE.GO:100"`},
		{`${Function}`, `(*gate).serve`, `"(*gate).serve"`},
		{`${Function:u}`, `(*GATE).SERVE`, `"(*GATE).SERVE"`},
		{`${Line}`, `100`, `100`},
		{`${Line:u}`, `100`, `100`},
		{`${Line:j}`, `100`, `100`},
	}},
	{"多行字符串", readmeMultiline, []readmeRow{
		{`${Message}`, `"first line\u000asecond line"`, `["first line","second line"]`},
		{`${Message:l}`, `first line\u000asecond line`, `["first line","second line"]`},
		{`${Message:u}`, `"FIRST LINE\u000aSECOND LINE"`, `["FIRST LINE","SECOND LINE"]`},
	}},
	{"对齐", readmeBase, []readmeRow{
		{`[${Message,-14:l}]`, `[Hello World   ]`, ""},
		{`[${Message,14:l}]`, `[   Hello World]`, ""},
		{`[${Message,-14}]`, `["Hello World" ]`, ""},
		{`[${APP_ENV,-8:lu}]`, `[PROD    ]`, ""},
	}},
	{"Go 时间布局", readmeBase, []readmeRow{
		{`${Time}`, `2024-01-02T03:04:05.678Z`, `"2024-01-02T03:04:05.678Z"`},
		{`${Time:2006-01-02 15:04:05}`, `2024-01-02 03:04:05`, `"2024-01-02 03:04:05"`},
		{`${Time:2006-01-02 15:04:05.000}`, `2024-01-02 03:04:05.678`, `"2024-01-02 03:04:05.678"`},
		{`${Time:15:04:05.000}`, `03:04:05.678`, `"03:04:05.678"`},
		{`${Time:15:04:05.000000}`, `03:04:05.678901`, `"03:04:05.678901"`},
		{`${Time:2006-01-02T15:04:05Z07:00}`, `2024-01-02T03:04:05Z`, `"2024-01-02T03:04:05Z"`},
		{`${Time:Jan _2 15:04:05}`, `Jan  2 03:04:05`, `"Jan  2 03:04:05"`},
		{`${Time:Jan 2, 2006}`, `Jan 2, 2024`, `"Jan 2, 2024"`},
		{`${Time:02/Jan/2006:15:04:05 -0700}`, `02/Jan/2024:03:04:05 +0000`, `"02/Jan/2024:03:04:05 +0000"`},
		{`${Time:Mon, 02 Jan 2006 15:04:05 MST}`, `Tue, 02 Jan 2024 03:04:05 UTC`, `"Tue, 02 Jan 2024 03:04:05 UTC"`},
		{`${Time:20060102-150405}`, `20240102-030405`, `"20240102-030405"`},
		{`${Time:3:04PM}`, `3:04AM`, `"3:04AM"`},
		{`${Time:time=15:04:05}`, `time=03:04:05`, `"time=03:04:05"`},
	}},
	{"动词在 Time 上的行为", readmeBase, []readmeRow{
		{`${Time:j}`, `"2024-01-02T03:04:05.678Z"`, `"2024-01-02T03:04:05.678Z"`},
		{`${Time:l}`, `2024-01-02T03:04:05.678Z`, `"2024-01-02T03:04:05.678Z"`},
		{`${Time:lj}`, `2024-01-02T03:04:05.678Z`, `"2024-01-02T03:04:05.678Z"`},
		{`${Time:u}`, `2024-01-02T03:04:05.678Z`, `"2024-01-02T03:04:05.678Z"`},
		{`${Time:w}`, `2024-01-02T03:04:05.678Z`, `"2024-01-02T03:04:05.678Z"`},
		{`${Time:t3}`, `2024-01-02T03:04:05.678Z`, `"2024-01-02T03:04:05.678Z"`},
		{`${Time:lu}`, `2024-01-02T03:04:05.678Z`, `"2024-01-02T03:04:05.678Z"`},
		{`${Time:d}`, `2024-01-02T03:04:05.678Z`, `"2024-01-02T03:04:05.678Z"`},
		{`${Time:f}`, `2024-01-02T03:04:05.678Z`, `"2024-01-02T03:04:05.678Z"`},
		{`${Time:t}`, `2024-01-02T03:04:05.678Z`, `"2024-01-02T03:04:05.678Z"`},
		{`${Time:06d}`, `2024-01-02T03:04:05.678Z`, `"2024-01-02T03:04:05.678Z"`},
	}},
	{"时间戳占位符", readmeBase, []readmeRow{
		{`${TimeUnix}`, `1704164645`, `1704164645`},
		{`${TimeUnixMilli}`, `1704164645678`, `1704164645678`},
		{`${TimeUnixMicro}`, `1704164645678901`, `1704164645678901`},
		{`${TimeUnixNano}`, `1704164645678901234`, `1704164645678901234`},
	}},
	{"时区", readmeCST, []readmeRow{
		{`${Time}`, `2024-01-02T11:04:05.678+08:00`, `"2024-01-02T11:04:05.678+08:00"`},
		{`${Time:15:04:05 MST}`, `11:04:05 CST`, `"11:04:05 CST"`},
		{`${TimeUnixMilli}`, `1704164645678`, `1704164645678`},
	}},
	{"Level 的动词", readmeBase, []readmeRow{
		{`${Level}`, `INFO`, `"INFO"`},
		{`${Level:l}`, `INFO`, `"INFO"`},
		{`${Level:j}`, `"INFO"`, `"INFO"`},
		{`${Level:u}`, `INFO`, `"INFO"`},
		{`${Level:w}`, `info`, `"info"`},
		{`${Level:t0}`, `Info`, `"Info"`},
		{`${Level:u3}`, `INF`, `"INF"`},
		{`${Level:w3}`, `inf`, `"inf"`},
		{`${Level:t3}`, `Inf`, `"Inf"`},
		{`${Level:lu3}`, `INF`, `"INF"`},
		{`${Level:ju3}`, `"INF"`, `"INF"`},
		{`${Level:d}`, `INFO`, `"INFO"`},
	}},
	{"Level 对齐", readmeBase, []readmeRow{
		{`[${Level,-5}]`, `[INFO ]`, ""},
		{`[${Level,5}]`, `[ INFO]`, ""},
		{`[${Level,-5:w}]`, `[info ]`, ""},
		{`[${Level,6:u3}]`, `[   INF]`, ""},
		{`[${Level,-6:w3}]`, `[inf   ]`, ""},
	}},
	{"环境变量的类型动词", readmeBase, []readmeRow{
		{`${PORT}`, `"8080"`, `"8080"`},
		{`${PORT:d}`, `8080`, `8080`},
		{`${PORT:l}`, `8080`, `"8080"`},
		{`${RATIO:f}`, `1.25`, `1.25`},
		{`${DEBUG}`, `"true"`, `"true"`},
		{`${DEBUG:t}`, `true`, `true`},
		{`${DEBUG:t3}`, `"True"`, `"True"`},
	}},
}

var (
	readmeStandardLevels = []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError}
	readmeCustomLevels   = []slog.Level{slog.LevelInfo + 2, slog.LevelDebug - 4, slog.LevelError + 4}

	// readmeLevelAbbreviations 的列依次为 DEBUG、INFO、WARN、ERROR。
	readmeLevelAbbreviations = []readmeLevelRow{
		{`u1`, nil, []string{`D`, `I`, `W`, `E`}},
		{`w1`, nil, []string{`d`, `i`, `w`, `e`}},
		{`t1`, nil, []string{`D`, `I`, `W`, `E`}},
		{`u2`, nil, []string{`DE`, `IN`, `WN`, `ER`}},
		{`w2`, nil, []string{`de`, `in`, `wn`, `er`}},
		{`t2`, nil, []string{`De`, `In`, `Wn`, `Er`}},
		{`u3`, nil, []string{`DBG`, `INF`, `WRN`, `ERR`}},
		{`w3`, nil, []string{`dbg`, `inf`, `wrn`, `err`}},
		{`t3`, nil, []string{`Dbg`, `Inf`, `Wrn`, `Err`}},
		{`u4`, nil, []string{`DBUG`, `INFO`, `WARN`, `EROR`}},
		{`w4`, nil, []string{`dbug`, `info`, `warn`, `eror`}},
		{`t4`, nil, []string{`Dbug`, `Info`, `Warn`, `Eror`}},
		{`u`, []string{``, `u0`, `u5`, `u6`, `u9`, `u12`}, []string{`DEBUG`, `INFO`, `WARN`, `ERROR`}},
		{`w`, []string{`w0`, `w5`, `w6`, `w9`}, []string{`debug`, `info`, `warn`, `error`}},
		{`t0`, []string{`t5`, `t6`, `t9`}, []string{`Debug`, `Info`, `Warn`, `Error`}},
	}

	// readmeCustomLevelRows 的列依次为 INFO+2、DEBUG-4、ERROR+4。
	readmeCustomLevelRows = []readmeLevelRow{
		{`u`, nil, []string{`INFO+2`, `DEBUG-4`, `ERROR+4`}},
		{`w`, nil, []string{`info+2`, `debug-4`, `error+4`}},
		{`t0`, nil, []string{`Info+2`, `Debug-4`, `Error+4`}},
		{`u3`, nil, []string{`INF`, `DEB`, `ERR`}},
		{`w3`, nil, []string{`inf`, `deb`, `err`}},
		{`t3`, nil, []string{`Inf`, `Deb`, `Err`}},
		{`u5`, nil, []string{`INFO+`, `DEBUG`, `ERROR`}},
		{`u6`, nil, []string{`INFO+2`, `DEBUG-`, `ERROR+`}},
	}
)

func setReadmeEnv(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "Prod")
	t.Setenv("PORT", "8080")
	t.Setenv("RATIO", "1.25")
	t.Setenv("DEBUG", "true")
}

// readREADME 读取 README，统一为 LF 后按行检查；测试目录中没有 README 时跳过。
func readREADME(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("README.md")
	if errors.Is(err, fs.ErrNotExist) {
		t.Skip("测试目录中没有 README.md")
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

// requireREADMEExample 要求 README 中有一行同时把全部文本写成代码片段。
func requireREADMEExample(t *testing.T, readme string, texts ...string) {
	t.Helper()
	for line := range strings.SplitSeq(readme, "\n") {
		found := true
		for _, text := range texts {
			if !strings.Contains(line, "`"+text+"`") {
				found = false
				break
			}
		}
		if found {
			return
		}
	}
	t.Errorf("README 中没有同时写出 %q 的行", texts)
}

// readmeText 用文本模板渲染记录，去掉 Handler 在末尾补齐的 LF。
func readmeText(t *testing.T, record slog.Record, template string) string {
	t.Helper()
	return strings.TrimSuffix(renderRecord(t, record, WithTemplate(template)), "\n")
}

// readmeJSON 把单个占位符放进 JSON 模板 {"v":<占位符>}，返回 v 的紧凑 JSON；成员被省略时返回 {}。
func readmeJSON(t *testing.T, record slog.Record, placeholder string) string {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(renderRecord(t, record, WithTemplate(`{"v":`+placeholder+`}`)))); err != nil {
		t.Fatal(err)
	}
	if compact.String() == "{}" {
		return "{}"
	}
	return strings.TrimSuffix(strings.TrimPrefix(compact.String(), `{"v":`), "}")
}

func TestREADMEFormatExamples(t *testing.T) {
	setReadmeEnv(t)
	readme := readREADME(t)
	for _, section := range readmeSections {
		t.Run(section.name, func(t *testing.T) {
			record := section.record()
			for _, row := range section.rows {
				if got := readmeText(t, record, row.template); got != row.text {
					t.Errorf("%s 的文本输出 = %q，期望 %q", row.template, got, row.text)
				}
				texts := []string{row.template, row.text}
				if row.json != "" {
					if got := readmeJSON(t, record, row.template); got != row.json {
						t.Errorf("%s 在 JSON 中输出 %s，期望 %s", row.template, got, row.json)
					}
					texts = append(texts, row.json)
				}
				requireREADMEExample(t, readme, texts...)
			}
		})
	}
}

// 缩写表的每一行：同一行的所有格式输出相同，JSON 中是带引号的同一个字符串。
func TestREADMELevelExamples(t *testing.T) {
	readme := readREADME(t)
	check := func(t *testing.T, levels []slog.Level, rows []readmeLevelRow) {
		t.Helper()
		for _, row := range rows {
			for _, format := range append([]string{row.format}, row.same...) {
				template := "${Level}"
				if format != "" {
					template = "${Level:" + format + "}"
				}
				for i, level := range levels {
					record := readmeLevel(level)
					if got := readmeText(t, record, template); got != row.want[i] {
						t.Errorf("级别 %s 的 %s = %q，期望 %q", level, template, got, row.want[i])
					}
					if got, want := readmeJSON(t, record, template), strconv.Quote(row.want[i]); got != want {
						t.Errorf("级别 %s 的 %s 在 JSON 中输出 %s，期望 %s", level, template, got, want)
					}
				}
			}
			requireREADMEExample(t, readme, append([]string{row.format}, row.want...)...)
		}
	}
	t.Run("标准级别", func(t *testing.T) { check(t, readmeStandardLevels, readmeLevelAbbreviations) })
	t.Run("自定义级别", func(t *testing.T) { check(t, readmeCustomLevels, readmeCustomLevelRows) })
}

// 总表中每个占位符的声明：大小写动词转换整个输出，N 只对 Level 有意义，类型动词只属于环境变量，
// Line 和 PID 不受任何动词影响。
func TestREADMEFormatMatrix(t *testing.T) {
	setReadmeEnv(t)
	record := readmeSite()

	t.Run("字符串占位符", func(t *testing.T) {
		for _, name := range []string{
			"Level", "Message", "Source", "File", "FileName", "Function", "Package", "Hostname", "Executable", "APP_ENV",
		} {
			base := readmeText(t, record, "${"+name+"}")
			if base == "" {
				t.Logf("%s 在当前环境中为空，跳过", name)
				continue
			}
			text := func(format string) string { return readmeText(t, record, "${"+name+":"+format+"}") }
			if got, want := text("u"), strings.ToUpper(base); got != want {
				t.Errorf("%s:u = %q，期望 %q", name, got, want)
			}
			if got, want := text("w"), strings.ToLower(base); got != want {
				t.Errorf("%s:w = %q，期望 %q", name, got, want)
			}
			// 默认带引号的值，l 去掉引号；其余本来就不带引号，l 和 lj 没有变化。
			wantPlain := base
			if name == "Message" || name == "APP_ENV" {
				wantPlain = strings.Trim(base, `"`)
			}
			for _, format := range []string{"l", "lj"} {
				if got := text(format); got != wantPlain {
					t.Errorf("%s:%s = %q，期望 %q", name, format, got, wantPlain)
				}
			}
			if name != "Level" {
				for _, tc := range []struct{ with, without string }{{"u3", "u"}, {"w3", "w"}, {"t3", "t0"}} {
					if got, want := text(tc.with), text(tc.without); got != want {
						t.Errorf("%s:%s = %q，应与 %s 相同 (%q)", name, tc.with, got, tc.without, want)
					}
				}
			}
			// 内置元数据忽略类型动词；环境变量则按值解析，APP_ENV=Prod 无法解析，保持字符串。
			for _, format := range []string{"d", "f", "t", "b"} {
				if got := text(format); got != base {
					t.Errorf("%s:%s = %q，期望 %q", name, format, got, base)
				}
				if got, want := readmeJSON(t, record, "${"+name+":"+format+"}"), readmeJSON(t, record, "${"+name+"}"); got != want {
					t.Errorf("%s:%s 在 JSON 中输出 %s，期望 %s", name, format, got, want)
				}
			}
		}
	})

	// Time 的格式是 Go 布局：不适用于时间的动词被忽略，输出与省略格式时相同，不会原样输出；
	// 整个格式不是一个动词时是布局，以动词字母开头的文字照常输出。
	t.Run("Time 忽略不适用的动词", func(t *testing.T) {
		base, baseJSON := readmeText(t, record, "${Time}"), readmeJSON(t, record, "${Time}")
		for _, format := range []string{
			"l", "lj", "u", "w", "t3", "t0", "lu", "lw3", "ljt3",
			"d", "b", "o", "O", "c", "U", "e", "E", "f", "F", "g", "G", "t", "06d", ".2f", "[1]t",
		} {
			template := "${Time:" + format + "}"
			if got := readmeText(t, record, template); got != base {
				t.Errorf("%s = %q，期望 %q", template, got, base)
			}
			if got := readmeJSON(t, record, template); got != baseJSON {
				t.Errorf("%s 在 JSON 中输出 %s，期望 %s", template, got, baseJSON)
			}
		}
		if got, want := readmeText(t, record, "${Time:j}"), strconv.Quote(base); got != want {
			t.Errorf("${Time:j} = %q，期望 %q", got, want)
		}
		for _, tc := range []struct{ format, want string }{
			{"time=15:04:05", "time=03:04:05"}, {"date=2006-01-02", "date=2024-01-02"}, {"d items", "d items"},
		} {
			if got := readmeText(t, record, "${Time:"+tc.format+"}"); got != tc.want {
				t.Errorf("${Time:%s} = %q，期望 %q", tc.format, got, tc.want)
			}
		}
	})

	t.Run("数值占位符不受任何动词影响", func(t *testing.T) {
		for _, name := range []string{"Line", "PID", "TimeUnix", "TimeUnixMilli", "TimeUnixMicro", "TimeUnixNano"} {
			base, baseJSON := readmeText(t, record, "${"+name+"}"), readmeJSON(t, record, "${"+name+"}")
			if _, err := strconv.ParseInt(base, 10, 64); err != nil || baseJSON != base {
				t.Fatalf("%s 的文本输出 %q、JSON 输出 %s 应是同一个数字", name, base, baseJSON)
			}
			for _, format := range []string{"l", "lj", "j", "u", "w", "t3", "lu", "ju", "d", "f", "t"} {
				template := "${" + name + ":" + format + "}"
				if got := readmeText(t, record, template); got != base {
					t.Errorf("%s = %q，期望 %q", template, got, base)
				}
				if got := readmeJSON(t, record, template); got != baseJSON {
					t.Errorf("%s 在 JSON 中输出 %s，期望 %s", template, got, baseJSON)
				}
			}
		}
	})
}

// README 正文中的规则，表格无法直接表达的部分。
func TestREADMEFormatRules(t *testing.T) {
	setReadmeEnv(t)
	readme := readREADME(t)

	t.Run("NewLine 忽略全部格式", func(t *testing.T) {
		want := readmeText(t, readmeBase(), "a${NewLine}b")
		if want != "a\nb" {
			t.Fatalf("a${NewLine}b = %q", want)
		}
		for _, format := range []string{"u", "w", "t3", "l", "lj", "j", "d", "lu3"} {
			if got := readmeText(t, readmeBase(), "a${NewLine:"+format+"}b"); got != want {
				t.Errorf("NewLine:%s = %q，期望 %q", format, got, want)
			}
		}
	})

	t.Run("多行字符串的 j 与 JSON 模板输出同一个数组", func(t *testing.T) {
		var compact bytes.Buffer
		if err := json.Compact(&compact, []byte(readmeText(t, readmeMultiline(), "${Message:j}"))); err != nil {
			t.Fatal(err)
		}
		if got, want := compact.String(), readmeJSON(t, readmeMultiline(), "${Message}"); got != want {
			t.Fatalf("文本模板的 ${Message:j} = %s，JSON 模板的 ${Message} = %s", got, want)
		}
	})

	t.Run("Level 总是有值，不回退到环境变量", func(t *testing.T) {
		t.Setenv("Level", "from env")
		if got := readmeText(t, readmeBase(), "${Level}"); got != "INFO" {
			t.Fatalf("${Level} = %q", got)
		}
	})

	t.Run("时间为零值时按缺失处理", func(t *testing.T) {
		zero := slog.NewRecord(time.Time{}, slog.LevelInfo, "Hello World", 0)
		for _, tc := range []struct{ template, want string }{
			{"[${Time}] ${Message:l}", `[] Hello World`},
			{`{"time":${Time},"message":${Message}}`, `{"message":"Hello World"}`},
			{`{"at":${TimeUnixMilli},"message":${Message}}`, `{"message":"Hello World"}`},
		} {
			if got := readmeText(t, zero, tc.template); got != tc.want {
				t.Errorf("%s = %q，期望 %q", tc.template, got, tc.want)
			}
			if !strings.Contains(readme, "// "+tc.want+"\n") {
				t.Errorf("README 中没有示例输出 // %s", tc.want)
			}
		}
		// 先回退同名环境变量。
		t.Setenv("Time", "from env")
		if got, want := readmeText(t, zero, "${Time}"), `"from env"`; got != want {
			t.Fatalf("环境变量回退 = %q，期望 %q", got, want)
		}
	})
}
