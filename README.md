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
    `{"timestamp":${TimeUnixMilli},"severity":${Level:u3},` +
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

| 占位符             | 内容                                                                                                   |
|--------------------|--------------------------------------------------------------------------------------------------------|
| `${Time}`          | 记录时间，省略格式时使用 RFC3339 毫秒；Go 布局见「Time 支持的格式」                                    |
| `${TimeUnix}`      | 记录时间的 Unix 时间戳（秒），数字，不受动词和格式影响                                                 |
| `${TimeUnixMilli}` | 毫秒时间戳，其余同上                                                                                   |
| `${TimeUnixMicro}` | 微秒时间戳，其余同上                                                                                   |
| `${TimeUnixNano}`  | 纳秒时间戳，其余同上                                                                                   |
| `${Level}`         | 完整级别名；`:u3` 为 `DBG/INF/WRN/ERR`，`:w3` 小写，`:t3` 为 `Dbg/Inf/Wrn/Err`，见「Level 支持的格式」 |
| `${Source}`        | 调用位置，不带方括号                                                                                   |
| `${File}`          | 调用文件路径，保留运行时提供的路径                                                                     |
| `${FileName}`      | 调用文件名，如 `handler.go`                                                                            |
| `${Line}`          | 调用行号，JSON 中为数字                                                                                |
| `${Function}`      | 调用函数名，不含包路径，保留接收者信息                                                                 |
| `${Package}`       | 调用方的完整包路径                                                                                     |
| `${PID}`           | 当前进程 ID，JSON 中为数字                                                                             |
| `${Hostname}`      | 当前主机名                                                                                             |
| `${Executable}`    | 当前可执行文件路径                                                                                     |
| `${Message}`       | 消息；文本中默认带引号，`:l` 或 `:lj` 去掉引号                                                         |
| `${NewLine}`       | 显式插入一个 LF，用于模板布局换行                                                                      |

内置名称不区分大小写。占位符不查找业务字段，业务字段的名称也不影响内置元数据。
调用信息从记录的 PC 解析并共享缓存；`Source` 保持 `上级目录/文件名:行号` 的缩短格式，
`File` 保留完整的运行时路径。进程信息在创建 Handler 时读取一次，派生 Logger 复用这份快照。
调用信息缺失或进程信息读取失败时，照常回退环境变量。这些字段的回退值默认按字符串规则编码；`Line` 和 `PID` 带类型动词时按数值编码，见「格式动词」。
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
文本模板中的 `:j` 按 JSON 规则编码对应值，例如 `${Message:j}`。通用动词、`Time` 和 `Level` 各支持哪些格式，见「格式动词」一章。

模板保留字面量中的空格和换行。Handler 在整条记录末尾自动补齐 LF，已有换行时不重复追加。
需要手工换行时，使用 `${NewLine}` 或 `%NewLine%`；连续使用可插入空行，末尾使用不会多补换行。
它是布局控制符，在 JSON 模板中应放在字符串引号外，作为成员之间或对象前后的空白：

```go
xslog.WithTemplate("${Message:l}${NewLine}details")
xslog.WithTemplate(`{"message":${Message},${NewLine}"pid":${PID}}${NewLine}`)
```

占位符先查日志内置元数据，缺失时按占位符原名回退系统环境变量。环境变量在每次
渲染时读取，空值也算存在；读取后使用相同的文本或 JSON 编码，并支持对齐和格式动词，如 `:l`、`:u`、`:w` 和类型动词（见「格式动词」）。
两处都没有的值不输出：文本省略占位值，JSON 省略对应的成员或数组元素。
JSON 中业务属性的 `nil` 保留为 `null`，空环境变量保留为空字符串。动态值不会再次当作模板解析。

模板复用 `Injector` 的解析、替换和对齐实现。`$VAR`、`$(VAR)`、
`{VAR}` 和 `{{VAR}}` 都按普通文本保留；JSON 对象的花括号不参与占位符解析。

未设置模板或传入 `WithTemplate("")` 时使用 `DefaultTemplate`，与自定义模板共用渲染流程：

```text
${Time} ${Level} [${Source}] message=${Message}
```

位置缺失时，模板里的 `[]` 仍会保留。多次设置模板时，后传入的选项生效。

## 格式动词

占位符冒号之后的格式写成 `${Name[,alignment][:format]}`，由三部分组成，职责互不重叠：

