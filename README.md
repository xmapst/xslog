# xslog

基于标准库 `log/slog`，提供一个 `Handler`。模板决定文本或 JSON 编码、元数据布局和时间格式，
业务字段自动追加为文本键值对或 JSON 对象成员。

```go
slog.SetDefault(slog.New(xslog.NewHandler()))

slog.Info("listen on", "network", "tcp", "address", "[::]:8080")
slog.Error("march frame panic", "module", "wmap", "panic", err, "stack", stack)
slog.With("module", "battle").WithGroup("frame").
    Debug("tick", "seq", 1042, "cost", 3.5, "ok", true)
```

默认文本模板的输出效果如下，包含普通字段、多行堆栈和点分组字段；时间与调用位置仅作示例：

```text
2026-08-20T11:48:55.756+08:00 INFO [gate/gate.go:100] message="listen on" network="tcp" address="[::]:8080"
2026-08-20T11:48:55.756+08:00 ERROR [wmap/ctl_march.go:562] message="march frame panic" module="wmap" panic="runtime error: invalid memory address"
    stack │ goroutine 42 [running]:
          │ runtime/debug.Stack()
          │   /usr/local/go/src/runtime/debug/stack.go:26 +0x5e
          │ main.frame()
          │   /src/wmap/ctl_march.go:562 +0x1a4
2026-08-20T11:48:55.756+08:00 DEBUG [battle/frame.go:88] message="tick" module="battle" frame.seq=1042 frame.cost=3.5 frame.ok=true
```

