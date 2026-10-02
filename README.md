# FomoRadar

FomoRadar 是一个基于 Go 和 SQLite 的多市场机会扫描系统。它聚合链上数据与 OKX 公共行情，对 BSC / Solana 新币、主流加密资产和美股永续合约进行实时筛选、可解释评分、风险控制与历史效果验证。

> 项目仅用于技术研究与演示，不构成投资建议，不连接交易账户，也不会自动下单。

![FomoRadar 新币雷达](docs/dashboard-phase1-refinement-radar.png)

## 核心功能

- **新币雷达**：发现 BSC、Solana 新池子，综合买家增速、成交量增速、买卖压力、参与者增长和流动性质量进行评分。
- **Fast / Deep 两阶段扫描**：快速证据先发布，详细交易数据由有界工作池异步补充。
- **主流币机会雷达**：结合多周期技术指标、成交量、持仓量、资金费率和 BTC 市场环境筛选机会。
- **美股机会雷达**：从 OKX 股票永续合约中筛选美国上市个股，使用 SPY 作为市场基准。
- **交易计划**：基于支撑阻力、ATR 和 tick size 生成分批入场、止损与目标位。
- **历史榜单**：按机会轮次统计上榜率、出现次数和区间收益。
- **Signal Lab**：跟踪评分后的未来收益、MFE 和 MAE，支持 Champion / Challenger 影子评分与人工晋升。

## 技术亮点

- Deep Queue 默认使用 4 个 worker 和容量为 64 的有界队列，避免任务与 goroutine 无限增长。
- Deep 结果写入前校验其依赖的 Fast 快照版本，旧任务标记为 `stale`，不能覆盖新结果。
- 评分同时保留原始分数、证据可信度和最终等级，数据不足时限制等级，避免少量证据产生虚假高分。
- 扫描状态区分 `completed`、`degraded` 和 `failed`；失败运行不会覆盖上一份完整结果。
- SQLite 启用 WAL、外键和 `busy_timeout`，适合单机及轻量 VPS 部署。
- Web 页面使用 Go 标准库模板和嵌入式静态资源，无需 Node.js 前端构建链。

## 架构

```text
GeckoTerminal / DexScreener
          │
          ▼
  Fast Scan ──► Deep Queue ──► 新币评分
          │
          ▼
        SQLite ◄── OKX ──► 动态候选池 ──► 多周期指标与交易计划
          │
          ├──► Web 看板 / 历史榜单
          └──► Signal Lab / Champion-Challenger
```

## 技术栈

- Go 1.26+
- SQLite（`modernc.org/sqlite`，纯 Go 驱动）
- Go `net/http`、`html/template`、goroutine、channel
- GeckoTerminal、DexScreener、OKX 公共 API

## 快速开始

### 1. 获取源码

```bash
git clone https://github.com/<your-name>/fomo-radar.git
cd fomo-radar
```

### 2. 运行测试

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

### 4. 启动 Web 看板与扫描器

Windows 可在完成上述构建后运行：

```powershell
.\start-demo.cmd
```

也可以分别启动：

```powershell
.\fomo-server.exe -serve -market-watch -db fomo.db -listen 127.0.0.1:18080
.\fomo-scanner.exe -watch
```

如果访问 OKX 需要代理：

```powershell
$env:FOMO_PROXY = "http://127.0.0.1:10808"
.\fomo-server.exe -serve -market-watch -db fomo.db -proxy $env:FOMO_PROXY -listen 127.0.0.1:18080
```

打开以下页面：

- 新币雷达：<http://127.0.0.1:18080/>
- 主流币雷达：<http://127.0.0.1:18080/market>
- 主流币历史榜：<http://127.0.0.1:18080/market/history>
- 美股雷达：<http://127.0.0.1:18080/stocks>
- 美股历史记录：<http://127.0.0.1:18080/stocks/history>

## 常用命令

```bash
# 新币完整扫描
./fomo-scanner -scan

# 新币持续扫描
./fomo-scanner -watch

# 更新 Signal Lab
./fomo-lab -run-once -source-db ./fomo.db -lab-db ./fomo-lab.db

# 查看 Signal Lab 报告
./fomo-lab -report -lab-db ./fomo-lab.db

# 查看评分版本
./fomo-score list --db ./fomo.db
```

## 项目结构

```text
cmd/                    可执行程序入口
internal/providers/     外部行情与链上数据适配
internal/scanner/       新币发现、Fast/Deep 扫描与过滤
internal/score/         新币评分和版本管理
internal/markets/       指标、机会评分与交易计划
internal/marketwatch/   动态候选池扫描与调度
internal/store/         SQLite 存储和迁移
internal/lab/           信号效果验证
internal/web/           Web 服务、模板和静态资源
tests/fixtures/         Provider 测试数据
docs/                   运行说明与界面截图
```

## 数据与安全边界

- 只调用公开市场接口，不需要交易所 API Key。
- 不读取账户、余额、仓位或订单。
- 不调用下单接口。
- 本地数据库、运行报告和编译产物已通过 `.gitignore` 排除。
- 免费数据源可能存在延迟、限流、缺失或短时不可用。

## 更多文档

- [动态机会雷达使用说明](docs/operations/dynamic-market-radar.md)
- [核心扫描器运维说明](docs/operations/core-scanner.md)