| 部分     | 写法                                                            | 范围                                 |
|----------|-----------------------------------------------------------------|--------------------------------------|
| 对齐     | `,alignment`，如 `,6`、`,-12`                                   | 所有占位符共用，在格式化之后补白     |
| 动词     | `:u`、`:l`、`:d` 等                                             | 各占位符共用，只在适用的占位符上生效 |
| 专用格式 | `Time` 的 Go 时间布局，`Level` 缩写的宽度 N（`uN`、`wN`、`tN`） | 只属于一种占位符                     |

对齐和动词对所有占位符通用，专用格式只有两处：`Time` 的 Go 时间布局，以及只有 `Level` 解释的缩写宽度 N。
动词由同一个动词管理器解释，区分大小写；不适用于占位符的动词被忽略，既不会报错，也不会原样输出。
下表列出每个占位符接受的格式，「忽略」表示写了也没有效果，输出与省略格式时相同。
后面分四节说明：「通用动词」「Time 支持的格式」「Level 支持的格式」和「环境变量的类型动词」。

| 占位符                                                                  | 引号动词 `l` `lj` `j` | 大小写动词 `u` `w` `tN` | 类型动词 | 其他格式    |
|-------------------------------------------------------------------------|-----------------------|-------------------------|----------|-------------|
| `Time`                                                                  | 只有 `j` 有效         | 忽略                    | 忽略     | Go 时间布局 |
| `Level`                                                                 | 有效                  | 有效，N 取缩写          | 忽略     | 无          |
| `Message`                                                               | 有效                  | 有效，N 被忽略          | 忽略     | 无          |
| `Source` `File` `FileName` `Function` `Package` `Hostname` `Executable` | 有效                  | 有效，N 被忽略          | 忽略     | 无          |
| `Line` `PID` `TimeUnix` `TimeUnixMilli` `TimeUnixMicro` `TimeUnixNano`  | 忽略                  | 忽略                    | 忽略     | 无          |
| `NewLine`                                                               | 忽略                  | 忽略                    | 忽略     | 无          |
| 环境变量（回退值）                                                      | 有效                  | 有效，N 被忽略          | 有效     | 无          |

`Line`、`PID` 和时间戳占位符在对应的调用信息、进程信息或记录时间缺失、回退到同名环境变量时，
按环境变量处理，此时类型动词有效。

### 通用动词

通用动词与具体占位符无关，所有字符串值共用同一套写法，分为两组：引号动词 `l`、`lj`、`j` 决定文本模板中怎样编码，
大小写动词 `u`、`w`、`tN` 转换字符串；两组可以连写。

| 动词     | 作用                                                             | 适用的值 | 在 JSON 模板中 |
|----------|------------------------------------------------------------------|----------|----------------|
| `l` `lj` | 文本中去掉字符串的引号，`lj` 与 `l` 相同                         | 字符串   | 无效果         |
| `j`      | 文本中按 JSON 规则编码：字符串加引号并转义，多行字符串展开为数组 | 字符串   | 无效果         |
| `u`      | 转大写                                                           | 字符串   | 同样转换       |
| `w`      | 转小写                                                           | 字符串   | 同样转换       |
| `tN`     | 标题大小写：首字符大写，其余小写；必须带宽度 N，不需要时写 `t0`  | 字符串   | 同样转换       |

下面的示例使用同一条记录：消息 `Hello World`，级别 `INFO`，环境变量 `APP_ENV=Prod`。
文本列是文本模板的输出，JSON 列是同一占位符在 JSON 模板中输出的值。

引号动词只影响文本模板：

| 模板            | 文本输出        | JSON 输出       |
|-----------------|-----------------|-----------------|
| `${Message}`    | `"Hello World"` | `"Hello World"` |
| `${Message:l}`  | `Hello World`   | `"Hello World"` |
| `${Message:lj}` | `Hello World`   | `"Hello World"` |
| `${Message:j}`  | `"Hello World"` | `"Hello World"` |
| `${APP_ENV}`    | `"Prod"`        | `"Prod"`        |
| `${APP_ENV:l}`  | `Prod`          | `"Prod"`        |
| `${Level}`      | `INFO`          | `"INFO"`        |
| `${Level:j}`    | `"INFO"`        | `"INFO"`        |

大小写动词转换字符串，不改变引号规则：

| 模板            | 文本输出        | JSON 输出       |
|-----------------|-----------------|-----------------|
| `${Message:u}`  | `"HELLO WORLD"` | `"HELLO WORLD"` |
| `${Message:w}`  | `"hello world"` | `"hello world"` |
| `${Message:t0}` | `"Hello world"` | `"Hello world"` |
| `${Message:u3}` | `"HELLO WORLD"` | `"HELLO WORLD"` |
| `${APP_ENV:u}`  | `"PROD"`        | `"PROD"`        |
| `${Level:w}`    | `info`          | `"info"`        |

