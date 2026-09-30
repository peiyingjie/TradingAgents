# Go 迁移验收记录（2026-09-30）

结论：**上一轮列出的五类功能缺口已补齐；Go 全量测试、离线冒烟和 Windows 终端交互验证通过。**
本记录已更新到 2026-09-30 15:16 的执行结果。Go 使用原生终端界面，视觉排版并非 Rich 的逐像素复制；
测试通过也不代表所有线上响应或全部边界条件已经穷尽。

本次以仓库中保留的 Python 源码为基准，核对模块、CLI 与运行入口，并执行 Go 全量检查。
没有修改交易逻辑、Python 基准源码或真实 `.env`；模型和数据冒烟使用本地 HTTP 响应及假凭据。
测试通过不能替代未实现功能的验收，也不能证明所有服务端响应都已覆盖。

## 实际执行结果

环境：Windows amd64、Go 1.25.0；竞态检测使用仓库旁独立安装的 LLVM-MinGW。
复跑入口：`./scripts/test-go-offline.ps1`，可通过 `-Compiler` 指定兼容的 C 编译器。

| 检查 | 结果 |
|---|---|
| `go build ./...` | 通过 |
| `go test -count=1 -timeout=5m -coverprofile=reports/go-audit/coverage.out -json ./...` | 52 个顶层测试，含子测试 170 项全部通过；失败 0、跳过 0 |
| `go test -race -count=1 -timeout=5m -json ./...` | 同一组 170 项通过，无竞态报告 |
| `go vet ./...` | 通过 |
| 构建独立 `bin/tradingagents.exe` | 通过 |
| 独立二进制 `--help`、`backtest --help` | 均退出 0 |
| Go 语句覆盖率 | 71.1%（不是迁移完成百分比） |
| Windows ConPTY 真实终端测试 | 方向键、多选、中文输入、Esc、Ctrl-C、上下文取消、模式恢复和实时面板通过 |
| Linux / macOS CLI 测试程序交叉编译 | 均通过；未在目标操作系统执行 |

15 个库包有测试；`cmd/tradingagents` 无独立单元测试，其二进制入口通过上述启动检查。
CLI、回测包自身覆盖率分别为 70.0%、38.6%，尚有交互和错误分支未覆盖。
端到端 CLI 测试在进程内调用实际 CLI 入口；未对打包后的二进制执行完整交互交易链。

本地原始证据位于 `reports/go-audit/`（已被 Git 忽略）：

- `summary.json`：执行状态、工具链、测试计数和覆盖率。
- `test.log`、`race.log`：逐项 JSON 测试事件，均使用 `-count=1` 禁用结果缓存。
- `coverage.out`、`coverage.log`：语句覆盖数据及函数统计。
- `build.log`、`vet.log`、`binary.log`、`cli-help.log`、`backtest-help.log`：其他检查结果。

脚本只在自身进程替换凭据、清除环境配置覆盖，并在结束时恢复；不打印原始密钥，不写真实 `.env`。
离线指业务 HTTP 使用模拟响应；首次获取 Go 依赖仍可能需要网络。

## 冒烟和行为对照范围

| 范围 | 已通过的验证 |
|---|---|
| 模型工厂 | 注册表中全部 20 个 provider 的创建、请求路径、认证头、文本响应与 token usage |
| 原生协议 | OpenAI Responses 分支、Bedrock AWS SigV4 签名额外冒烟；既有测试覆盖工具调用、原生内容重放、错误脱敏与取消 |
| 数据供应商 | 全部 23 条已登记 method/vendor 路由，加 StockTwits、Reddit、verified snapshot，共 26 个子测试；新增数据冒烟拒绝非本地目标 |
| 交互分析 | 配置读取、行情工具调用、隐藏分析日期、结构化决策、token 统计、报告与记忆落盘 |
| CLI 回测 | 实际 `backtest` 入口运行一个交易日单元；再次执行跳过已有单元、不重复调用模型、不污染实时记忆 |
| Agent 与完整 Graph | 36 组 Python Agent 消息/状态基准，4 组完整工作流的节点顺序、模型输入、工具、schema 和最终状态对照 |
| 状态与执行 | 消息归并、路由、辩论循环、递归限制、取消、工具参数和并发结果顺序 |
| 持久化 | Python version 1 SQLite 检查点兼容、中断恢复不重复节点、成功清理、报告目录、持仓指纹与记忆 |
| 历史数据与结算 | 13 个 stockstats 指标的 230 行基准、6 组 SEC 披露时点、快照/CSV/复权字段、禁止未来信息、完整持有期结算、未到期保持 pending |

