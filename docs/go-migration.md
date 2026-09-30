# Python → Go 迁移

Go 入口为 `cmd/tradingagents`，模块名为 `tradingagents`，要求 Go 1.25 或更新版本。
原 Python 源码保留，用作行为基准。Go 程序通过 HTTP 调用原有供应商；不启动
Python 子进程，也不需要 Python SDK、LangGraph 或 LangChain。

默认安装与容器入口现已切换为 Go：在仓库根目录执行 `go install ./cmd/tradingagents`，
将 Go 的安装目录加入 PATH 后使用 `tradingagents`。Docker 使用 Go 多阶段构建，运行镜像
不包含 Python，继续使用 `/home/appuser/.tradingagents` 数据卷。
保留的 Python 包通过 `pip install .` 安装后只提供 `tradingagents-python` 命令；
`python -m cli.main` 仍可用于基准开发。旧环境需重新安装或卸载 Python 包以移除原同名入口，
再安装 Go CLI，并检查 PATH 中实际命中的可执行文件。

当前验收结果见 [迁移审计记录](go-migration-audit.md)。已补齐动态模型列表、公告、耗时统计、
模型警告和终端交互。可用 `./scripts/test-go-offline.ps1` 使用假凭据复跑全量检查并生成本地审计日志。

## 实际架构与模块对应

当前 Python 代码已使用自研串行 Graph Runtime。分析师按所选顺序执行，分析师内部
通过消息中的 tool calls 循环；随后进入研究辩论、研究经理、交易员、风险辩论、投资组合经理。
同一批工具可以并发，但返回结果保持原调用顺序。

| Python 源码 | Go 实现 | 保留的行为 |
|---|---|---|
| `default_config.py`、`dataflows/config.py` | `internal/config` | 默认配置、环境变量、供应商路由配置 |
| `runtime/messages.py` | `pkg/model` | 消息、工具调用、原生内容块、usage、按 ID 增删替换 |
| `agents/utils/agent_states.py`、`graph/propagation.py` | `internal/state` | 强类型 State 与可选 Update；消息归并、其他字段覆盖 |
| `runtime/graph.py`、`graph/conditional_logic.py` | `internal/runtime`、`internal/graph/setup.go` | 串行节点、条件路由、递归限制、取消、状态流 |
| `runtime/checkpoint.py` | `internal/runtime/checkpoint.go` | SQLite version 1、state/next-node 原子保存与恢复 |
| `runtime/tools.py` | `internal/tools` | 工具 schema、校验、隐藏状态注入、并发有序结果 |
| `runtime/callbacks.py` | `internal/callbacks` | 上下文继承的 LLM/tool 开始、结束、异常通知 |
| `llm_clients/` | `internal/llm` | Chat/Responses、Anthropic、Gemini、Azure、Bedrock 及兼容端点 |
| `agents/analysts/` | `internal/agents` | market、social、news、fundamentals |
| `agents/researchers/`、`agents/managers/` | `internal/agents` | bull、bear、research manager、portfolio manager |
| `agents/trader/`、`agents/risk_mgmt/` | `internal/agents` | trader、aggressive、conservative、neutral |
| `agents/schemas.py` | `internal/agents/schemas.go` | 结构化决策校验与 Markdown；失败后一次自由文本回退 |
| `dataflows/`、`agents/utils/*_tools.py` | `internal/dataflows` | Yahoo、Alpha Vantage、FRED、SEC EDGAR、Polymarket、StockTwits、Reddit |
| `agents/utils/memory.py`、`rating.py` | `internal/memory` | 决策日志、收益结算、反思、历史可见性、评级解析 |
| `portfolio.py` | `internal/portfolio` | 持仓上下文与检查点指纹 |
| `graph/trading_graph.py` | `internal/graph/trading_graph.go` | 完整入口、身份信息、记忆、持仓、检查点、最终信号 |
| `reporting.py` | `internal/reporting` | 分节报告目录、完整 Markdown、状态 JSON |
| `backtest.py` | `internal/backtest` | ticker/date 独立单元、跳过已记录单元、结算和评级摘要 |
| `cli/` | `internal/cli`、`cmd/tradingagents` | 交互分析与 backtest 子命令 |

没有增加 Agent、指标、数据供应商、下单、持仓模拟、任务队列或分布式执行。
历史工具参数按分析日期截断；FRED vintage、SEC filed date、未归档实时数据的
withheld/unavailable 语义和记忆结果的可见日期分别处理。

## 运行

在包含 `go.mod` 的目录执行：

```sh
go run ./cmd/tradingagents --help
go run ./cmd/tradingagents
go run ./cmd/tradingagents --checkpoint --portfolio portfolio.json
go run ./cmd/tradingagents backtest AAPL,MSFT --start 2025-01-01 --end 2025-02-01 --every 7 --run-id sample
go build -o bin/tradingagents.exe ./cmd/tradingagents
```

仍使用 `.env` 和原有环境变量，例如 `OPENAI_API_KEY`、`ANTHROPIC_API_KEY`、
`GOOGLE_API_KEY`、`FRED_API_KEY`、`ALPHA_VANTAGE_API_KEY`、
`TRADINGAGENTS_LLM_PROVIDER`、`TRADINGAGENTS_QUICK_THINK_LLM`、
`TRADINGAGENTS_DEEP_THINK_LLM`、`TRADINGAGENTS_LLM_BACKEND_URL`。
Bedrock 支持 bearer token 以及 AWS 默认凭据链。