连写时引号动词在前、大小写动词在后，先转换再按引号规则编码：

| 模板             | 文本输出        | JSON 输出       |
|------------------|-----------------|-----------------|
| `${Message:lu}`  | `HELLO WORLD`   | `"HELLO WORLD"` |
| `${Message:lw}`  | `hello world`   | `"hello world"` |
| `${Message:lt0}` | `Hello world`   | `"Hello world"` |
| `${Message:ju}`  | `"HELLO WORLD"` | `"HELLO WORLD"` |
| `${APP_ENV:lu}`  | `PROD`          | `"PROD"`        |
| `${Level:lu3}`   | `INF`           | `"INF"`         |
| `${Level:ju}`    | `"INFO"`        | `"INFO"`        |

规则：

- **文本中的引号。** `Message` 和环境变量的字符串默认带引号；`Level`、调用信息和进程信息默认不带；
  数值和布尔值从不带引号。JSON 模板中字符串总是 JSON 字符串，`l`、`lj`、`j` 没有效果。
- **转换不改变引号。** 文本模板中 `${APP_ENV:u}` 输出带引号的 `"PROD"`；需要不带引号的 `PROD` 时连写成 `lu`。
- **只转换字符串。** 大小写动词作用于 `Level`、`Message`、`Source`、`File`、`FileName`、`Function`、`Package`、
  `Hostname`、`Executable` 和环境变量字符串。数值（`Line`、`PID`、时间戳）、布尔值和时间保持原样，
  `${PID:u}` 在 JSON 中仍是数字。
- **`tN` 只处理首字符。** 它把第一个字符转为大写、其余转为小写，不按单词分段：`Hello World` 得到 `Hello world`。
  单独的 `t` 是环境变量的布尔类型动词，所以标题大小写必须带宽度，不需要宽度时写 `t0`。
- **N 只对 `Level` 有意义。** `uN`、`wN`、`tN` 的 N 用来取级别缩写，见「Level 支持的格式」；
  对其他字符串 N 被忽略，`${Message:u3}` 与 `${Message:u}` 相同。
- **连写的语法。** `[引号动词][大小写动词]`，两部分都可以省略，例如 `lu`、`lw3`、`ju`、`lt0`；
  类型动词不参与连写。不符合语法的写法不是动词，见下表。

以下写法不是动词，当作没有格式，输出与省略格式时相同：

| 写法              | 文本输出        | JSON 输出       | 原因                                      |
|-------------------|-----------------|-----------------|-------------------------------------------|
| `${Message:ul}`   | `"Hello World"` | `"Hello World"` | 顺序颠倒，引号动词要写在前面              |
| `${Message:jl}`   | `"Hello World"` | `"Hello World"` | 引号动词不能连写，至多一个                |
| `${Message:lt}`   | `"Hello World"` | `"Hello World"` | `t` 必须带宽度，单独的 `t` 是布尔类型动词 |
| `${Message:W}`    | `"Hello World"` | `"Hello World"` | 动词区分大小写，没有 `W`                  |
| `${Message:u+3}`  | `"Hello World"` | `"Hello World"` | 宽度只能是数字                            |
| `${Message:uabc}` | `"Hello World"` | `"Hello World"` | 宽度只能是数字                            |

调用信息和进程信息也是字符串，同样适用。下表以 `/srv/app/internal/gate/gate.go` 第 100 行的 `(*gate).serve`
方法为例；`Line` 是数值，不受任何动词影响：

| 模板             | 文本输出           | JSON 输出            |
|------------------|--------------------|----------------------|
| `${FileName}`    | `gate.go`          | `"gate.go"`          |
| `${FileName:u}`  | `GATE.GO`          | `"GATE.GO"`          |
| `${FileName:t0}` | `Gate.go`          | `"Gate.go"`          |
| `${FileName:j}`  | `"gate.go"`        | `"gate.go"`          |
| `${Source}`      | `gate/gate.go:100` | `"gate/gate.go:100"` |
| `${Source:u}`    | `GATE/GATE.GO:100` | `"GATE/GATE.GO:100"` |
| `${Function}`    | `(*gate).serve`    | `"(*gate).serve"`    |
| `${Function:u}`  | `(*GATE).SERVE`    | `"(*GATE).SERVE"`    |
| `${Line}`        | `100`              | `100`                |
| `${Line:u}`      | `100`              | `100`                |
| `${Line:j}`      | `100`              | `100`                |