新增或扩展的测试：

- `internal/llm/smoke_test.go`
- `internal/dataflows/smoke_test.go`
- `internal/cli/integration_test.go` 的 `backtest_and_resume`
- `internal/cli/remote_test.go`、`parity_test.go`、`console_windows_test.go`
- `internal/graph/analyst_timing_test.go`、`internal/llm/validators_test.go`

Go 源码扫描未发现 Python 子进程调用或未实现占位符；构建、测试和程序运行使用 Go 代码及已保存的参考资产。
开发期参考资产导出脚本仍需要 Python，这不属于 Go 运行时依赖。Python 源码继续保留。

## 上一轮缺口的关闭情况

| 原缺口 | Python 证据 | 当前 Go 实现与验证 |
|---|---|---|
| OpenRouter 动态模型列表 | `cli/utils.py:221`、`:255` | `internal/cli/remote.go`：10 秒请求预算、最新优先、主流前五项、别名排除、无主流时全量回退、自定义模型与失败回退；已对照 Python 捕获选项 |
| CLI 公告 | `cli/announcements.py:10`、`:31`，调用点 `cli/main.py:534` | `internal/cli/remote.go`：1 秒超时、缺省公告、失败回退、显示与 require_attention 等待 Enter；HTTP 错误、无公告和注意提示已测试 |
| 分析师耗时跟踪 | `tradingagents/graph/analyst_execution.py:76`，调用点 `cli/main.py:1062` | `internal/graph/analyst_timing.go`：首次开始/完成、非负耗时、恢复后的已完成报告、顺序汇总；已对照 Python 逐事件结果 |
| 未知模型提示 | `tradingagents/llm_clients/validators.py:20`、`base_client.py:42` | `internal/llm/validators.go`：从 Python 导出的已知/历史模型、开放模型供应商例外、警告后继续；已核对 Python 校验结果 |
| CLI 展示和操作 | `cli/main.py`、`cli/utils.py` | Go 原生方向键单选、Space/a 多选、Esc/Ctrl-C、中文输入；实时状态/消息/工具/报告/计数面板；Windows ConPTY 实际按键测试通过 |

同时补齐地区端点选择、语言/思考等级菜单、Azure 非空部署名、自定义模型非空校验，以及分析后的保存路径和全文查看提示。
分析师选择按 Python CLI 的固定顺序去重，加密资产过滤 fundamentals；Graph 的程序化调用仍保留调用方所选执行顺序。
报告保存现在由用户选择，默认目录为 `results_dir/reports/<ticker>_<timestamp>`，不再无条件写入 ticker/date 报告目录。
使用管道输入的脚本需要提供新增的保存/显示回答。已有集成测试确认模型调用次数、记忆和回测跳过行为没有改变。

此外，Python `TradingAgentsGraph` 的部分公开辅助方法在 Go 中被合并进 `Propagate`、`InitialState` 或下层模块；
相关核心行为已有测试，但这不是逐一对应的公共库 API。外部程序接入时需按 Go 接口适配。

## 验收边界

本次未使用真实 API key，因此未验证真实账户权限、模型可用性、供应商当前响应变化、限流或公网连接。
没有运行全套 Python 测试；原有业务基准保留，另用 `scripts/capture_go_cli_reference.py` 从真实 Python 捕获 CLI 辅助行为。
`scripts/export_go_reference.py` 也已包含已知模型列表的导出，Go 构建/运行不依赖这些 Python 脚本。
Linux/macOS 只做了 CLI 测试程序交叉编译，没有目标系统实际运行验证；本次没有触发远端 CI。

当前可标记为“已列出的功能缺口关闭，离线验收通过”。终端外观、Go 公共库调用方式和真实服务联调属于上面说明的验收边界。
