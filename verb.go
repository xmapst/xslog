package xslog

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// quoteMode 是引号动词。
type quoteMode uint8

const (
	// quoteDefault 表示没有引号动词。
	quoteDefault quoteMode = iota
	// quotePlain 对应 l 或 lj：文本模板中字符串不加引号。
	quotePlain
	// quoteJSON 对应 j：按 JSON 规则编码。
	quoteJSON
)

// caseMode 是大小写动词的转换方式。
type caseMode uint8

const (
	// caseNone 表示没有大小写动词，文本保持原样。
	caseNone caseMode = iota
	// caseUpper 对应 u：转为大写。
	caseUpper
	// caseLower 对应 w：转为小写。
	caseLower
	// caseTitle 对应 t：首个字符大写，其余小写。
	caseTitle
)

// verb 是占位符的格式，即 ${Name[,alignment][:format]} 中冒号之后的文本，解析后同时是动词管理器：
// 识别、解释和应用动词的所有操作都是它的方法，其他文件不直接比较或解析格式文本。
// parseVerb 解析格式，每个占位符每次渲染解析一次。
//
// 动词是各占位符共用的格式。引号动词和大小写动词可以连写，引号动词在前，两部分都可以省略，
// 如 lu、lw3、ju、ljt3：
//
//	l lj           文本模板中字符串不加引号                     字符串
//	j              按 JSON 规则编码                             所有占位符
//	u w            字符串转大写、小写                           字符串
//	tN             标题大小写：首字符大写，其余小写             字符串，必须带宽度
//	uN wN tN       Level 按宽度 N 取缩写，u3 得到 INF           Level，其他字符串忽略 N
//	d b o O c U    环境变量文本解析为整数                       环境变量
//	e E f F g G    环境变量文本解析为浮点数                     环境变量
//	t              环境变量文本解析为布尔值                     环境变量
//
// 单独的 t 是布尔类型动词，所以标题大小写必须带宽度；t0 表示不限宽度。
// 动词只在适用的占位符上生效，不适用时忽略：大小写动词只转换字符串，数值、布尔值和时间保持原样。
// 时间布局是 Time 专用的，不属于动词，由 timeFormat 解释，verb 只决定何时把格式交给它：
// 格式恰为引号动词与大小写动词的合法连写，或恰为一个类型动词时，Time 忽略它，使用默认布局。
// Unix 时间戳不是格式，由 TimeUnix、TimeUnixMilli、TimeUnixMicro、TimeUnixNano 这些独立的数值占位符提供，
// 与 Line、PID 一样不受任何动词影响。
// 在 Injector.Inject 中，其余格式按 fmt 格式处理。README 的「格式动词」一章与此一致。
//
// verb 按值传递。字段不超过 4 个、总大小不超过 4 个字长，编译器才会把它放进寄存器，
// 否则每次传递、返回和赋值都要经过内存拷贝，新增字段前先确认这一点。
type verb struct {
	// text 是格式原文，时间布局和类型动词按它解析。
	text string
	// quote 是引号动词。
	quote quoteMode
	// mode 是大小写动词。
	mode caseMode
	// width 是大小写动词之后的宽度 N，没有时为 0，只用于 Level。
	width int
}

// parseVerb 解析格式。引号动词（l、lj、j）和大小写动词（u、w 加可选的 N，或 t 加 N）按这个顺序连写，
// 不符合这一语法的文本不是呈现动词，只保留原文，供时间布局和类型动词解释。
//
// 每个占位符每次渲染都会解析，所以先排除常见的情况：没有格式，以及以其他字符开头的时间布局
// 和类型动词，都不必进入完整的语法检查。
func parseVerb(format string) verb {
	v := verb{text: format}
	if format != "" {
		switch format[0] {
		case 'l', 'j', 'u', 'w', 't':
			v.parseGrammar()
		}
	}
	return v
}

// parseGrammar 按呈现动词的语法解析 v.text，只在它以 l、j、u、w、t 开头时调用；
// 不符合语法时清除已解析的部分。
func (v *verb) parseGrammar() {
	rest := v.text
	if after, ok := strings.CutPrefix(rest, "lj"); ok {
		v.quote, rest = quotePlain, after
	} else if after, ok := strings.CutPrefix(rest, "l"); ok {
		v.quote, rest = quotePlain, after
	} else if after, ok := strings.CutPrefix(rest, "j"); ok {
		v.quote, rest = quoteJSON, after
	}
	if rest == "" {
		return
	}
	switch rest[0] {
	case 'u':
		v.mode = caseUpper
	case 'w':
		v.mode = caseLower
	case 't':
		v.mode = caseTitle
	default:
		*v = verb{text: v.text}
		return
	}
	digits := rest[1:]
	for i := range len(digits) {
		if digits[i] < '0' || digits[i] > '9' {
			*v = verb{text: v.text}
			return
		}
	}
	switch {
	case digits != "":
		// 数字超出 int 时 Atoi 返回最大值，等同于没有宽度限制。
		v.width, _ = strconv.Atoi(digits)
	case v.mode == caseTitle:
		// 单独的 t 是布尔类型动词。
		*v = verb{text: v.text}
	}
}

