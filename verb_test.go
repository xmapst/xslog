package xslog

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// 类型动词的语义：Injector.Inject 和输出模板都经由 verb.envValue，这里逐条固定。
func TestVerbEnvValue(t *testing.T) {
	check := func(text, format string, kind slog.Kind, want string) {
		t.Helper()
		if got := parseVerb(format).envValue(text); got.Kind() != kind || got.String() != want {
			t.Errorf("parseVerb(%q).envValue(%q) = %s %q, want %s %q", format, text, got.Kind(), got.String(), kind, want)
		}
	}
	for _, name := range []string{"d", "b", "o", "O", "c", "U"} {
		check("65", name, slog.KindInt64, "65")
	}
	for _, name := range []string{"e", "E", "f", "F", "g", "G"} {
		check("1.25", name, slog.KindFloat64, "1.25")
	}
	for _, text := range []string{"1", "t", "T", "TRUE", "true", "True"} {
		check(text, "t", slog.KindBool, "true")
	}
	for _, text := range []string{"0", "f", "F", "FALSE", "false", "False"} {
		check(text, "t", slog.KindBool, "false")
	}

	// 整数的边界：符号、前导零，超出 int64 的无符号数。
	check("-12", "d", slog.KindInt64, "-12")
	check("+5", "d", slog.KindInt64, "5")
	check("007", "d", slog.KindInt64, "7")
	check("9223372036854775807", "d", slog.KindInt64, "9223372036854775807")
	check("9223372036854775808", "d", slog.KindUint64, "9223372036854775808")
	check("18446744073709551615", "d", slog.KindUint64, "18446744073709551615")

	// 浮点数：整数文本、指数、多余的小数位、非有限值。
	check("8080", "f", slog.KindFloat64, "8080")
	check("1e3", "g", slog.KindFloat64, "1000")
	check("1.20", "f", slog.KindFloat64, "1.2")
	check("NaN", "f", slog.KindFloat64, "NaN")
	check("inf", "f", slog.KindFloat64, "+Inf")

	// 动词之前的标志、宽度、精度和参数序号，以及动词之后的字面量，不影响识别。
	for _, format := range []string{"06d", "+d", "-8d", "# d", "[1]d", "d items", "d%%"} {
		check("8080", format, slog.KindInt64, "8080")
	}
	for _, format := range []string{".2f", "8.3f", "-8.3f", "+.1e", "g items"} {
		check("1.25", format, slog.KindFloat64, "1.25")
	}
	check("true", "t items", slog.KindBool, "true")

	// 不是类型动词的格式不改变类型：fmt 的字符串动词、模板的其他动词和时间布局。
	// 带宽度的 t 是标题大小写，不是布尔类型动词。
	for _, format := range []string{"", "s", "v", "q", "x", "X", "l", "lj", "j", "u", "w", "u3", "w3", "t3", "t0",
		"lu", "lw3", "lt3", "ju", "ljt3",
		"2006-01-02", "15:04:05", "unit", "Jan 2", "06", "[1]", "*d"} {
		check("8080", format, slog.KindString, "8080")
	}
	check("true", "t3", slog.KindString, "true")
	check("false", "lt3", slog.KindString, "false")

	// 无法解析时保持字符串，不丢失原值。
	for _, tc := range []struct{ text, format string }{
		{"abc", "d"}, {"1.5", "d"}, {"0x1F", "d"}, {"1_000", "d"}, {" 8080", "d"}, {"8080 ", "d"}, {"", "d"},
		{"99999999999999999999", "d"},
		{"abc", "f"}, {"1,5", "f"}, {"", "f"},
		{"yes", "t"}, {"2", "t"}, {"", "t"},
	} {
		check(tc.text, tc.format, slog.KindString, tc.text)
	}
}

func TestVerbLookupEnv(t *testing.T) {
	t.Setenv("XSLOG_TEST_VERB", "8080")
	unsetTestEnv(t, "XSLOG_TEST_VERB_ABSENT")
	if v, ok := parseVerb("d").lookupEnv("XSLOG_TEST_VERB"); !ok || v.Kind() != slog.KindInt64 || v.Int64() != 8080 {
		t.Fatalf("typed lookup = %v, %v", v, ok)
	}
	if v, ok := parseVerb("").lookupEnv("XSLOG_TEST_VERB"); !ok || v.Kind() != slog.KindString || v.String() != "8080" {
		t.Fatalf("plain lookup = %v, %v", v, ok)
	}
	// 空值算存在；空文本无法解析，保持字符串。
	t.Setenv("XSLOG_TEST_VERB", "")
	if v, ok := parseVerb("d").lookupEnv("XSLOG_TEST_VERB"); !ok || v.Kind() != slog.KindString || v.String() != "" {
		t.Fatalf("empty lookup = %v, %v", v, ok)
	}
	if _, ok := parseVerb("d").lookupEnv("XSLOG_TEST_VERB_ABSENT"); ok {
		t.Fatal("absent variable reported as found")
	}
}

var envValueSink slog.Value

// 没有动词和动词转换成功的路径不分配内存，渲染记录时每个环境变量占位符都会经过这里。
func TestVerbEnvValueDoesNotAllocate(t *testing.T) {
	for _, tc := range []struct{ text, format string }{
		{"8080", ""}, {"8080", "l"}, {"8080", "j"}, {"8080", "u"}, {"8080", "u3"}, {"8080", "t3"}, {"8080", "lu"},
		{"8080", "d"}, {"8080", "06d"}, {"1.25", "f"}, {"true", "t"},
	} {
		if n := testing.AllocsPerRun(100, func() { envValueSink = parseVerb(tc.format).envValue(tc.text) }); n != 0 {
			t.Errorf("parseVerb(%q).envValue(%q) allocates %v times", tc.format, tc.text, n)
		}
	}
}

