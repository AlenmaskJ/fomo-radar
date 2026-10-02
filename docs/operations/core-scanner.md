# Core scanner operations

## Local gate

Run the complete gate from the repository root:

```bash
go test ./...
go vet ./...
go build -trimpath -ldflags="-s -w" -o fomo-scanner ./cmd/fomo
```

The build produces one standalone `fomo-scanner` binary. Docker and a browser are not required.

## Linux/VPS manual scan

Run these commands from the repository root on the target host:

```bash
go test ./...
go build -trimpath -ldflags="-s -w" -o fomo-scanner ./cmd/fomo
/usr/bin/time -v ./fomo-scanner -scan
sqlite3 fomo.db 'select chain,address,first_seen_market_cap_usd from tokens order by first_seen_at desc limit 10;'
```

Inspect recent live scan outcomes, including degraded-provider details:

```bash
sqlite3 -header -column fomo.db "select id,mode,status,candidates_seen,candidates_scored,error_summary,datetime(started_at/1000.0,'unixepoch') as started,datetime(finished_at/1000.0,'unixepoch') as finished from scan_runs order by id desc limit 10;"
```

Use the Linux `/usr/bin/time -v` result to validate the `<150 MB` normal-operation acceptance target on the actual VPS. The current Windows development gate does not measure or claim a Linux fixture RSS result.

Observe live-provider behavior separately. The manual `-scan` command calls free BSC and Solana providers whose latency, rate limits, availability, and returned candidate counts can vary between runs. A provider or chain can degrade while the other chain still produces and persists results; investigate the scanner status and error summary rather than comparing a live run byte-for-byte with fixture output.

The production CLI shares a conservative GeckoTerminal pacer between discovery and trade reads: at most about one request every six seconds, including retry attempts. This follows the lower current free-tier allowance and avoids systematic HTTP 429 responses, at the cost of slower deep-scan throughput.

## Fast watch mode

`-scan` remains a full one-shot command: it persists Fast snapshots, waits for eligible Deep work, persists current-generation Deep snapshots, prints final results, and exits.

`-watch` starts Fast discovery immediately and then targets a two-minute start cadence:

```bash
./fomo-scanner -watch
```

Fast rounds never overlap. If one Fast round lasts longer than two minutes, the next starts after it finishes. Deep work runs independently through four workers and a bounded 64-task queue. Duplicate `(token, Fast snapshot)` tasks are rejected. A Deep result is appended only while its base Fast snapshot is still the token's latest generation; late results are marked stale and cannot replace newer Fast evidence.

Each Fast round prints the actual per-chain API coverage (`newest_pool_at`, `oldest_pool_at`, page count, and unique candidates), evidence confidence, raw/effective tiers, launch types, market source counts, and Deep backlog. The configured candidate age window is a filter, not a claim that one API round covered that entire duration. Continuous watch operation builds local coverage forward from the time watch started while preserving the shared GeckoTerminal pacing.

Evidence confidence gates only the displayed effective tier. The immutable V1 raw score and raw tier remain unchanged:

- `HIGH` (5/5) and `GOOD` (4/5): raw tier retained.
- `MEDIUM` (3/5): capped at `FAST_RISING`.
- `LOW` (0–2/5): capped at `WATCH`.

Social evidence is optional and is not counted among the five required core evidence factors.

## Champion/Challenger 运维

构建管理命令：

```bash
go build -trimpath -o fomo-score ./cmd/fomo-score
```

默认 Core 数据库来自 `FOMO_DB`（未设置时为 `./fomo.db`），Lab 数据库来自
`FOMO_LAB_DB`（未设置时为 `./fomo-lab.db`）。所有角色变更都要求人工原因并写入不可变
审计事件。

### 查看和注册 Challenger

```bash
./fomo-score list --db ./fomo.db
./fomo-score add \
  --db ./fomo.db \
  --config ./tests/fixtures/fomo_score_v1_1_challenger.json \
  --reason "验证 buyer_velocity 权重调整"
```

配置必须是完整 JSON，最大 1 MiB。建议从当前版本的完整配置复制后只改版本号和明确要
试验的数值，并把每个已部署配置随发布物归档。示例 fixture 将
`buyer_velocity.maximum` 从 25 调为 24；不能只提交差异片段。系统最多允许三个活跃
Challenger，且拒绝只换版本号、数值与当前 Champion 完全相同的配置。

### 晋升前检查

先让 Scanner 持续产生未来影子评分，再更新 Lab 并查看报告：

```bash
./fomo-lab -run-once -source-db ./fomo.db -lab-db ./fomo-lab.db
./fomo-lab -report -lab-db ./fomo-lab.db
./fomo-score compare --db ./fomo.db --lab-db ./fomo-lab.db
```

`compare` 会核对 Lab 绑定的 Core 路径和数据库身份；不匹配或 Lab 未初始化时拒绝输出。
缺失统计显示 `N/A`，样本不足显示 `INSUFFICIENT`。命令只展示数据和 Challenger 相对
Champion 的差值，不自动推荐胜者。

### 晋升、停用与回滚

人工确认后使用预期 Champion 执行 CAS 晋升：

```bash
./fomo-score promote \
  --db ./fomo.db \
  --version FOMO_SCORE_V1.1 \
  --expected-champion FOMO_SCORE_V1.0 \
  --reason "Lab 样本达到门槛，人工批准"

./fomo-score disable \
  --db ./fomo.db \
  --version FOMO_SCORE_V1.2 \
  --reason "停止该实验"
```

晋升只影响下一轮 Fast generation；已经排队的 Deep 任务继续使用其基础 Fast 快照冻结
的版本和角色。旧 Champion 会进入 `RETIRED`，历史评分不会删除或重算。

回滚也必须走完整审计链：先用归档的旧 Champion 完整配置重新执行 `add`，让它以
Challenger 身份积累新的前瞻数据；人工复核后，再把它显式晋升，并把当前 Champion
作为 `--expected-champion`。不要直接修改 SQLite 角色表。

## 评分基准

命令：

```bash
go test -run '^$' -bench 'BenchmarkEvaluateScore(SetFourVersions)?$' -benchmem ./internal/score
```

2026-09-19 使用 Go 1.26.8、Windows/amd64、Intel i5-10400F 的实测结果：

```text
BenchmarkEvaluateScore-12                    625419    1858 ns/op    1128 B/op     7 allocs/op
BenchmarkEvaluateScoreSetFourVersions-12     147555    8161 ns/op    5536 B/op    29 allocs/op
```

四版本集合约为单版本耗时的 4.5 倍；它只增加本地内存计算和同事务评分行，不增加
Provider 请求次数。实际 VPS 的 `<150 MB` RSS 目标仍需按上文 Linux 命令独立验证。