多行字符串在文本模板中默认写成带 `\u000a` 转义的单行字符串；`:j` 与 JSON 模板一样展开为按行的字符串数组。
下表的消息由 `first line` 和 `second line` 两行组成，JSON 列省略了数组内的缩进和换行；
`${Message:j}` 在文本模板中输出同样的数组：

| 模板           | 文本输出                        | JSON 输出                      |
|----------------|---------------------------------|--------------------------------|
| `${Message}`   | `"first line\u000asecond line"` | `["first line","second line"]` |
| `${Message:l}` | `first line\u000asecond line`   | `["first line","second line"]` |
| `${Message:u}` | `"FIRST LINE\u000aSECOND LINE"` | `["FIRST LINE","SECOND LINE"]` |

对齐在转换和编码之后补白，引号也计入宽度，宽度按 Unicode 码点计算：

| 模板                 | 文本输出           |
|----------------------|--------------------|
| `[${Message,-14:l}]` | `[Hello World   ]` |
| `[${Message,14:l}]`  | `[   Hello World]` |
| `[${Message,-14}]`   | `["Hello World" ]` |
| `[${APP_ENV,-8:lu}]` | `[PROD    ]`       |

组合使用：

```go
// APP_ENV=Prod
xslog.WithTemplate(`{"level":${Level:w},"env":${APP_ENV:w},"message":${Message}}`)
// {"level":"info","env":"prod","message":"listen on"}

xslog.WithTemplate("[${Level:u3}] [${APP_ENV:lu}] ${Message:l}")
// [INF] [PROD] listen on
```

### Time 支持的格式

`Time` 输出记录时间，格式是 Go 的参考时间布局。时间不是字符串，所以通用动词里只有 `j` 有效，
其余动词（`l`、`lj`、大小写动词和类型动词）都被忽略，使用默认布局，不会原样输出。
省略格式时同样使用默认布局：RFC3339 毫秒，即 `2006-01-02T15:04:05.000Z07:00`。

```go
xslog.WithTemplate(
    "${Time:2006-01-02 15:04:05.000} [${Level}] ${Message:l}",
)
```

下面各表的记录时间为 `2024-01-02 03:04:05.678901234 UTC`。Go 布局用参考时间 `Mon Jan 2 15:04:05 MST 2006`
的各个部分表示年月日、时分秒，不属于布局的字符原样输出。冒号之后的全部内容都是格式，
所以布局本身可以含冒号、空格和逗号：

| 模板                                    | 文本输出                        | JSON 输出                         | 说明                                              |
|-----------------------------------------|---------------------------------|-----------------------------------|---------------------------------------------------|
| `${Time}`                               | `2024-01-02T03:04:05.678Z`      | `"2024-01-02T03:04:05.678Z"`      | 省略格式：RFC3339 毫秒                            |
| `${Time:2006-01-02 15:04:05}`           | `2024-01-02 03:04:05`           | `"2024-01-02 03:04:05"`           | 日期和时间                                        |
| `${Time:2006-01-02 15:04:05.000}`       | `2024-01-02 03:04:05.678`       | `"2024-01-02 03:04:05.678"`       | 毫秒，固定三位                                    |
| `${Time:15:04:05.000}`                  | `03:04:05.678`                  | `"03:04:05.678"`                  | 只写时间；布局本身可以含冒号                      |
| `${Time:15:04:05.000000}`               | `03:04:05.678901`               | `"03:04:05.678901"`               | 微秒                                              |
| `${Time:2006-01-02T15:04:05Z07:00}`     | `2024-01-02T03:04:05Z`          | `"2024-01-02T03:04:05Z"`          | RFC3339；UTC 输出 `Z`                             |
| `${Time:Jan _2 15:04:05}`               | `Jan  2 03:04:05`               | `"Jan  2 03:04:05"`               | `_2` 是空格补位的日，syslog 风格                  |
| `${Time:Jan 2, 2006}`                   | `Jan 2, 2024`                   | `"Jan 2, 2024"`                   | 布局可以含逗号和空格                              |
| `${Time:02/Jan/2006:15:04:05 -0700}`    | `02/Jan/2024:03:04:05 +0000`    | `"02/Jan/2024:03:04:05 +0000"`    | 访问日志风格，`-0700` 是时区偏移                  |
| `${Time:Mon, 02 Jan 2006 15:04:05 MST}` | `Tue, 02 Jan 2024 03:04:05 UTC` | `"Tue, 02 Jan 2024 03:04:05 UTC"` | RFC1123                                           |
| `${Time:20060102-150405}`               | `20240102-030405`               | `"20240102-030405"`               | 适合文件名                                        |
| `${Time:3:04PM}`                        | `3:04AM`                        | `"3:04AM"`                        | 12 小时制                                         |
| `${Time:time=15:04:05}`                 | `time=03:04:05`                 | `"time=03:04:05"`                 | 文字可以与布局混写；以 `t` 开头也不是动词，见下表 |

