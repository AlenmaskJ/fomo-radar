# FomoRadar

FomoRadar 是一个基于 Go 和 SQLite 的本地多市场机会扫描系统。项目目前覆盖 BSC / Solana 新币、主流加密资产和美股永续合约，提供候选发现、可解释评分、风险过滤、交易计划、Web 看板、历史统计和 Signal Lab 效果验证。

> 本项目仅用于技术研究与演示，不构成投资建议，不连接交易账户，也不会自动下单。

![FomoRadar 新币雷达](docs/dashboard-phase1-refinement-radar.png)

## 当前能力

| 模块 | 当前实现 |
| --- | --- |
| 新币雷达 | 从 GeckoTerminal 和 DexScreener 发现 BSC / Solana 候选，执行 Fast / Deep 两阶段扫描 |
| 主流币雷达 | 使用 OKX 公共行情和多周期 K 线构建动态候选池，结合价格、成交量、持仓量、资金费率和 BTC 基准评分 |
| 美股雷达 | 从静态审阅白名单和 OKX 股票永续合约中筛选候选，使用 SPY 作为市场基准 |
| 交易计划 | 根据支撑阻力、ATR 和 tick size 生成分批入场、止损与目标位 |
| Web 看板 | 展示新币、主流币、美股、代币详情、扫描状态和历史榜单 |
| Signal Lab | 跟踪信号后的未来收益、MFE 和 MAE，生成阈值与版本对比报告 |
| Champion / Challenger | 保存不可变历史评分，支持 Challenger 注册、对比、停用和人工审计晋升 |

核心实现特性：

- 新币 Fast 结果先发布，Deep 数据由 4 个 worker、容量为 64 的有界队列异步补充。
- Deep 结果写入前校验基础 Fast 快照，过期任务标记为 `stale`，不能覆盖新证据。
- 评分同时保留原始分数、证据可信度和最终等级；证据不足时限制展示等级。
- 市场扫描区分 `completed`、`degraded` 和 `failed`；失败运行不会覆盖上一份完整结果。
- 主流币与美股共用分析代码，但候选池、K 线缓存、扫描状态和结果相互隔离。
- SQLite 启用 WAL、外键和 `busy_timeout`；Web 模板和静态资源直接嵌入 Go 二进制。

## 架构

```text
GeckoTerminal / DexScreener               OKX 公共行情 / K 线
             │                                      │
             ▼                                      ▼
       候选发现与补全                      动态币池 / 美股池
             │                                      │
       Fast Scan ──► Deep Queue              多周期指标与交易计划
             │                                      │
             └──────────────────┬───────────────────┘
                                ▼
                              SQLite
                         ┌──────┴──────┐
                         ▼             ▼
                    Web 看板       Signal Lab
                                       │
                                       ▼
                            Champion / Challenger
```

## 技术栈

- Go 1.26+
- SQLite（`modernc.org/sqlite`，纯 Go 驱动）
- Go `net/http`、`html/template`、goroutine、channel
- GeckoTerminal、DexScreener、OKX 公共 API
- 原生 HTML、CSS 和 JavaScript，无 Node.js 构建链

## 快速开始

### 1. 获取源码

```bash
git clone https://github.com/AlenmaskJ/fomo-radar.git
cd fomo-radar
```

### 2. 验证项目

```bash
go test -count=1 ./...
go vet ./...
```

### 3. 构建

Windows PowerShell：

```powershell
go build -trimpath -o fomo-scanner.exe ./cmd/fomo
go build -trimpath -o fomo-server.exe ./cmd/fomo-server
go build -trimpath -o fomo-market.exe ./cmd/fomo-market
go build -trimpath -o fomo-lab.exe ./cmd/fomo-lab
go build -trimpath -o fomo-score.exe ./cmd/fomo-score
```

Linux / macOS：

```bash
go build -trimpath -o fomo-scanner ./cmd/fomo
go build -trimpath -o fomo-server ./cmd/fomo-server
go build -trimpath -o fomo-market ./cmd/fomo-market
go build -trimpath -o fomo-lab ./cmd/fomo-lab
go build -trimpath -o fomo-score ./cmd/fomo-score
```

### 4. 启动完整本地看板

不需要代理时，在两个 PowerShell 终端中分别运行：

```powershell
# 终端一：Web 看板、主流币雷达和美股雷达
.\fomo-server.exe -serve -market-watch -db .\fomo.db -listen 127.0.0.1:18080
```

```powershell
# 终端二：新币持续扫描
$env:FOMO_DB = ".\fomo.db"
.\fomo-scanner.exe -watch
```

需要本地代理时可使用一键脚本：

```powershell
$env:FOMO_PROXY = "http://127.0.0.1:10808"
.\start-demo.cmd
```

`start-demo.cmd` 当前默认使用 `http://127.0.0.1:10808`，启动 `fomo-server.exe` 和 `fomo-scanner.exe`，等待主流币首次扫描完成后打开 `/market`。没有代理服务时请使用上面的手动启动方式。

### 5. 打开页面

