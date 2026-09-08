# xslog

基于标准库 `log/slog` 的两个 Handler：一个给人读，一个给机器读。

装上其中一个之后，全仓库直接用 `slog.Debug/Info/Warn/Error` 写日志，不需要任何自定义
Logger 包装层。

```go
slog.SetDefault(slog.New(xslog.NewConsoleHandler()))

slog.Info("listen on", "network", "tcp", "address", "[::]:8080")
```

```
2026-08-20T11:48:55.756+08:00 INFO [gate/gate.go:100] message="listen on" network="tcp" address="[::]:8080"
```

## 安装

```bash
go get github.com/xmapst/xslog
```

需要 **Go 1.27+**：JSON 那份的复合值走 `encoding/json/v2`。文件轮转依赖
[timberjack](https://github.com/DeRuina/timberjack)。

## 装配

两条路，分界在**谁来管生命周期**。

`Setup` 换掉进程的默认 Logger，格式、级别、去向都从配置来——服务启动时要的通常就是这一句：

```go
if err := xslog.Setup(
    xslog.WithFormat(xslog.FormatJSON),          // 拼错了在这里就报错
    xslog.WithFile("/var/log/game.log"),         // 文件由 Setup 开、Close 关
    xslog.WithLevel(lv),                         // 装配那一刻的阈值
); err != nil {
    return err
}
defer xslog.Close()

xslog.SetLevel(slog.LevelWarn) // 运行期调级，立即生效，不必重建 Handler
```

它也是这个包唯一存东西的地方，一共两样：一个动态级别（`SetLevel`），一份日志文件（`Close`）。
重复调用是安全的，上一次开的文件会在新 Logger 装好之后被关掉，所以热更日志配置不必先
`Close` 再 `Setup`。

自己 `New` 则一无所持——Handler 只管把记录渲染成字节，写到哪、什么时候关都在外面：

```go
slog.SetDefault(slog.New(xslog.NewConsoleHandler(xslog.WithWriter(w))))
```

一个进程里要同时留几份去向不同的日志（比如审计单独落一份），走的是这条：那里没有
「进程唯一」可言，也就没有谁有资格替调用方持有那个文件。

## 两个 Handler

渲染的是同一份记录，取舍相反。

| | `ConsoleHandler` | `JSONHandler` |
|---|---|---|
| 一条记录 | 一行（多行值另起竖线块） | 一个 JSON 对象 |
| 服务于 | 一个人正盯着控制台 | 采集器、`jq` |
| 可解析 | **不保证** | 保证 |
| 上色 | 不 | — |

选哪个就 `New` 哪个。按配置切格式是 [装配](#装配) 那条路的事（`WithFormat`），
它顺带把「配置里的格式拼错了」变成启动时的一个错误返回值，而不是静静换一种格式、
等有人去看日志才发现。

### 控制台

```go
log := slog.New(xslog.NewConsoleHandler())

log.Info("listen on", "network", "tcp", "address", "[::]:8080")
log.Error("march frame panic", "module", "wmap", "panic", err, "stack", stack)
log.With("module", "battle").WithGroup("frame").
    Debug("tick", "seq", 1042, "cost", 3.5, "ok", true)
```

```
2026-08-20T11:48:55.756+08:00 INFO [gate/gate.go:100] message="listen on" network="tcp" address="[::]:8080"
2026-08-20T11:48:55.756+08:00 ERROR [wmap/ctl_march.go:562] message="march frame panic" module="wmap" panic="runtime error: invalid memory address"
    stack │ goroutine 42 [running]:
          │ runtime/debug.Stack()
          │   /usr/local/go/src/runtime/debug/stack.go:26 +0x5e
          │ main.frame()
          │   /src/wmap/ctl_march.go:562 +0x1a4
2026-08-20T11:48:55.756+08:00 DEBUG [battle/frame.go:88] message="tick" module="battle" frame.seq=1042 frame.cost=3.5 frame.ok=true
```

几条规则值得先知道：

- **文本值恒定带引号，数字与布尔不带。** 文本可以含空格，不加引号就看不出一个值在哪儿
  结束、下一个键名从哪儿开始；恒定加而不是按需加，是为了不让同一个字段的形态随内容跳变。
  少了那对引号，「这是个数字还是一串数字组成的文本」一眼就分得开。
- **只有含换行的值才另起竖线块**，几个块按键名左对齐，列宽按本条记录里最长的那个算。
- **分组压成点分键名**（`frame.seq`），不做嵌套缩进：缩进已经用来区分「头行 / 字段 /
  多行块」三层了。
- **一段本身就是 JSON 的字符串按结构化值写出**，不加引号也不转义——否则满屏 `\"`。

### JSON

```go
log := slog.New(xslog.NewJSONHandler())
```

```json
{"@time":"2026-08-20T11:48:55.756+08:00","@level":"INFO","@source":"gate/gate.go:100","@message":"listen on","network":"tcp","address":"[::]:8080"}
```

固定成员一律带 `@` 前缀（`@time` / `@level` / `@source` / `@message`），业务字段一律不带，
两者因此永远撞不上。多行值展开成字符串数组，一行一个元素：

```json
{"@time":"…","@level":"ERROR","@source":"wmap/ctl_march.go:562","@message":"march frame panic","stack":[
	"goroutine 42 [running]:",
	"runtime/debug.Stack()",
	"\t/usr/local/go/src/runtime/debug/stack.go:26 +0x5e"]}
```

## 自定义头

两种格式对「头」的要求不是同一件事，所以是**两套互不相干的选项**：传错地方不会报错，
但也不会有任何效果。

### JSON：稳定的键值对，装配时算一次

```go
xslog.NewJSONHandler(xslog.WithJSONHeader(func() []xslog.HeaderField {
    return []xslog.HeaderField{
        {Key: "app", Value: "gate"},
        {Key: "node", Value: "3"},
    }
}))
```

结果夹在 `@source` 与 `@message` 之间：

```json
{"@time":"…","@level":"INFO","@source":"gate/gate.go:100","app":"gate","node":"3","@message":"listen on"}
```

只调一次而不是每条调一次：这一段的用处是「从混在一起的日志里认出这条是谁打的」，答案在
一个进程的生命周期里不会变。键名不要加 `@` 前缀，那是留给固定成员的。

### 控制台：一段排好的文本，逐条算

不设时头行恒为 `时间 级别 [位置] 消息`。设了 `WithConsoleHeader`，**「时间之后、消息之前」
整段交给调用方**——级别与位置作为参数给到，Handler 自己不再写它们：

```go
xslog.NewConsoleHandler(xslog.WithConsoleHeader(
    func(lv slog.Level, src string) []string {
        return []string{"gate", lv.String()[:1], "node-3", "[" + src + "]"}
    }))
```

```
2026-08-20T11:48:55.756+08:00 gate I node-3 [gate/gate.go:100] message="listen on" network="tcp"
```

级别写在哪一格、缩写成什么样、要不要定宽对齐，都由这里决定。`src` 形如
`gate/gate.go:100`、不带方括号（手工构造的 Record、PC 为 0 时是空串）。

- 返回的每一项自成一段，Handler 用一个空格隔开；**空串整项跳过**（连同分隔空格），
  所以「这条没有 trace id」不会在行里留下一个孤零零的空格。
- **上不了色**：内容和其它输入走同一条转义路径，ANSI 序列里的 ESC 会变成 `\u001b`
  原样打出来。这一条不为头破例，理由见下面的[安全性质](#安全性质)。
- 每条一次函数调用与一次切片分配是它的全部代价。只想要一段恒定标记的话，闭包里返回一个
  提前建好的切片即可：

  ```go
  fixed := []string{"gate", "node-3"} // 那时只剩一次函数调用，零分配
  xslog.WithConsoleHeader(func(slog.Level, string) []string { return fixed })
  ```

- 它自己 panic 时就地兜住，头里写出 `<header panicked: …>`——Handler 常常正是在
  `recover` 内部被调用的，在那里再炸一次等于把兜底本身炸掉。

## 级别

不传 `WithLevel` 时是 `DefaultLevel`，即 **Debug**（不是标准库的 Info）：这个包的默认
输出是给人看的控制台格式，而会去看控制台的场合几乎都是在开发或排查，那时被默认值挡掉的
Debug 正是要看的东西。

运行期怎么调级，两条装配路各不相同。

走 `Setup`：`WithLevel` 只定装配那一刻的阈值，之后一律用 `SetLevel`。装出来的 Handler
持有的始终是包内那一个 `LevelVar`，传自己的 `LevelVar` 进去也只是给了个起点，后续再
`Set` 它不会传导过去。

```go
xslog.Setup(xslog.WithLevel(slog.LevelInfo))
xslog.SetLevel(slog.LevelDebug) // 立即生效
```

自己 `New`：调级就是你自己的事——留住那个 `LevelVar`，想调的时候 `Set` 一下即可。

```go
lv := new(slog.LevelVar)
lv.Set(slog.LevelDebug) // 注意：LevelVar 的零值是 Info，不是 DefaultLevel
slog.SetDefault(slog.New(xslog.NewConsoleHandler(xslog.WithLevel(lv))))

lv.Set(slog.LevelInfo)  // 立即生效，不必重建 Handler
```

`WithLevel` 收的是 `slog.Leveler`，所以也可以传一个钉死的 `slog.Level`。多个 Handler
要一起跟着变，把同一个 `LevelVar` 都传进去。

## 时间格式

默认是 RFC3339 毫秒（`DefaultTimeLayout`）：它按字典序排就是按时间序排，带时区偏移
因此跨机器的日志能直接比对。换一种可读写法用 `WithTimeLayout`，收的是 `time` 包的
参考时间串：

```go
xslog.WithTimeLayout("15:04:05.000") // 只留时分秒
```

要 **Unix 纪元时间戳**用这四个常量之一——它们没有对应的 layout 串，只能另立取值：

```go
xslog.WithTimeLayout(xslog.LayoutUnixMilli)
```

| 常量 | 精度 |
|---|---|
| `LayoutUnix` | 秒 |
| `LayoutUnixMilli` | 毫秒 |
| `LayoutUnixMicro` | 微秒 |
| `LayoutUnixNano` | 纳秒 |

JSON 那份写成**裸数字**，下游可以直接比大小、做范围查询，不必先 `tonumber`：

```json
{"@time":1788854460043,"@level":"INFO","@source":"gate/gate.go:100","@message":"listen on","deadline":1755661800000}
```

```
1788854460043 INFO [gate/gate.go:100] message="listen on" deadline=1755661800000
```

两件事值得留意：

- **这个设置同时管行首时间和 `time.Time` 字段值**（上面的 `deadline` 就是），两者恒为
  同一种形态——不一致的话，想拿日志行里的时间去比对某个时间字段会对不上，而那正是这
  两个东西最常一起被读的场合。
- 时间戳形态下值是裸数字，控制台那边因此**不加引号**，与「数字与布尔不加引号」的规则
  一致；JSON 那边同理去掉引号，所以 `@time` 是 JSON 数字而不是字符串。

至于为什么时间戳不能写成一个 layout 串：`time` 包的 layout 动词是参考时间
`2006-01-02T15:04:05` 里的那几个数字，加上 `Jan` / `Mon` / `MST` / `PM` 这几个词，
每一个都对应一个**日历字段**，而纪元秒不是其中之一。反过来，拿 `"unix"` 这类词当哨兵
也就是安全的：真把它当 layout 传给 `Format`，得到的是字面量 `unix`，没有人会想要那个。

## 落盘与轮转

两条路各有各的写法，轮转参数（`FileOption`）是同一套。

走 `Setup`，给个路径就行，文件由它开、`Close` 关：

```go
if err := xslog.Setup(
    xslog.WithFormat(xslog.FormatJSON),
    xslog.WithFile("/var/log/game.log",
        xslog.WithMaxSizeMB(50),
        xslog.WithMaxBackups(7),
        xslog.WithCompression(xslog.CompressionZstd),
    ),
); err != nil {
    return err
}
defer xslog.Close()
```

自己 `New`，就自己 `OpenFile` 再用 `WithWriter` 传进去：

```go
w, err := xslog.OpenFile("/var/log/game.log", xslog.WithMaxSizeMB(50))
if err != nil {
    return err
}
defer w.Close() // 谁开的谁关

slog.SetDefault(slog.New(xslog.NewJSONHandler(xslog.WithWriter(w))))
```

开文件不归 Handler 管：它带来一个需要释放的资源，而一个进程里可以有好几个 Handler，
谁也没资格替别人决定那个文件什么时候关。`Setup` 那条路上这个前提不一样——它装的是
进程唯一的那个 Logger，「唯一」在，生命周期就只有一份，所以 `WithFile` 可以把开和关
都接过去。

忘了 `Close` 不会丢日志：底下的轮转器不攒缓冲，每条记录当场就落到文件上，关闭之后仍在
写的那些也是开-写-关地照写一遍。漏掉的是句柄和轮转的收尾，不是内容。

`OpenFile` 会**提前试开一次**再返回。轮转器本身是首次写入时才打开文件的，打不开也只把
错误交给 `io.Writer` 的返回值——而 slog 那条链上没人看它。于是路径写错、目录不存在、
没有写权限，装配照样成功、进程照常跑，整个服务的日志全进黑洞，还没有任何提示，恰恰因为
提示本身也进了黑洞。

## 安全性质

日志里有外部可控的文本——客户端上行帧、脚本正文、远端返回的错误消息。两个 Handler 对它们
的处理一致：

- **不可打印字符一律转义成 `\uXXXX`**，判定按 `unicode.IsPrint` 取反，而不是枚举已知的
  坏字符——那样每冒出一个新的控制符类别就会漏一次。ANSI 与八位形式的控制序列因此清不了屏、
  改不了色，双向覆写符伪装不了内容，零宽字符藏不住东西。
- **多行值展开出来的每一行都带缩进**（控制台是竖线块，JSON 是制表符），顶格的行因此恒为
  一条记录的开头：值里被塞进换行，伪造不出一条以假时间戳打头的记录。
- 取值时调到的外部代码（`Error()`、`String()`、自定义头）panic 一律就地兜住，写成
  `<log value panicked: …>`。

控制台那份**不保证可解析**，不要拿它去喂采集器——那是 JSON 那份的活。

## 桥接不认 slog 的第三方库

大多数库不需要这个：只要它直接调 `log/slog` 的包级函数，就会自动走到你装上的 Handler。
只有把 Logger 做成自有接口的库才需要：

```go
lib.SetLogger(xslog.NewAdapter(slog.With("module", "zk")))
grpclog.SetLoggerV2(xslog.NewAdapter(nil)) // nil = 跟随全局默认 Logger
```

Go 的接口是结构化的，库要的那几个方法在就行，多出来的不碍事——所以下面这些形状全挂在
同一个 `Adapter` 上，不必按库分成一堆类型：

| 方法族 | 形状 | 谁在用 |
|---|---|---|
| `Print` / `Printf` / `Println` | 标准库 `log.Logger` | 最常见的注入点 |
| `Debugf` / `Infof` / `Warnf` / `Errorf` | 分级 + 格式化 | sarama、resty、colly… |
| `Debug` / `Info` / `Warn` / `Error` | 分级 + 拼接 | logrus、zap `SugaredLogger` |
| `Debugln` / `Infoln` / `Warnln` / `Errorln` | 同上的 `Sprintln` 版 | logrus |
| `Warning` / `Warningf` / `Warningln` | grpclog 用 Warning 而不是 Warn | grpclog |
| `Fatal` / `Fatalf` / `Fatalln` | 打完 `os.Exit(1)` | grpclog、logrus |
| `Panic` / `Panicf` / `Panicln` | 打完 `panic` | logrus、zap |
| `Debugw` / `Infow` / `Warnw` / `Errorw` | 键值，`k, v, k, v` 交替 | zap `SugaredLogger` |
| `V(int) bool` | verbosity 开关 | `grpclog.LoggerV2` |

合起来它直接满足 `grpclog.LoggerV2`、logrus 的 `StdLogger`、zap `SugaredLogger` 的常用
子集，以及一大票库自定义的 `Debugf/Infof/...` 接口。

每个方法都跳过适配器自身的调用帧——日志行记录的位置指向真正触发日志的库代码，而不是
适配器内部。传 `nil` 表示跟随全局默认 Logger，且是**每条日志现取**：注入库的 Logger
通常发生在 `main` 的最前面，而 `Setup` 要等配置读完才调得上，存一次的话这中间装的
Handler 会被库一直用到进程结束。

`Fatal` 系列是真的退出，且与级别无关：级别把那条日志挡掉了，进程照样退——不然一个日志
级别就能改掉程序的控制流。

两种形状有意没做：logr 的 `Info(msg string, kv ...any)` 和上面的 `Info(args ...any)`
只有签名不同，一个类型上放不下两个（logr 生态自己有 slog 的桥）；gorm 那种带 `ctx`、
还要 `LogMode` 和 `Trace` 的接口要的不是「把一行字打出去」，塞进这个通用形状只会两头不像。

## 选项一览

`Setup` 与两个 Handler 都认：

| 选项 | 作用 | 默认 |
|---|---|---|
| `WithWriter(io.Writer)` | 输出目标 | `os.Stdout` |
| `WithLevel(slog.Leveler)` | 级别阈值（走 `Setup` 时只定装配那一刻的，之后用 `SetLevel`） | `DefaultLevel`（Debug） |
| `WithTimeLayout(string)` | 行首时间与 `time.Time` 字段值共用的格式；`LayoutUnix*` 则写成时间戳 | RFC3339 毫秒 |

只有 `Setup` 认——自己 `New` 的那条路上，格式就是选构造函数，文件就是自己 `OpenFile`：

| 选项 | 作用 | 默认 |
|---|---|---|
| `WithFormat(string)` | `FormatConsole` / `FormatJSON`，拼错了 `Setup` 返回错误 | 控制台 |
| `WithFile(string, ...FileOption)` | 落盘路径，文件由 `Setup` 开、`Close` 关；与 `WithWriter` 互为覆盖 | 不落盘 |

只有一边 Handler 认：

| 选项 | 归谁 |
|---|---|
| `WithConsoleHeader(ConsoleHeaderFunc)` | `NewConsoleHandler` |
| `WithJSONHeader(JSONHeaderFunc)` | `NewJSONHandler` |

落盘的选项是另一套类型 `FileOption`，`OpenFile` 和 `WithFile` 共用——「这一项调的是
渲染还是落盘」在类型上就分得清，把 `WithMaxSizeMB` 传给 `NewConsoleHandler` 编译期
就挡住了：

| 选项 | 作用 | 默认 |
|---|---|---|
| `WithMaxSizeMB(int)` | 单文件大小上限（MB），超过即轮转 | 50 |
| `WithMaxBackups(int)` | 最多保留几个历史文件 | 7 |
| `WithMaxAgeDays(int)` | 历史文件最长保留天数 | 7 |
| `WithRotationHours(int)` | 两次轮转的最大间隔（小时），**给 0 是关掉按时间轮转** | 24 |
| `WithCompression(string)` | `CompressionNone` / `CompressionGzip` / `CompressionZstd` | 不压缩 |

## 许可

[Apache License 2.0](LICENSE)