// isPlain 报告文本模板中字符串是否不加引号：l 或 lj。
func (v verb) isPlain() bool { return v.quote == quotePlain }

// isJSON 报告是否按 JSON 规则编码：j。
func (v verb) isJSON() bool { return v.quote == quoteJSON }

// isPresentation 报告格式是否为引号动词、大小写动词或它们的连写。
func (v verb) isPresentation() bool { return v.quote != quoteDefault || v.mode != caseNone }

// typeKind 是类型动词把环境变量文本解析成的类型。
type typeKind uint8

const (
	// typeNone 表示格式中没有类型动词。
	typeNone typeKind = iota
	// typeInt 对应 d b o O c U：整数。
	typeInt
	// typeFloat 对应 e E f F g G：浮点数。
	typeFloat
	// typeBool 对应 t：布尔值。
	typeBool
)

// typeVerb 找出格式中的类型动词，哪些字母是类型动词只在这里定义：
// 跳过 fmt 的标志、宽度、精度和参数序号，取第一个字符，不是 d b o O c U e E f F g G t 之一时返回 typeNone。
// 引号动词、大小写动词（包括带宽度的 t）也不是类型动词。
// exact 报告类型动词是否是格式的最后一个字符，即整个格式只有一个动词，没有跟在后面的字面量。
func (v verb) typeVerb() (kind typeKind, exact bool) {
	if v.isPresentation() {
		return typeNone, false
	}
	i := 0
	for i < len(v.text) && v.isVerbPrefix(v.text[i]) {
		i++
	}
	if i == len(v.text) {
		return typeNone, false
	}
	switch v.text[i] {
	case 'd', 'b', 'o', 'O', 'c', 'U':
		kind = typeInt
	case 'e', 'E', 'f', 'F', 'g', 'G':
		kind = typeFloat
	case 't':
		kind = typeBool
	default:
		return typeNone, false
	}
	return kind, i == len(v.text)-1
}

// isTypeVerb 报告格式是否恰好是一个类型动词，可以有 fmt 的标志、宽度、精度和参数序号作前缀，
// 如 d、06d、.2f、[1]t。要求整个格式只有一个动词，Go 布局里的普通文字（如 Feb、15h、末尾的 Z）
// 因此不会被误认为动词。类型动词只对环境变量有意义，其他值忽略它。
func (v verb) isTypeVerb() bool {
	kind, exact := v.typeVerb()
	return kind != typeNone && exact
}

// timeFormat 返回时间使用的格式。没有格式，以及不适用于时间的动词（引号动词、大小写动词、类型动词）
// 都被忽略，使用默认布局（timeFormat 的零值），不会当作布局原样输出；
// 其余都是 Go 时间布局，不属于布局的文字原样输出。
func (v verb) timeFormat() timeFormat {
	if v.isPresentation() || v.isTypeVerb() {
		return ""
	}
	return timeFormat(v.text)
}

// applyCase 按大小写动词转换字符串，没有大小写动词时返回原文。宽度 N 只用于 Level，这里忽略。
func (v verb) applyCase(s string) string {
	if v.mode == caseNone {
		return s
	}
	return v.fold(s)
}

// level 按大小写动词格式化级别名称：在大小写之外，N 是 Level 专用的宽度。
// DEBUG、INFO、WARN、ERROR 直接取 levelTable 中预先算好的缩写，不转换也不分配内存；
// 其他名称（如 INFO+2）比宽度长时截断，再转换大小写。没有大小写动词时返回原文。
func (v verb) level(text string) string {
	if v.mode == caseNone {
		return text
	}
	if short, ok := levelMoniker(text, v.mode, v.width); ok {
		return short
	}
	if v.width > 0 && v.width < len(text) {
		text = text[:v.width]
	}
	return v.fold(text)
}

// fold 按大小写动词转换 s。
func (v verb) fold(s string) string {
	switch v.mode {
	case caseUpper:
		return strings.ToUpper(s)
	case caseLower:
		return strings.ToLower(s)
	case caseTitle:
		return titleCase(s)
	}
	return s
}

// titleCase 把首个字符转为大写，其余转为小写；文本已经是这种形式时原样返回，不分配内存。
func titleCase(s string) string {
	if s == "" {
		return s
	}
	_, size := utf8.DecodeRuneInString(s)
	head, tail := strings.ToUpper(s[:size]), strings.ToLower(s[size:])
	if head == s[:size] && tail == s[size:] {
		return s
	}
	return head + tail
}

