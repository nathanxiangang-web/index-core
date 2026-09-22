# D03 — Collector Gap Comparison (rclone / fsspec)

> Discovery 03 — 窄范围对照调查 rclone 和 fsspec 能否解决 D02 留下的两个硬缺口。
>
> baseline: `80ce338` | branch: `research/d03-collector-gap-comparison`
> 日期：2026-09-23 | v2 返工：2026-09-23

## 1. 背景与目标

D02 结论（Architect ACCEPTED）：AList/OpenList 可作 Snapshot candidate/source，但存在两个硬缺口：

1. **Stable identity 不统一** — path/Parent+Name 是 path-based matching key，不是 stable resource identity（rename/move 后变化）。AList provider ID 仅部分 Driver 存在（DRIVER_DEPENDENT）；OpenList public FS API 不暴露 id。
2. **Snapshot completeness 无法通过 public API 可靠证明** — 一次 HTTP 200 完整递归遍历 ≠ provider-complete snapshot。

D03 目标：只查 rclone 和 fsspec，只回答这两个硬缺口。如果它们也不能明显解决，则停止寻找轮子，进入 Architecture Gate。

## 2. 源码版本

| Repo | Commit | License |
|------|--------|---------|
| rclone | `cfb90e3ebed479119718e3ae44b1171b060079e9` | MIT |
| fsspec | `751721f96c04fb53aa3b945ab1b0ff53781e79c0` | BSD-3-Clause |

## 3. Worker 产出索引

| Worker | 文件 | 调查者 | 状态 |
|--------|------|--------|------|
| W-A | `docs/research/d03/W-A-RCLONE-IDENTITY-METADATA.md` | Foreman 自查 | PASS |
| W-B | `docs/research/d03/W-B-RCLONE-COMPLETENESS-RC.md` | w02 远程 Worker | APPROVED |
| W-C | `docs/research/d03/W-C-FSSPEC-GAP-CHECK.md` | w03 远程 Worker | APPROVED |
| W-D | `docs/research/d03/W-D-COLLECTOR-GAP-MATRIX.md` | w04 远程 Worker | APPROVED |

## 4. 统一能力矩阵

| 维度 | rclone | fsspec | AList (D02) | OpenList (D02) |
|------|--------|--------|-------------|----------------|
| **Stable Identity** | DRIVER_DEPENDENT — `IDer` 可选接口，38 backend 实现，local/webdav/s3 不实现 | NO — 无 per-object id 接口，`fsid` 是 filesystem-level | DRIVER_DEPENDENT — `/api/fs/list` 有 `id` 字段，但仅部分 Driver 填充 | NO — public FS API 不暴露 id |
| **Snapshot Completeness** | PARTIAL — `List()` contract 期望 complete directory，`err != nil` 可明确降级为 incomplete；不能防 silent backend truncation | PARTIAL — `walk` 完成靠 `StopIteration`，无 done flag | PARTIAL — `len(content)==total` 是 weak sanity check | PARTIAL — 同 AList |
| **Partial Failure Visibility** | PARTIAL — `listRwalk` 继续但最终返回 error；typed errors 可区分；无 structured skipped-set | PARTIAL — `walk(on_error=callable)` 可回调；默认 `omit` 静默跳过 | NO — `storage.List` 出错时 error 被静默吞掉返回部分数据 | NO — 同 AList |
| **Checkpoint/Resume** | NO — 无 scan state 持久化，`--max-duration` 中断后重新从 root 列 | NO — `DirCache` 内存 only，无持久化 | NO | NO |
| **Delta/Change Notify** | DRIVER_DEPENDENT — `ChangeNotify` 可选，14/69 backend 实现，全部 polling 非 webhook | NO — 无 watch/notify/subscribe | NO | NO |
| **Hash/Checksum** | DRIVER_DEPENDENT — `Hashes()` 在核心接口，68 backend 实现，webdav 返回 `hash.None` | PARTIAL — `checksum`/`ukey` 默认是 metadata hash 非 content hash | DRIVER_DEPENDENT — 有 `hash_info` 字段，但 Driver 依赖 | DRIVER_DEPENDENT — 同 AList |
| **Pagination** | PARTIAL — `ListP` 可选，7 backend 实现；RC API 不暴露 continuation token | NO — `ls`/`walk`/`find` 无 continuation-token 参数 | PARTIAL — offset 分页，无完成标志 | PARTIAL — 同 AList |

## 5. 七个最终问题

### Q1: rclone 有无跨 backend 通用 stable identity？

