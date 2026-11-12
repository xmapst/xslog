package xslog

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestInjectorSyntaxAndFormatting(t *testing.T) {
	values := map[string]any{"name": "世界", "item.id": 7, "item-name": "ok", "number": 42, "float": 1.25, "when": testTime(), "nil": nil}
	for _, tc := range []struct{ template, want string }{
		{"%name%/${name}", "世界/世界"},
		{"${item.id}/%item-name%", "7/ok"},
		{"$name $(name) {name} {{name}}", "$name $(name) {name} {{name}}"},
		{"${name,4}/${name,-4}", "  世界/世界  "},
		{"${name,1}", "世界"},
		{"${number:04d}/${float:.2f}/${name:q}", "0042/1.25/\"世界\""},
		{"${when:2006-01-02 15:04:05}", "2024-01-02 03:04:05"},
		{"${when:15:04:05.000}", "03:04:05.678"},
		// 不适用于时间的动词被忽略，使用默认布局，不会原样输出。
		{"${when:d}/${when:f}/${when:u}/${when:lj}", "2024-01-02T03:04:05.678Z/2024-01-02T03:04:05.678Z/2024-01-02T03:04:05.678Z/2024-01-02T03:04:05.678Z"},
		{"${nil}", "<nil>"},
		{"${name,invalid}/${name,4097}/${name,-4097}", "${name,invalid}/${name,4097}/${name,-4097}"},
		{"${}/${9name}/%9name%/unfinished ${name", "${}/${9name}/%9name%/unfinished ${name"},
		{"plain text", "plain text"},
		{"", ""},
	} {
		t.Run(tc.template, func(t *testing.T) {
			var injector Injector
			injector.compile(tc.template)
			if got := injector.Inject(values); got != tc.want {
				t.Fatalf("Inject = %q, want %q", got, tc.want)
			}
		})
	}
	var injector Injector
	injector.compile("${name,4096}")
	if got := injector.Inject(values); len([]rune(got)) != 4096 || !strings.HasSuffix(got, "世界") {
		t.Fatalf("maximum alignment produced %d runes", len([]rune(got)))
	}
}

func TestInjectorEnvironmentAndReuse(t *testing.T) {
	t.Setenv("XSLOG_TEST_INJECT", "environment")
	unsetTestEnv(t, "XSLOG_TEST_INJECT_ABSENT")
	var injector Injector
	injector.compile("${XSLOG_TEST_INJECT}|%XSLOG_TEST_INJECT_ABSENT%")
	if got := injector.Inject(nil); got != "environment|%XSLOG_TEST_INJECT_ABSENT%" {
		t.Fatal(got)
	}
	if got := injector.Inject(map[string]any{"XSLOG_TEST_INJECT": "map"}); got != "map|%XSLOG_TEST_INJECT_ABSENT%" {
		t.Fatal(got)
	}
	if got := injector.Inject(map[string]any{"XSLOG_TEST_INJECT": ""}); got != "|%XSLOG_TEST_INJECT_ABSENT%" {
		t.Fatal(got)
	}
	t.Setenv("XSLOG_TEST_INJECT", "changed")
	if got := injector.Inject(nil); !strings.HasPrefix(got, "changed|") {
		t.Fatal(got)
	}
	injector.compile("${value}")
	if got := injector.Inject(map[string]any{"value": "%XSLOG_TEST_INJECT% ${value}"}); got != "%XSLOG_TEST_INJECT% ${value}" {
		t.Fatalf("replacement was recursively expanded: %q", got)
	}
	injector.compile("new literal")
	if got := injector.Inject(nil); got != "new literal" {
		t.Fatalf("old compilation survived reuse: %q", got)
	}
}

func TestInjectorEnvironmentVerbConversion(t *testing.T) {
	t.Setenv("XSLOG_TEST_INT", "8080")
	t.Setenv("XSLOG_TEST_NEG", "-12")
	t.Setenv("XSLOG_TEST_CODE", "65")
	t.Setenv("XSLOG_TEST_FLOAT", "1.25")
	t.Setenv("XSLOG_TEST_UINT", "18446744073709551615")
	t.Setenv("XSLOG_TEST_HUGE", "99999999999999999999")
	t.Setenv("XSLOG_TEST_TRUE", "true")
	t.Setenv("XSLOG_TEST_ONE", "1")
	t.Setenv("XSLOG_TEST_ZERO", "0")
	t.Setenv("XSLOG_TEST_TEXT", "abc")
	for _, tc := range []struct{ template, want string }{
		// 整数动词：标志、宽度和动词后的字面量不影响识别，超出 int64 的无符号数仍可解析。
		{"${XSLOG_TEST_INT:d}", "8080"},
		{"${XSLOG_TEST_INT:06d}", "008080"},
		{"${XSLOG_TEST_INT:+d}", "+8080"},
		{"${XSLOG_TEST_INT:d items}", "8080 items"},
		{"${XSLOG_TEST_INT,8:d}", "    8080"},
		{"${XSLOG_TEST_INT,-8:d}|", "8080    |"},
		{"${XSLOG_TEST_NEG:d}", "-12"},
		{"${XSLOG_TEST_UINT:d}", "18446744073709551615"},
		{"${XSLOG_TEST_CODE:b}/${XSLOG_TEST_CODE:o}/${XSLOG_TEST_CODE:O}", "1000001/101/0o101"},
		{"${XSLOG_TEST_CODE:c}/${XSLOG_TEST_CODE:U}", "A/U+0041"},
		// 浮点动词：整数文本同样可解析。
		{"${XSLOG_TEST_FLOAT:.2f}/${XSLOG_TEST_FLOAT:8.3f}", "1.25/   1.250"},
		{"${XSLOG_TEST_FLOAT:e}/${XSLOG_TEST_FLOAT:g}", "1.250000e+00/1.25"},
		{"${XSLOG_TEST_INT:.1f}", "8080.0"},
		// 布尔动词。
		{"${XSLOG_TEST_TRUE:t}/${XSLOG_TEST_ONE:t}/${XSLOG_TEST_ZERO:t}", "true/true/false"},
		// 字符串本身支持的动词保持字符串，x 仍是字节的十六进制。
		{"${XSLOG_TEST_INT:s}/${XSLOG_TEST_INT:v}/${XSLOG_TEST_INT:q}", `8080/8080/"8080"`},
		{"${XSLOG_TEST_INT:x}", "38303830"},
		// 无法解析时保持字符串，由 fmt 报告错误并带出原值。
		{"${XSLOG_TEST_TEXT:d}", "%!d(string=abc)"},
		{"${XSLOG_TEST_FLOAT:d}", "%!d(string=1.25)"},
		{"${XSLOG_TEST_HUGE:d}", "%!d(string=99999999999999999999)"},
		{"${XSLOG_TEST_TEXT:f}/${XSLOG_TEST_TEXT:t}", "%!f(string=abc)/%!t(string=abc)"},
	} {
		t.Run(tc.template, func(t *testing.T) {
			var injector Injector
			injector.compile(tc.template)
			if got := injector.Inject(nil); got != tc.want {
				t.Fatalf("Inject = %q, want %q", got, tc.want)
			}
		})
	}
	// envs 中的值由调用方决定类型：字符串不会被转换，类型化的值照常格式化。
	var injector Injector
	injector.compile("${XSLOG_TEST_INT:d}")
	if got := injector.Inject(map[string]any{"XSLOG_TEST_INT": "7"}); got != "%!d(string=7)" {
		t.Fatalf("envs string was converted: %q", got)
	}
	if got := injector.Inject(map[string]any{"XSLOG_TEST_INT": 7}); got != "7" {
		t.Fatalf("envs int = %q", got)
	}
}