通用动词在 `Time` 上的行为如下。引号动词 `l`、`lj`，大小写动词和类型动词不适用于时间，被忽略，
输出与省略格式时相同；只有 `j` 有效，在文本中给时间加引号。
整个格式必须恰好是一个动词，或引号动词与大小写动词的连写，才会被当作动词忽略；
含有其他内容的都是布局，所以 `${Time:time=15:04:05}` 这样以动词字母开头的写法仍然按布局输出：

| 模板          | 文本输出                     | JSON 输出                    | 说明                                            |
|---------------|------------------------------|------------------------------|-------------------------------------------------|
| `${Time:j}`   | `"2024-01-02T03:04:05.678Z"` | `"2024-01-02T03:04:05.678Z"` | 文本中加引号                                    |
| `${Time:l}`   | `2024-01-02T03:04:05.678Z`   | `"2024-01-02T03:04:05.678Z"` | 时间文本本来不带引号，使用默认布局              |
| `${Time:lj}`  | `2024-01-02T03:04:05.678Z`   | `"2024-01-02T03:04:05.678Z"` | 同上                                            |
| `${Time:u}`   | `2024-01-02T03:04:05.678Z`   | `"2024-01-02T03:04:05.678Z"` | 时间不是字符串，不转换大小写，使用默认布局      |
| `${Time:w}`   | `2024-01-02T03:04:05.678Z`   | `"2024-01-02T03:04:05.678Z"` | 同上                                            |
| `${Time:t3}`  | `2024-01-02T03:04:05.678Z`   | `"2024-01-02T03:04:05.678Z"` | 同上                                            |
| `${Time:lu}`  | `2024-01-02T03:04:05.678Z`   | `"2024-01-02T03:04:05.678Z"` | 同上                                            |
| `${Time:d}`   | `2024-01-02T03:04:05.678Z`   | `"2024-01-02T03:04:05.678Z"` | 类型动词只属于环境变量，被忽略，使用默认布局    |
| `${Time:f}`   | `2024-01-02T03:04:05.678Z`   | `"2024-01-02T03:04:05.678Z"` | 同上                                            |
| `${Time:t}`   | `2024-01-02T03:04:05.678Z`   | `"2024-01-02T03:04:05.678Z"` | 同上；单独的 `t` 是布尔类型动词，不是标题大小写 |
| `${Time:06d}` | `2024-01-02T03:04:05.678Z`   | `"2024-01-02T03:04:05.678Z"` | 带标志和宽度的类型动词同样被忽略                |

**时间戳。** 秒、毫秒、微秒、纳秒的 Unix 时间戳不是 `Time` 的格式，而是四个独立的占位符。
它们输出 `int64` 数字，文本和 JSON 中都不加引号，与 `Line`、`PID` 一样是数值：
任何动词和格式都被忽略，只有对齐有效；名称同样不区分大小写，`${timeunixmilli}` 也可以：

| 模板               | 含义 | 文本输出              | JSON 输出             |
|--------------------|------|-----------------------|-----------------------|
| `${TimeUnix}`      | 秒   | `1704164645`          | `1704164645`          |
| `${TimeUnixMilli}` | 毫秒 | `1704164645678`       | `1704164645678`       |
| `${TimeUnixMicro}` | 微秒 | `1704164645678901`    | `1704164645678901`    |
| `${TimeUnixNano}`  | 纳秒 | `1704164645678901234` | `1704164645678901234` |

**时区。** `Time` 的布局按记录自带的时区格式化，时间戳与时区无关。
下表的记录时间为 `2024-01-02 11:04:05.678 +08:00`：

| 模板                   | 文本输出                        | JSON 输出                         | 说明                                  |
|------------------------|---------------------------------|-----------------------------------|---------------------------------------|
| `${Time}`              | `2024-01-02T11:04:05.678+08:00` | `"2024-01-02T11:04:05.678+08:00"` | 按记录自带的时区格式化                |
| `${Time:15:04:05 MST}` | `11:04:05 CST`                  | `"11:04:05 CST"`                  | `MST` 输出时区缩写                    |
| `${TimeUnixMilli}`     | `1704164645678`                 | `1704164645678`                   | 时间戳与时区无关，和 UTC 记录的值相同 |