**NO.** `ID()` 是可选接口 `IDer`（`fs/types.go:166-170`），不在核心 `Object` 接口中。local/webdav/s3/azureblob/sftp/ftp/http 不实现。即使实现，返回的是 provider native ID（OneDrive item ID、Drive file ID 等），格式和语义各不相同。`lsjson` 对不实现 `IDer` 的 backend 静默返回空 ID。

### Q2: fsspec 有无跨 backend 通用 stable identity？

**NO.** `AbstractFileSystem`（`fsspec/spec.py:151`）不定义 per-object id 方法。`fsid`（`spec.py:222-227`）是 filesystem-level ID，不是 per-object。`info()` 返回的额外字段是 backend-specific（local 返回 `ino`，memory 无 id）。

### Q3: rclone 能否证明 provider-complete traversal？

**PARTIAL — contract-level completeness with explicit failure propagation, not proof against silent backend truncation.**

- `List()` contract 期望返回 complete single directory（`fs/types.go:20-29`）
- 显式目录遍历错误最终会返回 non-nil error（`listRwalk` 即使继续其它目录，最终仍返回 error，`fs/walk/walk.go:168-185`）
- `err == nil` 是基于 rclone contract 的 "successful traversal" 信号
- `err != nil` 可以明确把 Snapshot 降级为 incomplete
- 缺少 done flag 本身不是核心问题；即使有 done flag，也无法证明 backend 没有静默漏项
- rclone 不能证明 provider 从未 silent-truncate

这是 D03 真正证明 rclone 比 AList/OpenList 强的地方之一：AList `storage.List` 出错时 error 被静默吞掉，rclone 会传播错误。

### Q4: fsspec 能否证明 provider-complete traversal？

**NO.** `walk` 完成靠 `StopIteration`，无 done flag。默认 `on_error="omit"` 静默跳过失败目录。`ls` 合约只保证 `name`/`size`/`type`，无 completeness guarantee。无 continuation token。`DirCache` 内存 only，TTL 内返回 stale data。

### Q5: 哪些 partial failure 可以可靠暴露？

**可靠暴露**（backend 返回显式 error 时）：
- rclone：per-directory error 带路径记录 + 全局 error count + typed errors（`ErrorDirNotFound`/`ErrorPermissionDenied`/`ErrorListAborted`）；最终返回 non-nil error
- fsspec：`walk(on_error=callable)` 回调 + `cat(on_error="return")` per-path exception dict

**不可靠暴露**：
- 静默 backend truncation（无工具可检测）
- Stale cache（rclone VFS / fsspec DirCache TTL 内无 "stale" flag）
- fsspec `exists`/`isdir`/`isfile` 用 bare `except:` 吞掉所有异常

### Q6: 能力归属候选（最终归属由 Architecture Gate 决定）

"工具没有提供" ≠ "Kernel 必须实现"。以下为三层归属候选，不提前钉死：

#### Kernel 安全语义候选
- canonical resource identity 的决策/连续性规则
- Snapshot completeness acceptance / Safety Gate
- Canonical Inventory
- Safe Reconcile / removal safety
- Change Journal（canonical change journal，不等于 provider-native delta）

#### Collector / Scanner 责任候选
- traversal / pagination
- provider error capture
- skipped-path evidence
- cache bypass / refresh policy
- scan checkpoint/resume（蓝图已明确放在 Scanner Resume 阶段，不能直接塞进 Kernel）

#### Optional capability，不得强制
- provider_object_id
- hash / content hash（蓝图明确 `hash optional`，第一阶段绝不能要求所有 Provider 有 hash）
- native delta / change notify
- provider metadata

#### 已知事实（归属待定）
- listed entry 不自带 root identity — Architecture Gate must assign responsibility
- rclone RC 不返回 structured skipped-set — Architecture Gate must assign responsibility
- cache 可能 stale — Architecture Gate must assign responsibility

### Q7: Discovery 是否已经足够，可以进入 Architecture Gate？

**YES.** D02 + D03 evidence is sufficient to enter Architecture Gate. Remaining unknowns (U1-U5) are not blockers for defining the first architecture contracts; they are implementation/integration validation items.

## 6. 比较结论

- **rclone**：比 AList/OpenList 提供更强的通用 Collector 能力（typed errors、ListR/ListP、optional ID/hash/change notify），但高级能力仍 backend-dependent；不能提供跨 backend stable identity，也不能防 silent provider truncation。
- **fsspec**：不解决 stable identity，completeness/error 模型没有形成明显优势；当前没有证据要求把它引入主线。
- **AList/OpenList**：仍是可用的现有 Provider 聚合入口，但 public API 的 identity/completeness 较弱。
- **Architecture Gate 决定**：选择哪个 Collector，以及 identity/completeness/checkpoint 等责任具体落在哪层。
- 不再继续广泛寻找轮子。
