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
		{"${when:unix}/${when:unixMilli}/${when:unixMicro}/${when:unixNano}", "1704164645/1704164645678/1704164645678901/1704164645678901234"},
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