**缺少时间。** 记录的时间为零值时，`Time` 和四个时间戳占位符都按缺失处理：先回退占位符自己的名称对应的环境变量，
仍没有时文本省略占位值，JSON 省略对应成员，见「输出模板」。

```go
xslog.WithTemplate("[${Time}] ${Message:l}")
// [] Hello World
xslog.WithTemplate(`{"time":${Time},"message":${Message}}`)
// {"message":"Hello World"}
xslog.WithTemplate(`{"at":${TimeUnixMilli},"message":${Message}}`)
// {"message":"Hello World"}
```

JSON 中也可以直接选择时间戳，业务时间字段照常通过属性参数传入：

```go
log := slog.New(xslog.NewHandler(xslog.WithTemplate(
    `{"at":${TimeUnixMilli},"message":${Message}}`,
)))
log.Info("scheduled", slog.Time("deadline", deadline))
```

时间布局只影响 `Time` 占位符，不会改变另一个占位符选择的格式。业务属性中的时间使用默认的 RFC3339 毫秒格式，
`slog.Any` 结构体内部的时间遵循该值自身的 JSON 序列化规则。

### Level 支持的格式

`Level` 的值是 `slog.Level` 的名称：`DEBUG`、`INFO`、`WARN`、`ERROR`，以及在它们基础上加减偏移得到的自定义级别，
如 `INFO+2`、`DEBUG-4`。它总是有值，不会回退到环境变量；文本中默认不带引号，JSON 中是字符串。
`Level` 接受全部通用动词，并用宽度 N 取缩写；类型动词被忽略。下表的记录级别为 `INFO`：

| 模板           | 含义                         | 文本输出 | JSON 输出 |
|----------------|------------------------------|----------|-----------|
| `${Level}`     | 完整名称                     | `INFO`   | `"INFO"`  |
| `${Level:l}`   | 文本中本来不带引号，没有变化 | `INFO`   | `"INFO"`  |
| `${Level:j}`   | 文本中加引号                 | `"INFO"` | `"INFO"`  |
| `${Level:u}`   | 大写，级别名本来就是大写     | `INFO`   | `"INFO"`  |
| `${Level:w}`   | 小写                         | `info`   | `"info"`  |
| `${Level:t0}`  | 标题大小写                   | `Info`   | `"Info"`  |
| `${Level:u3}`  | 宽度 3 的缩写                | `INF`    | `"INF"`   |
| `${Level:w3}`  | 缩写转小写                   | `inf`    | `"inf"`   |
| `${Level:t3}`  | 缩写转标题大小写             | `Inf`    | `"Inf"`   |
| `${Level:lu3}` | 连写，与 `u3` 相同           | `INF`    | `"INF"`   |
| `${Level:ju3}` | 连写，文本中加引号           | `"INF"`  | `"INF"`   |
| `${Level:d}`   | 类型动词被忽略               | `INFO`   | `"INFO"`  |

宽度 N 为 1 至 4 时按下表取缩写，缩写与 Serilog 一致，不是简单截断：`WARN` 的 `u2` 是 `WN` 而不是 `WA`。
不写宽度、宽度为 0 或 5 及以上时使用完整名称：

| 格式                         | `DEBUG` | `INFO` | `WARN` | `ERROR` |
|------------------------------|---------|--------|--------|---------|
| `u1`                         | `D`     | `I`    | `W`    | `E`     |
| `w1`                         | `d`     | `i`    | `w`    | `e`     |
| `t1`                         | `D`     | `I`    | `W`    | `E`     |
| `u2`                         | `DE`    | `IN`   | `WN`   | `ER`    |
| `w2`                         | `de`    | `in`   | `wn`   | `er`    |
| `t2`                         | `De`    | `In`   | `Wn`   | `Er`    |
| `u3`                         | `DBG`   | `INF`  | `WRN`  | `ERR`   |
| `w3`                         | `dbg`   | `inf`  | `wrn`  | `err`   |
| `t3`                         | `Dbg`   | `Inf`  | `Wrn`  | `Err`   |
| `u4`                         | `DBUG`  | `INFO` | `WARN` | `EROR`  |
| `w4`                         | `dbug`  | `info` | `warn` | `eror`  |
| `t4`                         | `Dbug`  | `Info` | `Warn` | `Eror`  |
| 省略、`u`、`u0`、`u5` 及以上 | `DEBUG` | `INFO` | `WARN` | `ERROR` |
| `w`、`w0`、`w5` 及以上       | `debug` | `info` | `warn` | `error` |
| `t0`、`t5` 及以上            | `Debug` | `Info` | `Warn` | `Error` |