// 呈现动词的语法：引号动词 l、lj、j 在前，大小写动词 u、w（可带宽度）或必须带宽度的 t 在后，
// 两部分都可以省略；区分大小写，不接受其他字符。不符合语法的格式只保留原文。
func TestVerbParse(t *testing.T) {
	plain := func(format string, mode caseMode, width int) verb {
		return verb{text: format, quote: quotePlain, mode: mode, width: width}
	}
	for _, tc := range []struct {
		format string
		want   verb
	}{
		{"", verb{}},
		// 引号动词。
		{"l", plain("l", caseNone, 0)}, {"lj", plain("lj", caseNone, 0)}, {"j", verb{text: "j", quote: quoteJSON}},
		// 大小写动词：u、w 的宽度可以省略，t 必须带宽度。
		{"u", verb{text: "u", mode: caseUpper}}, {"w", verb{text: "w", mode: caseLower}},
		{"u3", verb{text: "u3", mode: caseUpper, width: 3}}, {"w12", verb{text: "w12", mode: caseLower, width: 12}},
		{"u0", verb{text: "u0", mode: caseUpper}}, {"u03", verb{text: "u03", mode: caseUpper, width: 3}},
		{"t3", verb{text: "t3", mode: caseTitle, width: 3}}, {"t0", verb{text: "t0", mode: caseTitle}},
		{"t12", verb{text: "t12", mode: caseTitle, width: 12}},
		// 连写：引号动词在前。
		{"lu", plain("lu", caseUpper, 0)}, {"lw3", plain("lw3", caseLower, 3)}, {"lt4", plain("lt4", caseTitle, 4)},
		{"ljw", plain("ljw", caseLower, 0)}, {"ljt3", plain("ljt3", caseTitle, 3)}, {"lju3", plain("lju3", caseUpper, 3)},
		{"ju", verb{text: "ju", quote: quoteJSON, mode: caseUpper}}, {"jt3", verb{text: "jt3", quote: quoteJSON, mode: caseTitle, width: 3}},
		// 不符合语法：只保留原文。
		{"t", verb{text: "t"}}, // 单独的 t 是布尔类型动词。
		{"L", verb{text: "L"}}, {"J", verb{text: "J"}}, {"U", verb{text: "U"}}, {"W3", verb{text: "W3"}}, {"T3", verb{text: "T3"}},
		{"jl", verb{text: "jl"}}, {"ll", verb{text: "ll"}}, {"jj", verb{text: "jj"}}, {"lj3", verb{text: "lj3"}},
		{"lt", verb{text: "lt"}}, {"ljt", verb{text: "ljt"}}, {"jt", verb{text: "jt"}},
		{"ul", verb{text: "ul"}}, {"wl", verb{text: "wl"}}, {"tl", verb{text: "tl"}}, {"uu", verb{text: "uu"}}, {"uj", verb{text: "uj"}},
		{"u-1", verb{text: "u-1"}}, {"u+3", verb{text: "u+3"}}, {"u 3", verb{text: "u 3"}}, {"u3x", verb{text: "u3x"}},
		{"3u", verb{text: "3u"}}, {"l ", verb{text: "l "}}, {"lx", verb{text: "lx"}}, {"tx", verb{text: "tx"}}, {"t-1", verb{text: "t-1"}},
		// 以 u、w、t 开头的普通文字、时间布局和类型动词不是呈现动词。
		{"unit", verb{text: "unit"}}, {"uabc", verb{text: "uabc"}},
		{"wed", verb{text: "wed"}}, {"d", verb{text: "d"}}, {"06d", verb{text: "06d"}}, {"2006-01-02", verb{text: "2006-01-02"}},
		{"15:04:05", verb{text: "15:04:05"}}, {"Jan 2", verb{text: "Jan 2"}},
	} {
		got := parseVerb(tc.format)
		if got != tc.want {
			t.Errorf("parseVerb(%q) = %+v, want %+v", tc.format, got, tc.want)
		}
		wantPresentation := tc.want.quote != quoteDefault || tc.want.mode != caseNone
		if got.isPresentation() != wantPresentation {
			t.Errorf("parseVerb(%q).isPresentation() = %v, want %v", tc.format, got.isPresentation(), wantPresentation)
		}
	}
	// 数字超出 int 时仍是大小写动词，宽度大到不会截断任何文本。
	if v := parseVerb("u99999999999999999999"); v.mode != caseUpper || v.width < 1<<30 {
		t.Errorf("parseVerb of an out-of-range width = %+v", v)
	}
}

// l、lj 去掉文本中字符串的引号，j 按 JSON 编码，可以和大小写动词连写；动词区分大小写，不做前缀匹配。
func TestVerbFlags(t *testing.T) {
	for _, tc := range []struct {
		format        string
		plain, isJSON bool
	}{
		{"", false, false},
		{"l", true, false}, {"lj", true, false}, {"j", false, true},
		{"lu", true, false}, {"lw3", true, false}, {"ljt3", true, false}, {"ju", false, true}, {"jt3", false, true},
		{"L", false, false}, {"J", false, false}, {"jl", false, false}, {"l ", false, false}, {"lt", false, false},
		{"u", false, false}, {"w", false, false}, {"u3", false, false}, {"t3", false, false}, {"d", false, false},
		{"unit", false, false},
	} {
		v := parseVerb(tc.format)
		if v.isPlain() != tc.plain || v.isJSON() != tc.isJSON {
			t.Errorf("parseVerb(%q): isPlain=%v isJSON=%v, want %v %v", tc.format, v.isPlain(), v.isJSON(), tc.plain, tc.isJSON)
		}
	}
}