需要 Go 1.27+；复合值使用 `encoding/json/v2`，文件轮转依赖
[timberjack](https://github.com/DeRuina/timberjack)。

```sh
go get github.com/xmapst/xslog
```

## 输出模板

`NewHandler` 和 `Setup` 共用 `WithTemplate(string)`。模板在装配时解析一次；
写日志时复用编译结果，再取值、编码并追加到缓冲区。

配置时先用带类型的示例值按 JSON 规则展开模板，再交给标准 JSON 校验。
完整结果是合法 JSON 对象时使用 JSON 编码，其余情况使用文本编码。
格式确定后，不会因为实际记录或环境变量的内容变化而切换。

文本布局：

```go
log := slog.New(xslog.NewHandler(xslog.WithTemplate(
    "${Time:15:04:05.000} [${Level:u3}] ${Message:l}",
)))
log.Info("listen on", "network", "tcp", "port", 8080)
// 11:48:55.756 [INF] listen on network="tcp" port=8080
```

JSON 对象布局自动使用 JSON 编码。元数据的键名、顺序和嵌套结构直接写在模板中，
占位符是完整 JSON 值，外面不加引号：

```go
log := slog.New(xslog.NewHandler(xslog.WithTemplate(
    `{"timestamp":${Time:unixMilli},"severity":${Level:u3},` +
        `"event":{"text":${Message}}}`,
)))
log.Info("listen on", "network", "tcp", "port", 8080)
// {"timestamp":1787197735756,"severity":"INF","event":{"text":"listen on"},"network":"tcp","port":8080}
```

字符串里的引号和控制字符由编码器转义，数字与布尔保留原来的类型。
JSON 占位符通常直接表示完整值，不需要外加引号。例如 `"text":"${Message}"` 会因为
示例展开后产生重复引号而使用文本编码。`{${Time},"Level":${Level}}` 缺少成员名和
冒号，也作为普通文本输出，不会导致配置失败。
业务字段自动加入记录：文本追加 `key=value`，JSON 追加为对象成员。
字段通过 `With`、`WithGroup` 和日志调用的属性参数传入，无需写进模板。

| 占位符          | 内容                                                 |
|-----------------|------------------------------------------------------|
| `${Time}`       | 记录时间，省略格式时使用 RFC3339 毫秒                |
| `${Level}`      | 完整级别名；`:u3` 为 `DBG/INF/WRN/ERR`，`:w3` 为小写 |
| `${Source}`     | 调用位置，不带方括号                                 |
| `${File}`       | 调用文件路径，保留运行时提供的路径                   |
| `${FileName}`   | 调用文件名，如 `handler.go`                          |
| `${Line}`       | 调用行号，JSON 中为数字                              |
| `${Function}`   | 完整调用函数名，包含包路径和接收者信息               |
| `${Package}`    | 调用方的完整包路径                                   |
| `${PID}`        | 当前进程 ID，JSON 中为数字                           |
| `${Hostname}`   | 当前主机名                                           |
| `${Executable}` | 当前可执行文件路径                                   |
| `${Message}`    | 消息；文本中默认带引号，`:l` 或 `:lj` 去掉引号       |
| `${NewLine}`    | 显式插入一个 LF，用于模板布局换行                    |

内置名称不区分大小写。占位符不查找业务字段，业务字段的名称也不影响内置元数据。
调用信息从记录的 PC 解析并共享缓存；`Source` 保持 `上级目录/文件名:行号` 的缩短格式，
`File` 保留完整的运行时路径。进程信息在创建 Handler 时读取一次，派生 Logger 复用这份快照。
调用信息缺失或进程信息读取失败时，照常回退环境变量。这些字段的回退值按字符串规则编码。
调用和进程信息中的字符串元数据在文本中不加引号；JSON 中单行值编码为字符串，多行值按行展开为字符串数组。

可以单独组合文件、行号和函数名，也可以在 JSON 中保留数值类型：

```go
xslog.WithTemplate("${FileName}:${Line} ${Function} ${Message:l}")
xslog.WithTemplate(`{"file":${File},"line":${Line},"function":${Function},"pid":${PID},"message":${Message}}`)
```

模板只支持 `%VAR%` 和 `${VAR}` 两种占位符，例如 `%Level%`、`${SERVICE_NAME:l}`。
名称以 ASCII 字母或下划线开头，后续可包含字母、数字、下划线、点和连字符。
格式和对齐使用 `${Name[,alignment][:format]}`，例如 `${Level,6:u3}` 右对齐到六个字符，
`${SERVICE_NAME,-12:l}` 左对齐。宽度按 Unicode 码点计算，上限 4096。
文本模板中的 `:j` 按 JSON 规则编码对应值，例如 `${Message:j}`。

模板保留字面量中的空格和换行。Handler 在整条记录末尾自动补齐 LF，已有换行时不重复追加。
需要手工换行时，使用 `${NewLine}` 或 `%NewLine%`；连续使用可插入空行，末尾使用不会多补换行。
它是布局控制符，在 JSON 模板中应放在字符串引号外，作为成员之间或对象前后的空白：

```go
xslog.WithTemplate("${Message:l}${NewLine}details")
xslog.WithTemplate(`{"message":${Message},${NewLine}"pid":${PID}}${NewLine}`)
```

占位符先查日志内置元数据，缺失时按占位符原名回退系统环境变量。环境变量在每次
渲染时读取，空值也算存在；读取后使用相同的文本或 JSON 编码，并支持对齐及 `:l` 格式。
两处都没有的值不输出：文本省略占位值，JSON 省略对应的成员或数组元素。
JSON 中业务属性的 `nil` 保留为 `null`，空环境变量保留为空字符串。动态值不会再次当作模板解析。

模板复用 `Injector` 的解析、替换和对齐实现。`$VAR`、`$(VAR)`、
`{VAR}` 和 `{{VAR}}` 都按普通文本保留；JSON 对象的花括号不参与占位符解析。

未设置模板或传入 `WithTemplate("")` 时使用 `DefaultTemplate`，与自定义模板共用渲染流程：

```text
${Time} ${Level} [${Source}] message=${Message}
```

位置缺失时，模板里的 `[]` 仍会保留。多次设置模板时，后传入的选项生效。

## 时间格式

时间格式写在对应占位符里，使用 Go 的参考时间布局：

```go
xslog.WithTemplate(
    "${Time:2006-01-02 15:04:05.000} [${Level}] ${Message:l}",
)
```

JSON 中也可以直接选择记录时间的格式，业务时间字段照常通过属性参数传入：

```go
log := slog.New(xslog.NewHandler(xslog.WithTemplate(
    `{"at":${Time:unixMilli},"message":${Message}}`,
)))
log.Info("scheduled", slog.Time("deadline", deadline))
```

`unix` / `unixMilli` / `unixMicro` / `unixNano` 分别输出秒、毫秒、微秒、纳秒时间戳，
格式名区分大小写，JSON 中是数字。业务属性中的时间使用默认的 RFC3339 毫秒格式，
`slog.Any` 结构体内部的时间遵循该值自身的 JSON 序列化规则。

省略格式时，时间占位符使用 RFC3339 毫秒格式。时间格式只影响对应占位符，
不会改变另一个占位符选择的格式。

## 附加字段

恒定标记可以直接用标准库的 `With` 添加，自动作为业务字段输出：

```go
log := slog.New(xslog.NewHandler(xslog.WithTemplate(
    "[${Level:u3}] ${Message:l}",
))).With("app", "gate")
```

动态字段通过日志调用的属性参数传入。`With` 字段在添加时确定分组，之后调用
`WithGroup` 不会改变它们原来的归属；从同一个 Logger 派生出的子 Logger 互不覆盖字段。

文本输出中，自动追加的业务字段遵循以下四条规则：

1. **普通单行字符串恒定带引号，数字与布尔不带。** 不根据内容是否含空格来决定是否加引号，
   因此 `count="123"` 与 `count=123` 始终能区分字符串和数字。多行文本和结构化 JSON
   分别按下面的第 2、4 条处理。
2. **实际包含多行内容的值才另起竖线块。** 去掉尾部 CR/LF 后，内容中仍包含换行时才使用
   多行块，单独的末尾换行不会触发。多个块的键名左对齐，列宽按本条记录中转义后多行字段键名的最大字节长度计算。
3. **分组展开成点分键名。** 例如 `frame.seq=7`，不使用嵌套缩进，缩进留给多行字段块。
4. **完整的 JSON 对象或数组字符串按结构化值写出。** 字符串以 `{` 或 `[` 开头且完整合法时，
   先紧凑化，再直接嵌入，不额外包裹字符串引号或将内部引号再次转义；不可打印字符仍会转义。
   `"123"`、`"true"`、`"null"` 这类业务字符串仍按普通字符串输出。

这些规则约束业务字段。模板字面量保留原样；`${Message}` 默认带引号，可用 `:l` 或 `:lj`
取消；`Source`、`File`、`FileName`、`Function`、`Package`、`Hostname`、`Executable`
等字符串元数据在文本中直接输出。

JSON 输出中，分组保留为嵌套对象。**直接传入的多行字符串统一展开为字符串数组，一行一个元素**，
包括字符串业务字段、`${Message}`、其他字符串元数据及环境变量回退值。
去掉尾部 CR/LF 后仍有多行内容时才展开；行内空行保留为空字符串，尾部空行不追加数组元素。
只有末尾换行的单行文本仍保留为字符串。业务字段和普通环境变量中的完整 JSON 对象或数组文本
优先作为结构化值嵌入；消息、调用信息和进程信息中的 JSON 文本仍按字符串规则编码。
`Time` 的环境变量回退值采用普通属性值的编码规则。通过 `slog.Any` 传入的复合值遵循自身的 JSON 序列化规则。

例如，模板将消息键名设为 `@message`，堆栈仍通过普通业务字段传入：

```go
log := slog.New(xslog.NewHandler(xslog.WithTemplate(
    `{"time":${Time},"level":${Level},"source":${Source},"@message":${Message}}`,
)))
stack := "goroutine 42 [running]:\n" +
    "runtime/debug.Stack()\n" +
    "\t/usr/local/go/src/runtime/debug/stack.go:26 +0x5e\n"
log.Error("march frame panic", "stack", stack)
```

输出如下，时间与调用位置仅作示例。数组元素各占一行，行内的制表符保留为 JSON 转义 `\t`：

```json
{"time":"2026-08-20T11:48:55.756+08:00","level":"ERROR","source":"wmap/ctl_march.go:562","@message":"march frame panic","stack":[
	"goroutine 42 [running]:",
	"runtime/debug.Stack()",
	"\t/usr/local/go/src/runtime/debug/stack.go:26 +0x5e"]}
```

## 装配与生命周期

自己 `NewHandler` 时，Writer 与动态级别都由调用方管理：

```go
lv := new(slog.LevelVar)
lv.Set(slog.LevelDebug)
log := slog.New(xslog.NewHandler(xslog.WithWriter(w), xslog.WithLevel(lv)))
lv.Set(slog.LevelWarn)
```

未设置级别或传入 `WithLevel(nil)` 时使用 `slog.LevelDebug`。
`WithLevel` 也接受固定的 `slog.Level`；`slog.LevelVar` 自身的零值为 Info。

`Setup` 则替换进程的默认 Logger，并管理由 `WithFile` 打开的文件：

```go
if err := xslog.Setup(
    xslog.WithTemplate(`{"time":${Time:unixMilli},"message":${Message}}`),
    xslog.WithFile("/var/log/game.log", xslog.WithMaxSizeMB(50)),
    xslog.WithLevel(slog.LevelInfo),
); err != nil {
    return err
}
defer xslog.Close()
xslog.SetLevel(slog.LevelDebug)
```

`WithLevel` 在这里确定初始阈值，后续用 `SetLevel` 修改包内的共享级别。
重复 `Setup` 会先安装新 Logger，再关闭旧文件；`Close` 可重复调用。
文件参数校验或打开失败时，`Setup` 返回错误，现有 Logger 保持有效。

自行管理轮转文件时，使用 `OpenFile`：

```go
w, err := xslog.OpenFile("/var/log/game.log", xslog.WithMaxSizeMB(50))
if err != nil {
    return err
}
defer w.Close()
log := slog.New(xslog.NewHandler(xslog.WithWriter(w)))
```

`WithWriter` 与 `WithFile` 按选项顺序互相覆盖。`OpenFile` 提前试开文件，避免路径或权限
错误直到第一次写入才暴露。大小和时间触发轮转，历史文件清理按保留批次和天数执行。

## 输入编码与错误

文本编码器将制表符以外的不可打印字符转义为 `\uXXXX`，文本属性的制表符展开为空格。
JSON 编码器使用 JSON 字符串转义；多行属性按布局规则缩进或展开成数组。

直接作为属性值传入的 `Error()`、`String()` 和 `LogValue()` 在属性解析阶段执行，结果供
布局使用。复合值内部的序列化由编码器处理；外部文本方法 panic 时写成错误占位文本。
无法序列化的值（包括循环引用、通道和函数）输出包含类型的 `<cannot encode 类型>`，
避免格式化失败值时再次遍历循环引用。非字符串 panic 载荷只描述类型，避免错误处理时再次触发递归。
单个输入属性最多嵌套 128 层分组、解析 4096 个 `LogValuer` 值；循环分组或超过限制的
分支转为占位文本，其余字段继续输出。单个值的链式解析仍由 `slog.Value.Resolve` 管理。

布局先生成整条记录，再在同一把锁下写出，多个派生 Logger 的记录不会交错。
布局返回错误时整条记录不写出。模板在配置时探测编码，已选 JSON 编码的记录在写出前
再次校验完整对象。

`slog.Logger` 会忽略 Handler 的错误，可以通过 `WithErrorHandler` 观察布局错误和 Writer 错误：

```go
xslog.WithErrorHandler(func(err error) {
    // 在这里记录或统计输出失败。
})
```

同一个 Handler 可被并发调用，错误回调也需要支持并发。
Writer 未完整写入却返回 `nil` 时，`Handle` 返回 `io.ErrShortWrite` 并调用错误回调。
Writer 和错误回调自行触发的 panic 会向调用方传播，写入锁仍会释放。

不满足 JSON 格式的模板使用文本编码。已选 JSON 编码但实际生成的对象无效时，
`Handle` 返回 `ErrInvalidJSONLayout` 并调用上述回调，该记录不会写出。

## 第三方日志接口

`Adapter` 将第三方库使用的日志接口转发给 `slog.Logger`。直接调用 `log/slog` 的库无需适配。

```go
lib.SetLogger(xslog.NewAdapter(slog.With("module", "zk")))
grpclog.SetLoggerV2(xslog.NewAdapter(nil)) // nil 表示跟随全局默认 Logger
```

可用方法如下，接入时应核对库所要求的完整接口签名：

| 方法族                                      | 行为                                     |
|---------------------------------------------|------------------------------------------|
| `Print` / `Printf` / `Println`              | 按 Info 级别拼接、格式化或逐行拼接       |
| `Debugf` / `Infof` / `Warnf` / `Errorf`     | 按对应级别格式化                         |
| `Debug` / `Info` / `Warn` / `Error`         | 按对应级别拼接                           |
| `Debugln` / `Infoln` / `Warnln` / `Errorln` | 按对应级别逐行拼接                       |
| `Warning` / `Warningf` / `Warningln`        | 按 Warn 级别输出                         |
| `Fatal` / `Fatalf` / `Fatalln`              | 按 Error 输出后调用 `os.Exit(1)`         |
| `Panic` / `Panicf` / `Panicln`              | 按 Error 输出后触发 `panic`              |
| `Debugw` / `Infow` / `Warnw` / `Errorw`     | 添加按 `k, v` 交替传入的字段             |
| `V(int) bool`                               | 正数查询 Debug 是否启用，其他值查询 Info |

适配器采集的位置指向调用方。传 `nil` 时每次读取当前全局默认 Logger，因此后续调用
`Setup` 仍会生效。`Fatal` 的退出行为不受级别过滤影响，且不会执行 defer。
`Println` 和各级别的 `ln` 方法去掉 `fmt.Sprintln` 追加的末尾换行，由 Handler 统一补齐记录换行。

## 选项一览

`NewHandler` 与 `Setup` 共用：

| 选项                             | 作用                             | 默认                    |
|----------------------------------|----------------------------------|-------------------------|
| `WithWriter(io.Writer)`          | 输出目标                         | `os.Stdout`             |
| `WithLevel(slog.Leveler)`        | 级别阈值                         | `slog.LevelDebug`       |
| `WithTemplate(string)`           | 编译输出模板，决定编码和格式     | `DefaultTemplate`       |
| `WithErrorHandler(ErrorHandler)` | 接收布局错误及 Writer 错误       | 不设置                  |

`WithFile(string, ...FileOption)` 仅供 `Setup` 使用，文件由 `Close` 关闭。
轮转选项由 `OpenFile` 和 `WithFile` 共用：

| 选项                      | 作用                                                      | 默认   |
|---------------------------|-----------------------------------------------------------|--------|
| `WithMaxSizeMB(int)`      | 单文件大小上限（MiB，1024×1024 字节），超过即轮转         | 50     |
| `WithMaxBackups(int)`     | 最多保留的历史轮转批次数                                  | 7      |
| `WithMaxAgeDays(int)`     | 历史文件最长保留天数                                      | 7      |
| `WithRotationHours(int)`  | 两次轮转的最大间隔（小时），**给 0 是关掉按时间轮转**     | 24     |
| `WithCompression(string)` | `CompressionNone` / `CompressionGzip` / `CompressionZstd` | 不压缩 |

轮转数值参数为负数时，`OpenFile` 或 `Setup` 返回错误。`WithRotationHours(0)` 关闭按时间
轮转；`WithMaxBackups(0)` 和 `WithMaxAgeDays(0)` 分别关闭对应的清理限制。
`WithMaxSizeMB(0)` 使用底层轮转器的默认上限。

## 测试

运行完整测试、race 检查和静态检查：

```sh
go test -race -shuffle=on -cover ./...
go vet ./...
```

单独运行缺失 JSON 值的模糊测试：

```sh
go test -run='^$' -fuzz='^FuzzOmitMissingJSONValues$' -fuzztime=10s -parallel=2
```

另外四个模糊测试目标是 `FuzzTemplateCompilation`、`FuzzInjectorPreservesUnresolvedText`、
`FuzzJSONAttributeString` 和 `FuzzHandlerRecord`。常规测试包含 256 种缺失值组合、多行与结构化值的语义校验、
并发写入、错误恢复和子进程极端输入测试。

扩大压力测试规模（以下为 shell 环境变量写法）：

```sh
XSLOG_STRESS_RECORDS=1000000 XSLOG_STRESS_WORKERS=64 \
    go test -run='^TestStressConcurrentRecords$' -count=1 -v
```

每种编码各写入指定数量的记录，逐条检查完整性、唯一序号和字段值。默认每种编码 2048 条、
8 个并发工作协程；环境变量可调整到每种编码 200 万条、512 个工作协程。

运行性能基准，报告耗时、内存分配及并发表现：

```sh
go test -run='^$' -bench=. -benchmem -cpu=1,4,16 -count=3 -benchtime=200ms
```

## 许可

[Apache License 2.0](LICENSE)