默认日志、缓存、记忆和 CLI 偏好位于 `~/.tradingagents/`。可用
`TRADINGAGENTS_RESULTS_DIR`、`TRADINGAGENTS_CACHE_DIR` 和
`TRADINGAGENTS_MEMORY_LOG_PATH` 指定目录。回测日志隔离在
`results_dir/backtest/<run-id>/trading_memory.md`。

Go CLI 在真实终端使用原生方向键菜单、分析师多选和实时进度面板；Esc/Ctrl-C 可取消必填选择，
语言选择取消时回到 English。非终端输入保留逐行模式。布局与 Rich 不同，不需要 Python UI 库。
分析完成后提示是否保存报告、保存路径及是否显示全文；默认保存目录为
`results_dir/reports/<ticker>_<timestamp>`。管道调用需提供这些后续回答。

## 验证

```sh
go build ./...
go test ./...
go test -race ./...
go vet ./...
```

测试不调用外部收费模型或实时数据。覆盖包括：

- 36 组由真实 Python Agent 工厂捕获的消息和状态更新，含中文、股票、加密资产、缺失报告、结构化回退。
- 四组完整 Python 工作流，比较节点顺序、所有模型输入、工具绑定、结构化 schema 名称和最终状态；包含 0/1/2 轮辩论及不同分析师顺序。
- 13 个现有 stockstats 指标的 230 行数值对照、完整市场快照、股票 CSV 精度和分红/拆股、六组 SEC 历史披露输出。CSV 对照仅统一操作系统换行符。
- 持仓文本与指纹、记忆文件与历史上下文、回测摘要、完整报告目录逐项对照。
- HTTP provider 请求/响应、原生推理签名重放、错误脱敏、取消与生命周期通知；10 组已有 Python provider 请求基准。
- Python version 1 检查点读取与签名元数据往返；SQLite 中断恢复、成功清理、已完成节点不重复执行；持有期不足保持 pending、未来结果不进入历史记忆。
- 工具并发有序结果、隐藏日期注入、数值参数转换、供应商回退、无数据与不可用信号、日期窗口限制。
- 回测日志隔离、跳过已有单元、单元失败后继续、取消和 CLI help。
- 完整交互 CLI 的本地 HTTP 端到端测试：环境配置、行情工具、隐藏日期截断、结构化决策、token 计数、记忆与报告落盘。
- OpenRouter 动态模型筛选、公告失败回退与注意提示、地区菜单、取消、报告保存/显示和分析师耗时；新增 Python CLI 辅助行为对照。
- Windows ConPTY 真实终端按键测试，包含中文输入、模式恢复及实时面板；纳入普通测试与竞态检测。

后续行为核对还修正了以下差异：

- Azure 使用 `AZURE_OPENAI_DEPLOYMENT_NAME` 选择部署，保留独立的模型名；其 token 参数透传行为与 Python AzureClient 一致。
- Anthropic 保持 Python 适配器显式禁用整体 HTTP 超时的设置，仍接受调用上下文取消。
- 行情缓存支持 Python 使用的日期列别名和 NaN；先截断未来日期、剔除无收盘价行，再对有效行前向和后向填充价格与成交量。
- 验证快照独立按日期排序，数据陈旧判断使用最大日期，避免倒序缓存误判。
- Yahoo 429 保留首次请求加三次重试，等待 2/4/8 秒，等待期间可取消。
- 持仓接受 Python 支持的数字字符串，拒绝必填字段缺失或 null；指纹保留 Pydantic 的小数、科学计数与负零格式。

真实 API 的凭据、账户权限、限流和即时服务可用性没有通过离线测试验证。
Go 的普通错误文本与 Python 异常堆栈不同；测试证明上述覆盖场景一致，不代表所有外部接口的所有响应都已穷尽。

### Windows 竞态检测

本机原有 MinGW GCC 8.1.0 会使 `-race` 测试在启动时以 `0xc0000139` 退出。
按 [Go 的 Windows race 工具链要求](https://go.dev/doc/articles/race_detector#Requirements)，
使用了独立下载的 LLVM-MinGW，位置在仓库旁
`../.go-toolchain/llvm-mingw-20260922-ucrt-x86_64/`；系统 PATH 和 Go 全局配置未修改。
这仅用于本地构建验证，不是 Go 程序的运行时依赖。

```powershell
./scripts/validate-go.ps1
# 或指定自己的兼容 C 编译器：
./scripts/validate-go.ps1 -Compiler C:/path/to/clang.exe
```

脚本仅在自身进程中设置 `CC` 和 PATH，依次运行四项检查与两个 CLI help，结束后恢复环境。
Linux CI 使用 `.github/workflows/go.yml` 运行相同检查。

## 更新参考资产

这些脚本只供开发期间生成基准；Go 编译、测试、运行均直接使用已保存的 JSON 与文本：

```sh
python scripts/export_go_reference.py
python scripts/capture_go_agent_reference.py
python scripts/capture_go_behavior_reference.py
python scripts/capture_go_cli_reference.py
```

它们必须在保留的 Python 项目及其开发依赖环境中运行。Prompt 通过 AST 提取，
模板仅替换原动态变量，固定文本不改写。指标、记忆、报告与工作流基准调用真实 Python
实现，并替换外部网络或模型响应；不要手工改期望结果来掩盖差异。