// levelMonikers 是一个级别按宽度取得的缩写。缩写与 Serilog 的 u1 至 u4 一致：每个宽度都有容易辨认的
// 写法，而不是简单截断，例如 WARN 在宽度 2 是 WN，DEBUG 在宽度 4 是 DBUG。
// 大小写三种形式在初始化时一次算好，渲染时直接取用，不再转换，也不分配内存。
type levelMonikers struct {
	name string
	// forms 按大小写动词索引，caseNone 对应的一项不用。每一项的下标 0 对应宽度 1，
	// 宽度超过长度时使用最后一个元素，即完整名称。
	forms [caseTitle + 1][]string
}

// newLevelMonikers 由大写缩写（从宽度 1 到完整名称）生成小写和标题大小写两种形式。
func newLevelMonikers(name string, upper ...string) levelMonikers {
	m := levelMonikers{name: name}
	m.forms[caseUpper] = upper
	m.forms[caseLower] = make([]string, len(upper))
	m.forms[caseTitle] = make([]string, len(upper))
	for i, short := range upper {
		m.forms[caseLower][i] = strings.ToLower(short)
		m.forms[caseTitle][i] = titleCase(short)
	}
	return m
}

// levelTable 是 DEBUG、INFO、WARN、ERROR 的缩写表，初始化之后只读。
var levelTable = [...]levelMonikers{
	newLevelMonikers("DEBUG", "D", "DE", "DBG", "DBUG", "DEBUG"),
	newLevelMonikers("INFO", "I", "IN", "INF", "INFO"),
	newLevelMonikers("WARN", "W", "WN", "WRN", "WARN"),
	newLevelMonikers("ERROR", "E", "ER", "ERR", "EROR", "ERROR"),
}

// levelMoniker 返回级别 name 在大小写 mode 和宽度 width 下的缩写。宽度小于 1 表示不限宽度，
// 返回完整名称；不在表中的名称（如 INFO+2）和没有大小写动词时，ok 为 false。
func levelMoniker(name string, mode caseMode, width int) (short string, ok bool) {
	for i := range levelTable {
		row := &levelTable[i]
		if row.name != name {
			continue
		}
		forms := row.forms[mode]
		if len(forms) == 0 {
			return "", false
		}
		if width < 1 {
			return forms[len(forms)-1], true
		}
		return forms[min(width, len(forms))-1], true
	}
	return "", false
}

// lookupEnv 读取系统环境变量，并按类型动词转换，变量不存在时返回 false。
func (v verb) lookupEnv(name string) (slog.Value, bool) {
	text, exists := os.LookupEnv(name)
	if !exists {
		return slog.Value{}, false
	}
	return v.envValue(text), true
}

// envValue 按类型动词把环境变量文本转换为带类型的值：
//
//	d b o O c U    整数，按十进制解析 int64，超出范围时再按 uint64
//	e E f F g G    浮点数
//	t              布尔值
//
// 动词之前的标志、宽度、精度和参数序号会被跳过。没有格式、动词不在上表，或文本无法解析时
// 返回字符串，不丢失原值；s、v、q、x、X、引号动词、大小写动词（包括 t3 这样带宽度的 t）
// 和时间布局都属于这种情况。
func (v verb) envValue(text string) slog.Value {
	kind, _ := v.typeVerb()
	switch kind {
	case typeInt:
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return slog.Int64Value(n)
		}
		if n, err := strconv.ParseUint(text, 10, 64); err == nil {
			return slog.Uint64Value(n)
		}
	case typeFloat:
		if f, err := strconv.ParseFloat(text, 64); err == nil {
			return slog.Float64Value(f)
		}
	case typeBool:
		if b, err := strconv.ParseBool(text); err == nil {
			return slog.BoolValue(b)
		}
	}
	return slog.StringValue(text)
}

// appendAny 供 [Injector.Inject] 格式化任意值：没有格式时按默认形式输出，时间使用 timeFormat，
// 大小写动词转换字符串，其余把格式当作省略百分号的 fmt 格式。
// 大小写动词不适用于字符串之外的值，按默认形式输出；连写中的引号动词对纯文本没有意义，被忽略。
func (v verb) appendAny(dst []byte, value any) []byte {
	if v.text == "" {
		return fmt.Append(dst, value)
	}
	if timestamp, ok := value.(time.Time); ok {
		return v.timeFormat().append(dst, timestamp)
	}
	if v.mode != caseNone {
		if text, ok := value.(string); ok {
			return append(dst, v.fold(text)...)
		}
		return fmt.Append(dst, value)
	}
	return fmt.Appendf(dst, "%"+v.text, value)
}

// isVerbPrefix 判断 c 是否可以出现在 fmt 动词之前：标志 + - # 和空格，宽度与精度的数字和 .，
// 以及参数序号的 [ ]。动词都是 ASCII 字母，逐字节扫描即可，不必构造字符集或解码 rune。
func (v verb) isVerbPrefix(c byte) bool {
	switch c {
	case '+', '-', '#', ' ', '.', '[', ']':
		return true
	}
	return c >= '0' && c <= '9'
}