| 页面 | 地址 |
| --- | --- |
| 新币雷达 | <http://127.0.0.1:18080/> |
| 主流币雷达 | <http://127.0.0.1:18080/market> |
| 主流币历史榜 | <http://127.0.0.1:18080/market/history> |
| 美股雷达 | <http://127.0.0.1:18080/stocks> |
| 美股历史榜 | <http://127.0.0.1:18080/stocks/history> |

## 可执行程序

| 程序 | 用途 | 主要模式 |
| --- | --- | --- |
| `fomo-scanner` | BSC / Solana 新币扫描 | `-scan`、`-watch` |
| `fomo-server` | Web 看板及内置市场调度器 | `-serve`、可选 `-market-watch` |
| `fomo-market` | 命令行主流币机会扫描 | `-scan`、`-watch` |
| `fomo-lab` | 信号归档、成熟结果计算和报告 | `-run-once`、`-watch`、`-report` |
| `fomo-score` | 评分版本和角色管理 | `list`、`add`、`compare`、`promote`、`disable` |

常用命令：

```bash
# 新币完整扫描
./fomo-scanner -scan

# 主流币单次扫描
./fomo-market -scan -db ./fomo.db

# 更新并查看 Signal Lab
./fomo-lab -run-once -source-db ./fomo.db -lab-db ./fomo-lab.db
./fomo-lab -report -lab-db ./fomo-lab.db

# 查看 Champion / Challenger
./fomo-score list --db ./fomo.db
./fomo-score compare --db ./fomo.db --lab-db ./fomo-lab.db
```

完整的 Challenger 注册、晋升和回滚流程见[核心扫描器运维说明](docs/operations/core-scanner.md)。

## 配置

| 环境变量 | 默认值 | 用途 |
| --- | --- | --- |
| `FOMO_DB` | `./fomo.db` | Core SQLite 数据库路径 |
| `FOMO_LAB_DB` | `./fomo-lab.db` | Signal Lab 数据库路径 |
| `FOMO_HTTP_ADDR` | `127.0.0.1:8080` | Web 服务监听地址 |
| `FOMO_PROXY` | 空 | `fomo-server`、`fomo-market` 的显式 HTTP/HTTPS 代理 |
| `HTTP_PROXY` / `HTTPS_PROXY` | 继承系统环境 | 新币 Provider 使用的标准 Go HTTP 代理 |
| `FOMO_DEEP_SCAN_THRESHOLD` | `40` | 进入 Deep 扫描的最低分数，范围 `0..100` |

命令行参数优先于对应环境变量。Web 演示命令使用 `127.0.0.1:18080`，与程序自身的默认监听端口 `8080` 不同。

## Web 路由与请求边界

| 路由 | 方法 | 用途 |
| --- | --- | --- |
| `/`、`/token/{address}`、`/api/tokens` | `GET` | 新币列表、详情和 JSON 数据 |
| `/market`、`/market/history` | `GET` | 主流币当前机会与历史榜单 |
| `/stocks`、`/stocks/history` | `GET` | 美股当前机会与历史榜单 |
| `/api/market/status`、`/api/stocks/status` | `GET` | 市场扫描状态 |
| `/api/market/scan`、`/api/stocks/scan` | `POST` | 手动触发扫描，带 60 秒冷却 |
| `/health/market`、`/health/stocks` | `GET` | 最近完整结果健康检查 |

项目没有用户登录、Session、JWT 或 Bearer Token。默认监听回环地址；手动扫描接口要求 `application/json`，并校验同源 `Origin` 或回环来源。这是本地访问边界，不是公网认证方案。

## 数据与安全边界

- 只调用公开市场接口，不需要交易所 API Key。
- 不读取账户、余额、仓位或订单，不调用下单接口。
- 本地数据库、运行报告、`.env` 和编译产物已通过 `.gitignore` 排除。
- 若改为 `0.0.0.0` 或通过反向代理公开服务，应在外层增加身份认证、TLS 和访问控制。
- 免费数据源可能存在延迟、限流、缺失或短时不可用；程序会保留降级状态和错误摘要。
- 美股雷达读取的是股票永续合约，不等同于美国现货市场；休市期间可能出现额外流动性和价格偏差。

## 项目结构

```text
cmd/                    五个可执行程序入口
internal/providers/     GeckoTerminal、DexScreener、OKX 数据适配
internal/scanner/       新币发现、Fast / Deep 扫描与过滤
internal/score/         可解释评分和版本集合
internal/markets/       多周期指标、机会评分与交易计划
internal/marketwatch/   主流币 / 美股动态候选池与调度
internal/store/         Core SQLite 存储和迁移
internal/lab/           独立 Signal Lab 数据库与效果分析
internal/web/           Web 路由、模板、静态资源和只读查询
tests/fixtures/         Provider 与评分测试数据
docs/                   运维说明、设计记录和界面截图
```

## 更多文档

- [动态机会雷达使用说明](docs/operations/dynamic-market-radar.md)
- [核心扫描器运维说明](docs/operations/core-scanner.md)
- [Dashboard Phase 1 设计](docs/plan2-dashboard-design.md)
- [Signal Lab 实施报告](docs/plan3-signal-lab-implementation-report.md)
