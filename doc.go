// Package xslog 基于 [log/slog] 提供由模板控制文本或 JSON 输出的 Handler。
// [NewHandler] 和 [Setup] 默认使用 [DefaultTemplate]，可通过 [WithTemplate] 设置模板。
// 空模板恢复默认值。
//
// 创建使用文本模板的 Logger：
//
//	slog.SetDefault(slog.New(xslog.NewHandler(xslog.WithTemplate(
//	    "${Time:15:04:05.000} [${Level:u3}] ${Message:l}",
//	))))
//
// 模板支持 %VAR% 和 ${VAR} 两种占位符；${Name[,alignment][:format]} 还支持对齐和格式。
// 配置时以带类型的示例值展开模板，结果是合法 JSON 对象时采用 JSON 编码，否则采用文本编码。
// 选定的编码不随记录或环境变量变化。JSON 占位符表示完整值，通常无需额外加引号：
//
//	xslog.NewHandler(xslog.WithTemplate(
//	    `{"time":${Time:unixMilli},"message":${Message}}`,
//	))
//
// 时间格式写在对应占位符中，可用 Go 时间布局或区分大小写的 unix、unixMilli、unixMicro、unixNano。
// 调用信息通过 Source、File、FileName、Line、Function、Package 读取，并按程序计数器缓存。
// File 保留运行时文件路径，Source 使用缩短的目录/文件名:行号。
// PID、Hostname、Executable 在创建 Handler 时读取，派生 Handler 复用同一份快照。
// Line 和 PID 的元数据在 JSON 中为数字，环境变量回退值按字符串处理。
//
// 内置占位符名称不区分大小写；元数据缺失时，按占位符原名回退系统环境变量。
// 空环境变量算作存在，两处均缺失时，文本省略占位值，JSON 省略对应成员或数组元素。
// 占位符不查找业务属性，业务属性通过 Logger 的 With、WithGroup 及日志调用参数传入。
// 文本字段使用 key=value，普通单行字符串带引号，分组展开为点分键名，多行字段写为对齐的竖线块。
// JSON 字段保留对象嵌套；直接传入的字符串去掉末尾 CR/LF 后仍有多行内容时，按行展开为字符串数组。
// 业务字段及普通环境变量中的完整 JSON 对象或数组文本优先作为结构化值编码。
// 复合值使用自身的 JSON 序列化规则，内部字符串不递归展开为数组。
//
// [Handler] 管理属性、动态设置和并发写入；完整渲染后在派生实例共享的锁下调用一次 Writer。
// 模板、级别、Writer 和错误回调可通过 [Handler.SetTemplate]、[Handler.SetLevel]、
// [Handler.SetWriter]、[Handler.SetErrorHandler] 在运行时调整，对已派生的实例同步生效。
// 记录末尾缺少 LF 时自动补齐换行。NewLine 占位符可显式插入 LF，控制文本布局或 JSON 空白。
//
// [Setup] 配置进程的全局 Handler 并安装为默认 Logger，可通过 [WithFile] 管理轮转文件，
// 之后可用 [SetLevel]、[SetWriter]、[SetErrorHandler]、[SetTemplate] 动态调整。
// 此前经默认 Logger 派生的 Logger 与全局 Handler 共享设置，随之更新。
// 文件配置或打开失败时保留当前配置，[Close] 释放 Setup 持有的文件。
// 独立创建的 Handler 使用调用方提供的 Writer 和 Leveler；[OpenFile] 可供调用方自行管理文件生命周期。
//
// 编码器转义不可打印字符。属性值无法序列化、存在分组循环、超出解析限制，
// 或其文本、序列化方法发生可恢复的 panic 时，使用诊断文本保留记录的其他字段。
// 渲染或写入错误由 Handle 返回，也可通过 [WithErrorHandler] 观察；渲染失败时不写出记录。
// Writer 短写返回 io.ErrShortWrite。Writer 和错误回调的 panic 向调用方传播，写入锁仍会释放。
package xslog