自定义级别没有缩写表，宽度 N 把名称截断到 N 个字符，再转换大小写；宽度不小于名称长度时是完整名称：

| 格式 | `INFO+2` | `DEBUG-4` | `ERROR+4` |
|------|----------|-----------|-----------|
| `u`  | `INFO+2` | `DEBUG-4` | `ERROR+4` |
| `w`  | `info+2` | `debug-4` | `error+4` |
| `t0` | `Info+2` | `Debug-4` | `Error+4` |
| `u3` | `INF`    | `DEB`     | `ERR`     |
| `w3` | `inf`    | `deb`     | `err`     |
| `t3` | `Inf`    | `Deb`     | `Err`     |
| `u5` | `INFO+`  | `DEBUG`   | `ERROR`   |
| `u6` | `INFO+2` | `DEBUG-`  | `ERROR+`  |

对齐在转换之后补白，适合让长短不一的级别对齐成列：

| 模板               | 文本输出   |
|--------------------|------------|
| `[${Level,-5}]`    | `[INFO ]`  |
| `[${Level,5}]`     | `[ INFO]`  |
| `[${Level,-5:w}]`  | `[info ]`  |
| `[${Level,6:u3}]`  | `[   INF]` |
| `[${Level,-6:w3}]` | `[inf   ]` |

### 环境变量的类型动词

环境变量的值是文本，默认按字符串编码：`${PORT}` 在 JSON 中输出 `"8080"`，在文本中输出 `"8080"`
（带引号，加 `:l` 去掉引号）。需要数字或布尔值时，用类型动词把环境变量文本解析为对应类型，
再按该类型编码。下表的环境变量为 `PORT=8080`、`RATIO=1.25`、`DEBUG=true`：

| 模板          | 文本输出 | JSON 输出 |
|---------------|----------|-----------|
| `${PORT}`     | `"8080"` | `"8080"`  |
| `${PORT:d}`   | `8080`   | `8080`    |
| `${PORT:l}`   | `8080`   | `"8080"`  |
| `${RATIO:f}`  | `1.25`   | `1.25`    |
| `${DEBUG}`    | `"true"` | `"true"`  |
| `${DEBUG:t}`  | `true`   | `true`    |
| `${DEBUG:t3}` | `"True"` | `"True"`  |

```go
// PORT=8080  RATIO=1.25  DEBUG=true
xslog.WithTemplate(`{"port":${PORT:d},"ratio":${RATIO:f},"debug":${DEBUG:t},"message":${Message}}`)
// {"port":8080,"ratio":1.25,"debug":true,"message":"listen on"}

xslog.WithTemplate("port=${PORT:d} ratio=${RATIO:f} debug=${DEBUG:t} ${Message:l}")
// port=8080 ratio=1.25 debug=true listen on
```

| 动词                    | 解析为 | 解析规则                                                                      |
|-------------------------|--------|-------------------------------------------------------------------------------|
| `d` `b` `o` `O` `c` `U` | 整数   | 十进制 `int64`，可带 `+`、`-`；超出范围时按 `uint64`                          |
| `e` `E` `f` `F` `g` `G` | 浮点数 | Go 浮点数语法，整数文本也可以；`NaN`、`Inf` 在 JSON 中输出为字符串            |
| `t`                     | 布尔值 | 真：`1` `t` `T` `TRUE` `true` `True`；假：`0` `f` `F` `FALSE` `false` `False` |

- **只选类型。** 动词之前可以写 fmt 的标志、宽度和精度，例如 `06d`、`.2f`，输出模板里它们不生效；
  `b`、`o`、`O`、`c`、`U` 也只表示整数，仍按十进制输出。需要补白时使用对齐，如 `${PORT,6:d}`。
- **按值编码。** 前导零和多余的小数位不保留：`007` 得到 `7`，`1.20` 得到 `1.2`。
  整数只按十进制解析，不接受 `0x`、`1_000` 和首尾空白。
- **无法解析时保持字符串。** 例如 `PORT=abc` 配 `:d`，输出 `"abc"`：原值不丢失，JSON 依然合法，
  字段类型随之变成字符串，便于发现配置错误。