// u、w 转换字符串的大小写，对齐在转换之后；N 被忽略，其他类型按默认形式输出，
// 不是大小写动词的格式仍交给 fmt。
func TestInjectorCaseVerbs(t *testing.T) {
	t.Setenv("XSLOG_TEST_CASE", "Hello World")
	t.Setenv("XSLOG_TEST_INT", "8080")
	defaultTime := timeFormat("").text(testTime())
	envs := map[string]any{"S": "MiXed", "N": 7, "B": true, "T": testTime(), "Z": nil}
	for _, tc := range []struct{ template, want string }{
		{"${XSLOG_TEST_CASE:u}|${XSLOG_TEST_CASE:w}", "HELLO WORLD|hello world"},
		{"${S:u}|${S:w}|${S:u3}|${S:w3}", "MIXED|mixed|MIXED|mixed"},
		{"${S,8:u}|${S,-8:w}|", "   MIXED|mixed   |"},
		// 标题大小写必须带宽度，单独的 t 仍是 fmt 的布尔动词；连写中的引号动词被忽略。
		{"${S:t3}|${XSLOG_TEST_CASE:t0}|${S:lu}|${S:ljw3}|${S:ju}", "Mixed|Hello world|MIXED|mixed|MIXED"},
		{"${B:t}|${N:t3}|${N:lu}", "true|7|7"},
		{"${N:u}|${B:w}|${Z:u}", "7|true|<nil>"},
		{"${T:u}|${T:w3}", defaultTime + "|" + defaultTime},
		// 数字不是类型动词，环境变量仍是字符串。
		{"${XSLOG_TEST_INT:u}", "8080"},
		{"${S:uabc}|${S:U}", "%!u(string=MiXed)abc|%!U(string=MiXed)"},
	} {
		t.Run(tc.template, func(t *testing.T) {
			var injector Injector
			injector.compile(tc.template)
			if got := injector.Inject(envs); got != tc.want {
				t.Fatalf("Inject = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInjectorAppendCallback(t *testing.T) {
	var injector Injector
	injector.compile("${known,6:q}|%unknown%")
	got := injector.AppendFunc([]byte("prefix:"), func(dst []byte, name, format string) ([]byte, bool) {
		if name == "known" {
			if format != "q" {
				t.Errorf("format = %q", format)
			}
			return append(dst, "世界"...), true
		}
		return dst, false
	})
	if string(got) != "prefix:    世界|%unknown%" {
		t.Fatalf("AppendFunc = %q", got)
	}
	if got := string(injector.AppendFunc(nil, nil)); got != "${known,6:q}|%unknown%" {
		t.Fatal(got)
	}
}

func TestInjectorConcurrentReuse(t *testing.T) {
	var injector Injector
	injector.compile("[${value,4}] %name%")
	var wg sync.WaitGroup
	errs := make(chan string, 20)
	for id := range 20 {
		wg.Go(func() {
			values := map[string]any{"value": id, "name": "shared"}
			want := fmt.Sprintf("[%4d] shared", id)
			for range 20 {
				if got := injector.Inject(values); got != want {
					errs <- got
					return
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	for got := range errs {
		t.Fatalf("concurrent Inject = %q", got)
	}
}

func FuzzInjectorPreservesUnresolvedText(f *testing.F) {
	for _, seed := range []string{"", "plain", "%A% ${A,4:q}", "${A,4097} ${A,-4}", "{A} {{A}} $A $(A)", "\xff${A}\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		var injector Injector
		injector.compile(input)
		if got := string(injector.AppendFunc([]byte("prefix:"), nil)); got != "prefix:"+input {
			t.Fatalf("lost template text: %q -> %q", input, got)
		}
	})
}