// Level 的缩写表与 Serilog 的 u1 至 u4 一致：每个宽度都有可识别的写法，宽度超过行长时使用完整名称。
// 不在表中的名称（自定义级别）按宽度截断。
func TestVerbLevel(t *testing.T) {
	title := func(s string) string { return s[:1] + strings.ToLower(s[1:]) }
	for _, row := range []struct {
		name  string
		short []string
	}{
		{"DEBUG", []string{"D", "DE", "DBG", "DBUG", "DEBUG"}},
		{"INFO", []string{"I", "IN", "INF", "INFO"}},
		{"WARN", []string{"W", "WN", "WRN", "WARN"}},
		{"ERROR", []string{"E", "ER", "ERR", "EROR", "ERROR"}},
	} {
		for i, want := range row.short {
			width := fmt.Sprint(i + 1)
			for _, tc := range []struct{ format, want string }{
				{"u" + width, want}, {"w" + width, strings.ToLower(want)}, {"t" + width, title(want)},
				{"lu" + width, want}, {"jw" + width, strings.ToLower(want)}, {"ljt" + width, title(want)},
			} {
				if got := parseVerb(tc.format).level(row.name); got != tc.want {
					t.Errorf("parseVerb(%q).level(%q) = %q, want %q", tc.format, row.name, got, tc.want)
				}
			}
		}
		// 宽度超过行长、不限宽度（没有宽度或 0）都得到完整名称。
		for _, format := range []string{"u", "u0", "u5", "u6", "u10", "u99999999999999999999"} {
			if got := parseVerb(format).level(row.name); got != row.name {
				t.Errorf("parseVerb(%q).level(%q) = %q, want the full name", format, row.name, got)
			}
		}
		for _, format := range []string{"w", "w0", "w5", "w10"} {
			if got, want := parseVerb(format).level(row.name), strings.ToLower(row.name); got != want {
				t.Errorf("parseVerb(%q).level(%q) = %q, want %q", format, row.name, got, want)
			}
		}
		for _, format := range []string{"t0", "t5", "t10"} {
			if got, want := parseVerb(format).level(row.name), title(row.name); got != want {
				t.Errorf("parseVerb(%q).level(%q) = %q, want %q", format, row.name, got, want)
			}
		}
	}

	for _, tc := range []struct{ format, text, want string }{
		// 缩写表与简单截断不同的位置：WARN 的 2 位、DEBUG 和 ERROR 的 4 位。
		{"u2", "WARN", "WN"}, {"u4", "DEBUG", "DBUG"}, {"u4", "ERROR", "EROR"}, {"w4", "DEBUG", "dbug"},
		{"t3", "INFO", "Inf"}, {"t4", "ERROR", "Eror"}, {"t5", "DEBUG", "Debug"}, {"t0", "WARN", "Warn"},
		// 自定义级别不在表中：大小写之外按宽度截断，没有宽度或宽度足够时保持完整。
		{"u3", "INFO+2", "INF"}, {"u4", "INFO+2", "INFO"}, {"w2", "ERROR+4", "er"}, {"t3", "DEBUG-4", "Deb"},
		{"u", "INFO+2", "INFO+2"}, {"u9", "INFO+2", "INFO+2"}, {"t0", "INFO+2", "Info+2"}, {"w99999999999999999999", "WARN-1", "warn-1"},
		// 没有大小写动词时不改变文本：没有格式、引号动词、类型动词、单独的 t、时间布局。
		{"", "INFO", "INFO"}, {"l", "INFO", "INFO"}, {"lj", "INFO", "INFO"}, {"j", "INFO", "INFO"}, {"d", "INFO", "INFO"},
		{"t", "INFO", "INFO"}, {"2006", "INFO", "INFO"},
		// 不符合语法的格式不是动词：数字之后不能再有其他字符，也不接受符号和空白，区分大小写，引号动词在前。
		{"wabc", "INFO", "INFO"}, {"w3x", "INFO", "INFO"}, {"w-1", "INFO", "INFO"}, {"w+3", "INFO", "INFO"}, {"w 3", "INFO", "INFO"},
		{"W3", "INFO", "INFO"}, {"U3", "INFO", "INFO"}, {"T3", "INFO", "INFO"}, {"3w", "INFO", "INFO"}, {"wl", "INFO", "INFO"},
	} {
		if got := parseVerb(tc.format).level(tc.text); got != tc.want {
			t.Errorf("parseVerb(%q).level(%q) = %q, want %q", tc.format, tc.text, got, tc.want)
		}
	}
}

// 大小写动词作用于所有字符串，N 只属于 Level，对其他字符串没有作用；其他动词不改变文本。
func TestVerbApplyCase(t *testing.T) {
	for _, tc := range []struct{ format, text, want string }{
		{"u", "Hello 世界", "HELLO 世界"}, {"w", "Hello 世界", "hello 世界"}, {"u", "", ""},
		{"t3", "hELLO wORLD", "Hello world"}, {"t0", "ÉCOLE", "École"}, {"t1", "éCOLE", "École"}, {"t3", "世界abc", "世界abc"},
		{"u3", "hello", "HELLO"}, {"w2", "HeLLo", "hello"}, {"u0", "hello", "HELLO"}, {"w99999999999999999999", "HELLO", "hello"},
		// 连写：引号动词不影响转换。
		{"lu", "hello", "HELLO"}, {"lw", "HELLO", "hello"}, {"lt3", "hELLO", "Hello"}, {"ju", "hello", "HELLO"},
		{"ljw3", "HELLO", "hello"}, {"jt1", "hELLO", "Hello"},
		// 没有大小写动词时不改变文本。
		{"", "Hello", "Hello"}, {"l", "hELLO", "hELLO"}, {"lj", "Hello", "Hello"}, {"j", "Hello", "Hello"},
		{"d", "Hello", "Hello"}, {"t", "hELLO", "hELLO"}, {"U", "Hello", "Hello"}, {"uabc", "Hello", "Hello"},
		{"unit", "Hello", "Hello"}, {"2006-01-02", "Hello", "Hello"}, {"ul", "Hello", "Hello"},
	} {
		if got := parseVerb(tc.format).applyCase(tc.text); got != tc.want {
			t.Errorf("parseVerb(%q).applyCase(%q) = %q, want %q", tc.format, tc.text, got, tc.want)
		}
	}
}