- **`t` 与 `tN`。** 单独的 `t` 是布尔类型动词，带宽度的 `tN` 是标题大小写：
  `${DEBUG:t}` 得到布尔值 `true`，`${DEBUG:t3}` 得到字符串 `"True"`。
- **其他格式不改变类型。** `s`、`v`、`q`、`x`、`X`、`l`、`lj`、`j`、`u`、`w`、`t3`、`lu` 和时间布局
  都不会把环境变量变成数字。
- **只作用于环境变量回退值。** 内置元数据和业务属性保持自己的类型。调用信息缺失、`Line`
  回退到环境变量时，`${Line:d}` 按数值编码，`${Line}` 仍是字符串。
- **缺失照常省略。** 环境变量不存在时，类型动词不改变省略规则。

`Injector.Inject` 使用同一个动词管理器。它输出纯文本，因此还会按 fmt 规则套用标志、宽度和精度
（`${PORT:06d}` 得到 `008080`），无法解析时输出 fmt 的错误标记，如 `%!d(string=abc)`；
大小写动词转换字符串的大小写，其他类型按默认形式输出，连写中的引号动词被忽略（`lu` 等同于 `u`）；
时间值使用时间格式，不适用于时间的动词（引号动词、大小写动词和类型动词）同样沿用默认布局。

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

`Setup` 则配置进程的全局 Handler 并安装为默认 Logger，同时管理由 `WithFile` 打开的文件：

```go
if err := xslog.Setup(
    xslog.WithTemplate(`{"time":${TimeUnixMilli},"message":${Message}}`),
    xslog.WithFile("/var/log/game.log", xslog.WithMaxSizeMB(50)),
    xslog.WithLevel(slog.LevelInfo),
); err != nil {
    return err
}
defer xslog.Close()
xslog.SetLevel(slog.LevelDebug)
```

`WithLevel` 在这里确定初始阈值，后续用 `SetLevel` 修改全局 Handler 的级别。
重复 `Setup` 会先切换到新配置，再关闭旧文件；`Close` 可重复调用。
文件参数校验或打开失败时，`Setup` 返回错误，现有配置保持不变。

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

## 运行时调整

模板、级别、Writer 和错误回调都可以在运行时调整，不必重建 Logger。
`Handler` 的方法对该 Handler 及其 `With`、`WithGroup` 派生的实例立即生效：

```go
h := xslog.NewHandler(xslog.WithLevel(slog.LevelInfo))
log := slog.New(h).With("module", "gate")

h.SetLevel(slog.LevelDebug)
h.SetTemplate(`{"time":${TimeUnixMilli},"message":${Message}}`) // 文本与 JSON 可以互相切换
h.SetWriter(w)                                                    // nil 表示标准输出
h.SetErrorHandler(func(err error) { /* 记录或统计输出失败 */ })   // nil 表示不再通知
```

`Setup` 管理的全局 Handler 有对应的包级函数，作用于经 `slog.Default()` 派生的全部 Logger，
包括 `Setup` 之前派生的：

```go
xslog.SetLevel(slog.LevelWarn)
xslog.SetTemplate("${Time:15:04:05} ${Message:l}")
xslog.SetWriter(os.Stderr)
xslog.SetErrorHandler(nil)
```

语义如下：

- `SetTemplate` 的语法与 `WithTemplate` 相同，空模板恢复 `DefaultTemplate`。每条记录只读取一次模板，
  不会混用新旧模板；调用时已在渲染的记录仍使用旧模板。
- `SetLevel` 遇到经 `WithLevel` 传入的 `*slog.LevelVar` 时更新该变量，共享它的 Handler 同步变化；
  其他情况以固定级别取代原 Leveler。
- `SetWriter` 与写入互斥，返回后旧 Writer 不会再被调用，可以安全关闭；Handler 不会替调用方关闭它。
  `Setup` 通过 `WithFile` 打开的文件仍由 `Close` 关闭。
- 错误回调在写锁之外执行，可以在回调里调用上述任何方法；回调本身需要支持并发。
- 这些方法可并发调用。`Setup` 一次更新多项设置时逐项生效，此间并发的记录可能混用新旧设置，
  但每条记录都是完整的。

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

README「格式动词」一章中的示例表格由 `TestREADMEFormatExamples` 和 `TestREADMELevelExamples` 逐行渲染并核对，
占位符总表和正文规则由 `TestREADMEFormatMatrix`、`TestREADMEFormatRules` 核对，
修改动词行为或示例后，测试会提示同步更新文档。

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