// fold 和 levelMoniker 由 applyCase、level 在确认有大小写动词之后调用；直接以没有大小写动词的 verb 调用时，
// 文本保持原样，也不会返回缩写，调用方可以放心依赖这一点。
func TestVerbHelpersWithoutCaseVerb(t *testing.T) {
	for _, text := range []string{"", "Hello", "INFO+2"} {
		if got := (verb{}).fold(text); got != text {
			t.Errorf("verb{}.fold(%q) = %q, want it unchanged", text, got)
		}
	}
	for _, name := range []string{"DEBUG", "INFO", "WARN", "ERROR", "INFO+2", ""} {
		for _, width := range []int{0, 3} {
			if short, ok := levelMoniker(name, caseNone, width); ok || short != "" {
				t.Errorf("levelMoniker(%q, caseNone, %d) = %q, %v, want no moniker", name, width, short, ok)
			}
		}
	}
}

// 标题大小写：首个字符大写，其余小写，空文本和已经是这种形式的文本原样返回。
func TestTitleCase(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", ""}, {"a", "A"}, {"A", "A"}, {"hello", "Hello"}, {"Hello", "Hello"}, {"HELLO", "Hello"}, {"hELLO wORLD", "Hello world"},
		{"école", "École"}, {"ÉCOLE", "École"}, {"世界", "世界"}, {"1abc", "1abc"}, {" abc", " abc"},
	} {
		if got := titleCase(tc.in); got != tc.want {
			t.Errorf("titleCase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

var caseSink string

// 渲染记录时每个字符串占位符都会经过 applyCase，级别占位符会经过 level；没有改写文本的路径不分配内存。
func TestVerbCaseDoesNotAllocate(t *testing.T) {
	for _, tc := range []struct{ format, text string }{
		{"", "hello"}, {"l", "hello"}, {"lj", "hello"}, {"j", "hello"}, {"d", "hello"}, {"uabc", "hello"}, {"t", "hello"},
		{"w", "hello"}, {"w3", "hello"}, {"u", "HELLO"}, {"u3", "INFO"}, {"u", "INFO"}, {"u2", "ERROR"}, {"u4", "DEBUG"},
		{"t3", "Hello"}, {"t0", "Hello"}, {"lu", "HELLO"}, {"lw3", "hello"}, {"ju3", "INFO"}, {"ljt3", "Hello"},
	} {
		if n := testing.AllocsPerRun(100, func() { caseSink = parseVerb(tc.format).applyCase(tc.text) }); n != 0 {
			t.Errorf("parseVerb(%q).applyCase(%q) allocates %v times", tc.format, tc.text, n)
		}
		if n := testing.AllocsPerRun(100, func() { caseSink = parseVerb(tc.format).level(tc.text) }); n != 0 {
			t.Errorf("parseVerb(%q).level(%q) allocates %v times", tc.format, tc.text, n)
		}
	}
}

// DEBUG、INFO、WARN、ERROR 的缩写在初始化时按三种大小写算好，渲染时直接取用：
// 任何大小写、任何宽度（包括不限宽度和超过缩写表）以及连写，都不分配内存。
func TestVerbLevelDoesNotAllocate(t *testing.T) {
	var formats []string
	for _, mode := range []string{"u", "w", "t"} {
		if mode != "t" {
			formats = append(formats, mode) // 没有宽度；单独的 t 是布尔类型动词，标题大小写必须带宽度。
		}
		for width := range 8 {
			formats = append(formats, fmt.Sprintf("%s%d", mode, width))
		}
	}
	formats = append(formats, "lu3", "lw", "ljt3", "ju4")
	for _, name := range []string{"DEBUG", "INFO", "WARN", "ERROR"} {
		for _, format := range formats {
			v := parseVerb(format)
			if n := testing.AllocsPerRun(20, func() { caseSink = v.level(name) }); n != 0 {
				t.Errorf("parseVerb(%q).level(%q) allocates %v times", format, name, n)
			}
		}
	}
}

// 时间：没有格式，以及不适用于时间的动词（引号动词、大小写动词、类型动词和它们的连写）都被忽略，
// 使用默认布局（timeFormat 的零值），不会当作布局原样输出；其他都是 Go 布局，不属于布局的文字原样输出。
func TestVerbTimeFormat(t *testing.T) {
	tm := testTime()
	const defaultText = "2024-01-02T03:04:05.678Z"
	for _, format := range []string{
		"", "l", "lj", "j", "u", "w", "u3", "w3", "u12", "t3", "t0", "lu", "lw3", "ju", "ljt3",
		// 类型动词，包括带 fmt 标志、宽度、精度和参数序号的写法，以及单独的 t。
		"d", "b", "o", "O", "c", "U", "e", "E", "f", "F", "g", "G", "t", "06d", "-5d", "+d", ".2f", "8.3e", "[1]t",
	} {
		got := parseVerb(format).timeFormat()
		if got != "" {
			t.Errorf("parseVerb(%q).timeFormat() = %q, want the default layout (zero value)", format, got)
		}
		if text := got.text(tm); text != defaultText {
			t.Errorf("parseVerb(%q) formats the time as %q", format, text)
		}
	}
	for _, tc := range []struct{ format, want string }{
		{"2006-01-02 15:04:05", "2024-01-02 03:04:05"},
		{"uabc", "uabc"}, // 不是大小写动词的格式不会被当作动词忽略。
		{"lt", "lt"},     // t 必须带宽度，单独的 t 才是类型动词；lt 两者都不是。
		// 以动词字母开头或结尾的布局不是动词：整个格式必须只有一个动词。
		{"Feb 2", "Feb 2"}, {"15h", "03h"}, {"15h04m", "03h04m"}, {"20060102150405Z", "20240102030405Z"},
		{"2006-01-02T", "2024-01-02T"}, {"date=2006-01-02", "date=2024-01-02"}, {"dxyz", "dxyz"}, {"s", "s"}, {"x", "x"},
	} {
		if got := parseVerb(tc.format).timeFormat().text(tm); got != tc.want {
			t.Errorf("parseVerb(%q).timeFormat().text() = %q, want %q", tc.format, got, tc.want)
		}
	}
}

// typeVerb 是类型动词唯一的定义：字母分属整数、浮点数和布尔值三类，其余字母都不是；
// 动词之前可以有 fmt 的标志、宽度、精度和参数序号，之后的字面量不改变类型，只影响 exact。
func TestVerbTypeVerb(t *testing.T) {
	kinds := map[byte]typeKind{}
	for _, c := range "dboOcU" {
		kinds[byte(c)] = typeInt
	}
	for _, c := range "eEfFgG" {
		kinds[byte(c)] = typeFloat
	}
	kinds['t'] = typeBool
	for c := byte('A'); c <= 'z'; c++ {
		if c > 'Z' && c < 'a' {
			continue
		}
		kind, exact := parseVerb(string(c)).typeVerb()
		if want := kinds[c]; kind != want || exact != (want != typeNone) {
			t.Errorf("parseVerb(%q).typeVerb() = %d, %v, want %d, %v", string(c), kind, exact, want, want != typeNone)
		}
	}
	for _, tc := range []struct {
		format string
		kind   typeKind
		exact  bool
	}{
		{"06d", typeInt, true}, {"-8d", typeInt, true}, {"# d", typeInt, true}, {"[1]d", typeInt, true},
		{".2f", typeFloat, true}, {"+.1e", typeFloat, true}, {"[1]t", typeBool, true},
		{"d items", typeInt, false}, {"d%%", typeInt, false}, {"g items", typeFloat, false}, {"t x", typeBool, false},
		// 引号动词、大小写动词（包括带宽度的 t）和不属于类型动词的格式。
		{"", typeNone, false}, {"06", typeNone, false}, {"*d", typeNone, false}, {"unit", typeNone, false},
		{"l", typeNone, false}, {"lj", typeNone, false}, {"j", typeNone, false}, {"u", typeNone, false},
		{"u3", typeNone, false}, {"t3", typeNone, false}, {"t0", typeNone, false}, {"lu", typeNone, false},
		{"ljt3", typeNone, false}, {"2006-01-02", typeNone, false}, {"Jan 2", typeNone, false},
	} {
		if kind, exact := parseVerb(tc.format).typeVerb(); kind != tc.kind || exact != tc.exact {
			t.Errorf("parseVerb(%q).typeVerb() = %d, %v, want %d, %v", tc.format, kind, exact, tc.kind, tc.exact)
		}
	}
}

// isTypeVerb：整个格式恰好是一个类型动词，可以有 fmt 的标志、宽度、精度和参数序号作前缀；
// 动词之后还有内容的不是，所以以动词字母开头或结尾的时间布局不会被误认。
func TestVerbIsTypeVerb(t *testing.T) {
	for _, tc := range []struct {
		format string
		want   bool
	}{
		{"d", true}, {"t", true}, {"06d", true}, {"-8d", true}, {"+d", true}, {"# d", true}, {"[1]d", true},
		{".2f", true}, {"8.3f", true}, {"+.1e", true}, {"[1]t", true},
		{"", false}, {"06", false}, {"-0700", false}, {".000", false}, {"2006-01-02", false},
		{"dd", false}, {"d items", false}, {"d%%", false}, {"g items", false}, {"dxyz", false},
		{"Feb 2", false}, {"15h", false}, {"20060102150405Z", false}, {"2006-01-02T", false},
		{"s", false}, {"v", false}, {"q", false}, {"x", false}, {"X", false}, {"T", false}, {"D", false},
		{"u", false}, {"w", false}, {"l", false}, {"j", false}, {"t3", false}, {"lu", false},
	} {
		if got := parseVerb(tc.format).isTypeVerb(); got != tc.want {
			t.Errorf("parseVerb(%q).isTypeVerb() = %v, want %v", tc.format, got, tc.want)
		}
	}
}

// Injector.Inject 的格式化：没有格式按默认形式，时间使用时间格式，大小写动词转换字符串，
// 其余是 fmt 格式。
func TestVerbAppendAny(t *testing.T) {
	tm := testTime()
	defaultTime := timeFormat("").text(tm)
	for _, tc := range []struct {
		format string
		value  any
		want   string
	}{
		{"", 42, "42"}, {"", "世界", "世界"}, {"", nil, "<nil>"},
		{"04d", 42, "0042"}, {".2f", 1.25, "1.25"}, {"q", "世界", `"世界"`}, {"x", 255, "ff"},
		{"d", "abc", "%!d(string=abc)"}, {"t", true, "true"},
		{"2006-01-02", tm, "2024-01-02"}, {"15:04:05.000", tm, "03:04:05.678"},
		{"l", tm, defaultTime}, {"lj", tm, defaultTime}, {"j", tm, defaultTime},
		{"u", tm, defaultTime}, {"w3", tm, defaultTime}, {"t3", tm, defaultTime}, {"lu", tm, defaultTime},
		// 大小写动词转换字符串，N 被忽略；其他类型按默认形式输出；不是大小写动词的格式仍交给 fmt。
		{"u", "abc", "ABC"}, {"w", "ABC", "abc"}, {"u3", "abcdef", "ABCDEF"}, {"w", "Hello 世界", "hello 世界"},
		{"t3", "hELLO wORLD", "Hello world"}, {"t0", "ÉCOLE", "École"},
		{"u", 42, "42"}, {"w", true, "true"}, {"u", nil, "<nil>"}, {"w", []byte("ab"), "[97 98]"}, {"t3", 42, "42"},
		{"uabc", "x", "%!u(string=x)abc"}, {"U", "x", "%!U(string=x)"},
		// 连写中的引号动词对纯文本没有意义，被忽略。
		{"lu", "abc", "ABC"}, {"ju", "abc", "ABC"}, {"ljw3", "ABC", "abc"}, {"lt3", "aBC", "Abc"}, {"lu", 42, "42"},
	} {
		if got := string(parseVerb(tc.format).appendAny([]byte("prefix:"), tc.value)); got != "prefix:"+tc.want {
			t.Errorf("parseVerb(%q).appendAny(%v) = %q, want %q", tc.format, tc.value, got, "prefix:"+tc.want)
		}
	}
}

// 输出模板和 Injector 经由同一个动词管理器：JSON 保留数值和布尔类型，文本不加引号。
func TestTemplateEnvironmentVerbs(t *testing.T) {
	t.Setenv("XSLOG_TEST_PORT", "8080")
	t.Setenv("XSLOG_TEST_RATIO", "1.25")
	t.Setenv("XSLOG_TEST_DEBUG", "true")
	t.Setenv("XSLOG_TEST_BIG", "18446744073709551615")
	t.Setenv("XSLOG_TEST_NAN", "NaN")
	t.Setenv("XSLOG_TEST_BAD", "abc")
	t.Setenv("Line", "42")
	unsetTestEnv(t, "XSLOG_TEST_MISSING")

	t.Run("JSON", func(t *testing.T) {
		for _, tc := range []struct{ name, template, want string }{
			{"类型动词",
				`{"port":${XSLOG_TEST_PORT:d},"ratio":${XSLOG_TEST_RATIO:f},"debug":${XSLOG_TEST_DEBUG:t}}`,
				`{"port":8080,"ratio":1.25,"debug":true}`},
			{"没有格式保持字符串", `{"port":${XSLOG_TEST_PORT}}`, `{"port":"8080"}`},
			{"其他格式保持字符串",
				`{"l":${XSLOG_TEST_PORT:l},"lj":${XSLOG_TEST_PORT:lj},"j":${XSLOG_TEST_PORT:j},"s":${XSLOG_TEST_PORT:s},"x":${XSLOG_TEST_PORT:x}}`,
				`{"l":"8080","lj":"8080","j":"8080","s":"8080","x":"8080"}`},
			{"带宽度的 t 是标题大小写，不是布尔类型动词",
				`{"bool":${XSLOG_TEST_DEBUG:t},"title":${XSLOG_TEST_DEBUG:t3},"joined":${XSLOG_TEST_DEBUG:lt3}}`,
				`{"bool":true,"title":"True","joined":"True"}`},
			{"标志宽度精度不生效",
				`{"port":${XSLOG_TEST_PORT:06d},"ratio":${XSLOG_TEST_RATIO:.3f}}`,
				`{"port":8080,"ratio":1.25}`},
			{"超出 int64 的整数", `{"big":${XSLOG_TEST_BIG:d}}`, `{"big":18446744073709551615}`},
			{"非有限浮点数", `{"nan":${XSLOG_TEST_NAN:f}}`, `{"nan":"NaN"}`},
			{"无法解析保持字符串", `{"bad":${XSLOG_TEST_BAD:d}}`, `{"bad":"abc"}`},
			{"缺失仍然省略", `{"missing":${XSLOG_TEST_MISSING:d},"port":${XSLOG_TEST_PORT:d}}`, `{"port":8080}`},
			{"调用信息缺失时回退", `{"line":${Line:d}}`, `{"line":42}`},
			{"调用信息回退默认是字符串", `{"line":${Line}}`, `{"line":"42"}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				requireJSON(t, renderRecord(t, testRecord(), WithTemplate(tc.template)), tc.want)
			})
		}
	})

	t.Run("文本", func(t *testing.T) {
		for _, tc := range []struct{ name, template, want string }{
			{"类型动词不加引号",
				"port=${XSLOG_TEST_PORT:d} ratio=${XSLOG_TEST_RATIO:f} debug=${XSLOG_TEST_DEBUG:t} ${Message:l}",
				"port=8080 ratio=1.25 debug=true hello\n"},
			{"没有格式的字符串带引号", "port=${XSLOG_TEST_PORT} ${Message:l}", "port=\"8080\" hello\n"},
			{"l 去掉引号仍是字符串", "port=${XSLOG_TEST_PORT:l} ${Message:l}", "port=8080 hello\n"},
			{"带宽度的 t 是标题大小写", "${XSLOG_TEST_DEBUG:t}|${XSLOG_TEST_DEBUG:t3}|${XSLOG_TEST_DEBUG:lt3}", "true|\"True\"|True\n"},
			{"对齐", "[${XSLOG_TEST_PORT,6:d}][${XSLOG_TEST_PORT,-6:d}]", "[  8080][8080  ]\n"},
			{"无法解析保持字符串", "${XSLOG_TEST_BAD:d}", "\"abc\"\n"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := renderRecord(t, testRecord(), WithTemplate(tc.template)); got != tc.want {
					t.Fatalf("output = %q, want %q", got, tc.want)
				}
			})
		}
	})

	// README 的示例：环境变量取 PORT=8080、RATIO=1.25、DEBUG=true，消息为 listen on。
	t.Run("README 示例", func(t *testing.T) {
		r := slog.NewRecord(testTime(), slog.LevelInfo, "listen on", 0)
		got := renderRecord(t, r, WithTemplate(
			`{"port":${XSLOG_TEST_PORT:d},"ratio":${XSLOG_TEST_RATIO:f},"debug":${XSLOG_TEST_DEBUG:t},"message":${Message}}`))
		if want := `{"port":8080,"ratio":1.25,"debug":true,"message":"listen on"}` + "\n"; got != want {
			t.Fatalf("JSON output = %q, want %q", got, want)
		}
		got = renderRecord(t, r, WithTemplate(
			"port=${XSLOG_TEST_PORT:d} ratio=${XSLOG_TEST_RATIO:f} debug=${XSLOG_TEST_DEBUG:t} ${Message:l}"))
		if want := "port=8080 ratio=1.25 debug=true listen on\n"; got != want {
			t.Fatalf("text output = %q, want %q", got, want)
		}
	})
}

// 大小写动词 u、w、t 对所有字符串占位符生效：级别、消息、调用信息、进程信息和环境变量。
// 数值和时间保持原样，N 只用于级别，对齐在转换之后；转换本身不改变引号规则，需要去掉引号时连写 l。
func TestTemplateCaseVerbs(t *testing.T) {
	t.Setenv("XSLOG_TEST_CASE", "Hello World")
	t.Setenv("XSLOG_TEST_PORT", "8080")
	unsetTestEnv(t, "XSLOG_TEST_MISSING")
	defaultTime := timeFormat("").text(testTime())

	var pcs [1]uintptr
	runtime.Callers(1, pcs[:])
	frame, _ := runtime.CallersFrames(pcs[:]).Next()
	withSource := testRecord()
	withSource.PC = pcs[0]

	multiline := testRecord()
	multiline.Message = "first line\nsecond line"

	t.Run("JSON", func(t *testing.T) {
		for _, tc := range []struct {
			name, template, want string
			record               slog.Record
		}{
			{"级别、消息和环境变量",
				`{"level":${Level:u},"lower":${Level:w},"message":${Message:u},"env":${XSLOG_TEST_CASE:w}}`,
				`{"level":"INFO","lower":"info","message":"HELLO","env":"hello world"}`, testRecord()},
			{"N 只用于级别",
				`{"level":${Level:w3},"message":${Message:u3},"env":${XSLOG_TEST_CASE:w3}}`,
				`{"level":"inf","message":"HELLO","env":"hello world"}`, testRecord()},
			{"标题大小写：级别缩写，其他字符串首字符大写",
				`{"level":${Level:t3},"full":${Level:t0},"message":${Message:t1},"env":${XSLOG_TEST_CASE:t1}}`,
				`{"level":"Inf","full":"Info","message":"Hello","env":"Hello world"}`, testRecord()},
			{"连写的引号动词在 JSON 中被忽略",
				`{"level":${Level:lu3},"message":${Message:lw},"env":${XSLOG_TEST_CASE:ljt1}}`,
				`{"level":"INF","message":"hello","env":"Hello world"}`, testRecord()},
			{"多行消息先转换再按行展开",
				`{"message":${Message:u}}`,
				`{"message":["FIRST LINE","SECOND LINE"]}`, multiline},
			{"调用信息：字符串转换，行号保持数字",
				`{"line":${Line:u},"file":${FileName:u},"function":${Function:w}}`,
				fmt.Sprintf(`{"line":%d,"file":%q,"function":"testtemplatecaseverbs"}`,
					frame.Line, strings.ToUpper(filepath.Base(frame.File))), withSource},
			{"进程 ID 保持数字", `{"pid":${PID:u}}`, fmt.Sprintf(`{"pid":%d}`, os.Getpid()), testRecord()},
			{"不是类型动词，环境变量保持字符串", `{"port":${XSLOG_TEST_PORT:u}}`, `{"port":"8080"}`, testRecord()},
			{"时间忽略大小写动词", `{"time":${Time:w},"title":${Time:t3},"joined":${Time:lu}}`,
				fmt.Sprintf(`{"time":%q,"title":%q,"joined":%q}`, defaultTime, defaultTime, defaultTime), testRecord()},
			{"缺失仍然省略", `{"missing":${XSLOG_TEST_MISSING:u},"level":${Level:u}}`, `{"level":"INFO"}`, testRecord()},
		} {
			t.Run(tc.name, func(t *testing.T) {
				requireJSON(t, renderRecord(t, tc.record, WithTemplate(tc.template)), tc.want)
			})
		}
	})

	t.Run("文本", func(t *testing.T) {
		for _, tc := range []struct {
			name, template, want string
			record               slog.Record
		}{
			{"级别", "[${Level:u}] [${Level:w}] ${Message:l}", "[INFO] [info] hello\n", testRecord()},
			{"级别对齐在转换之后", "[${Level,6:u}][${Level,-6:w}]", "[  INFO][info  ]\n", testRecord()},
			{"N 只用于级别", "[${Level:u3}] ${Message:u3}", "[INF] \"HELLO\"\n", testRecord()},
			{"标题大小写", "[${Level:t3}] [${Level:t0}] ${Message:t1}", "[Inf] [Info] \"Hello\"\n", testRecord()},
			{"转换不改变引号规则", "${Message:u} ${Message:w} ${XSLOG_TEST_CASE:u}", "\"HELLO\" \"hello\" \"HELLO WORLD\"\n", testRecord()},
			{"只写 l 去掉引号，不转换", "${XSLOG_TEST_CASE:l}", "Hello World\n", testRecord()},
			{"连写 l 去掉引号并转换", "${Message:lu} ${XSLOG_TEST_CASE:lw} ${XSLOG_TEST_CASE:ljt1} ${Level:lu3}",
				"HELLO hello world Hello world INF\n", testRecord()},
			{"连写 lj 与 l 相同，都不加引号", "${XSLOG_TEST_CASE:lu}|${XSLOG_TEST_CASE:lju}", "HELLO WORLD|HELLO WORLD\n", testRecord()},
			{"连写 j 按 JSON 规则编码转换后的值", "${Message:ju} ${XSLOG_TEST_CASE:jw}", "\"HELLO\" \"hello world\"\n", testRecord()},
			{"时间忽略大小写动词", "${Time:w}|${Time:u3}|${Time:lt3}", defaultTime + "|" + defaultTime + "|" + defaultTime + "\n", testRecord()},
			{"缺失仍然省略", "[${XSLOG_TEST_MISSING:u}][${XSLOG_TEST_MISSING:lu}]", "[][]\n", testRecord()},
			{"不符合语法的连写不是动词", "[${Message:ul}][${Level:wl}]", "[\"hello\"][INFO]\n", testRecord()},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := renderRecord(t, tc.record, WithTemplate(tc.template)); got != tc.want {
					t.Fatalf("output = %q, want %q", got, tc.want)
				}
			})
		}
		// 连写 j 对多行消息输出 JSON 数组，空白排版不属于约定，按 JSON 语义比较。
		t.Run("连写 j 对多行消息输出数组", func(t *testing.T) {
			requireJSON(t, renderRecord(t, multiline, WithTemplate("${Message:ju}")), `["FIRST LINE","SECOND LINE"]`)
		})
		if host, err := os.Hostname(); err == nil && host != "" {
			got := renderRecord(t, testRecord(), WithTemplate("${Hostname:u}|${Hostname:w}"))
			if want := strings.ToUpper(host) + "|" + strings.ToLower(host) + "\n"; got != want {
				t.Fatalf("hostname output = %q, want %q", got, want)
			}
		}
	})

	// README 的示例：环境变量取 APP_ENV=Prod，消息为 listen on。
	t.Run("README 示例", func(t *testing.T) {
		t.Setenv("XSLOG_TEST_APP_ENV", "Prod")
		r := slog.NewRecord(testTime(), slog.LevelInfo, "listen on", 0)
		got := renderRecord(t, r, WithTemplate(`{"level":${Level:w},"env":${XSLOG_TEST_APP_ENV:w},"message":${Message}}`))
		if want := `{"level":"info","env":"prod","message":"listen on"}` + "\n"; got != want {
			t.Fatalf("JSON output = %q, want %q", got, want)
		}
		got = renderRecord(t, r, WithTemplate("[${Level:u3}] [${XSLOG_TEST_APP_ENV:lu}] ${Message:l}"))
		if want := "[INF] [PROD] listen on\n"; got != want {
			t.Fatalf("text output = %q, want %q", got, want)
		}
		// 没有连写 l 时，文本中的环境变量字符串带引号。
		got = renderRecord(t, r, WithTemplate("[${XSLOG_TEST_APP_ENV:u}]"))
		if want := "[\"PROD\"]\n"; got != want {
			t.Fatalf("quoted text output = %q, want %q", got, want)
		}
	})
}

// Level 缩写表对四个级别逐一渲染，与 README 中的表格保持一致。
func TestTemplateLevelMonikers(t *testing.T) {
	for _, tc := range []struct {
		level slog.Level
		want  string
	}{
		{slog.LevelDebug, "D/DE/DBG/DBUG/DEBUG|d/de/dbg/dbug/debug|D/De/Dbg/Dbug/Debug"},
		{slog.LevelInfo, "I/IN/INF/INFO/INFO|i/in/inf/info/info|I/In/Inf/Info/Info"},
		{slog.LevelWarn, "W/WN/WRN/WARN/WARN|w/wn/wrn/warn/warn|W/Wn/Wrn/Warn/Warn"},
		{slog.LevelError, "E/ER/ERR/EROR/ERROR|e/er/err/eror/error|E/Er/Err/Eror/Error"},
		// 自定义级别按宽度截断。
		{slog.LevelInfo + 2, "I/IN/INF/INFO/INFO+|i/in/inf/info/info+|I/In/Inf/Info/Info+"},
	} {
		t.Run(tc.level.String(), func(t *testing.T) {
			r := testRecord()
			r.Level = tc.level
			var parts []string
			for _, mode := range []string{"u", "w", "t"} {
				var widths []string
				for width := 1; width <= 5; width++ {
					widths = append(widths, fmt.Sprintf("${Level:%s%d}", mode, width))
				}
				parts = append(parts, strings.Join(widths, "/"))
			}
			got := renderRecord(t, r, WithTemplate(strings.Join(parts, "|")))
			if got != tc.want+"\n" {
				t.Fatalf("output = %q, want %q", got, tc.want)
			}
		})
	}
}
