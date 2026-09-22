# D03 — Collector Gap Comparison (rclone / fsspec)

> Discovery 03 — 窄范围对照调查 rclone 和 fsspec 能否解决 D02 留下的两个硬缺口。
>
> baseline: `80ce338` | branch: `research/d03-collector-gap-comparison`
> 日期：2026-09-23

## 1. 背景与目标

D02 结论（Architect ACCEPTED）：AList/OpenList 可作 Snapshot candidate/source，但存在两个硬缺口：

1. **Stable identity 不统一** — path/Parent+Name 是 path-based matching key，不是 stable resource identity（rename/move 后变化）。AList provider ID 仅部分 Driver 存在；OpenList public FS API 不暴露 id。
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

| 维度 | rclone | fsspec | AList/OpenList (D02) |
|------|--------|--------|---------------------|
| **Stable Identity** | DRIVER_DEPENDENT — `IDer` 可选接口，38 backend 实现，local/webdav/s3 不实现 | NO — 无 per-object id 接口，`fsid` 是 filesystem-level | NO — path-based，provider ID 部分存在但不统一 |
| **Snapshot Completeness** | PARTIAL — `List()` doc 说 "should be for a complete directory"（soft contract），完成靠 iterator exhaustion，无 done flag | PARTIAL — `walk` 完成靠 `StopIteration`，无 done flag | PARTIAL — `len(content)==total` 是 weak sanity check |
| **Partial Failure Visibility** | PARTIAL — `listRwalk` 继续但返回 last error；typed errors 可区分；无 structured skipped-set | PARTIAL — `walk(on_error=callable)` 可回调；默认 `omit` 静默跳过 | NO — error 被静默吞掉 |
| **Checkpoint/Resume** | NO — 无 scan state 持久化，`--max-duration` 中断后重新从 root 列 | NO — `DirCache` 内存 only，无持久化 | NO |
| **Delta/Change Notify** | DRIVER_DEPENDENT — `ChangeNotify` 可选，14/69 backend 实现，全部 polling 非 webhook | NO — 无 watch/notify/subscribe | NO |
| **Hash/Checksum** | DRIVER_DEPENDENT — `Hashes()` 在核心接口，68 backend 实现，webdav 返回 `hash.None` | PARTIAL — `checksum`/`ukey` 默认是 metadata hash 非 content hash | NO — list 中无 hash |
| **Pagination** | PARTIAL — `ListP` 可选，7 backend 实现；RC API 不暴露 continuation token | NO — `ls`/`walk`/`find` 无 continuation-token 参数 | PARTIAL — offset 分页，无完成标志 |

## 5. 七个最终问题

### Q1: rclone 有无跨 backend 通用 stable identity？

**NO.** `ID()` 是可选接口 `IDer`（`fs/types.go:166-170`），不在核心 `Object` 接口中。local/webdav/s3/azureblob/sftp/ftp/http 不实现。即使实现，返回的是 provider native ID（OneDrive item ID、Drive file ID 等），格式和语义各不相同。`lsjson` 对不实现 `IDer` 的 backend 静默返回空 ID。

### Q2: fsspec 有无跨 backend 通用 stable identity？

**NO.** `AbstractFileSystem`（`fsspec/spec.py:151`）不定义 per-object id 方法。`fsid`（`spec.py:222-227`）是 filesystem-level ID，不是 per-object。`info()` 返回的额外字段是 backend-specific（local 返回 `ino`，memory 无 id）。

### Q3: rclone 能否证明 provider-complete traversal？

**NO.** `List()` doc 用 "should"（soft contract），完成靠 iterator exhaustion（无 done flag）。`listRwalk` 遇到目录列表错误时继续遍历但只返回 last error，无 structured skipped-set。`operations/list` RC 返回仅 `list` 数组，无 completeness marker / total count / pagination cursor。无 checkpoint/resume。

### Q4: fsspec 能否证明 provider-complete traversal？

**NO.** `walk` 完成靠 `StopIteration`，无 done flag。默认 `on_error="omit"` 静默跳过失败目录。`ls` 合约只保证 `name`/`size`/`type`，无 completeness guarantee。无 continuation token。`DirCache` 内存 only，TTL 内返回 stale data。

### Q5: 哪些 partial failure 可以可靠暴露？

**可靠暴露**（backend 返回显式 error 时）：
- rclone：per-directory error 带路径记录 + 全局 error count + typed errors（`ErrorDirNotFound`/`ErrorPermissionDenied`/`ErrorListAborted`）
- fsspec：`walk(on_error=callable)` 回调 + `cat(on_error="return")` per-path exception dict

**不可靠暴露**：
- 静默 backend truncation（无工具可检测）
- Stale cache（rclone VFS / fsspec DirCache TTL 内无 "stale" flag）
- fsspec `exists`/`isdir`/`isfile` 用 bare `except:` 吞掉所有异常

### Q6: 哪些能力最终还是必须由 IndexCore 自己承担？

1. **Stable object identity through rename/move** — 所有四个工具都不提供跨 backend 通用 stable ID
2. **Scan-state checkpoint and resume** — 所有四个工具都没有
3. **Provable completeness signal** — 所有四个工具都靠 exhaustion 推断，不证明
4. **Content hash for all backends** — rclone DRIVER_DEPENDENT，fsspec 默认 metadata hash，AList/OpenList 无
5. **Durable delta log with replay** — rclone ChangeNotify 是 polling hint 无 replay，其他无 notify
6. **Structured skipped-set on partial failure** — rclone 返回 first error，fsspec 默认 omit，AList HTTP status only
7. **Cross-root disambiguation in the entry** — 无工具在 listed entry 中嵌入 root/storage identity
8. **Real-time state under cache** — rclone VFS / fsspec DirCache 返回 stale data，snapshot scan 须 cache=off

### Q7: Discovery 是否已经足够，可以进入 Architecture Gate？

**YES.** D02 + D03 已用源码引用建立了四个候选 collector 层（AList、OpenList、rclone、fsspec）在七个维度上的能力画像。每个缺口都有源码引用，无 UNKNOWN 格。Architecture Gate 可以基于以下 grounded contract 进入：

- **Collector 提供**：best-effort listing + per-path error visibility
- **IndexCore 承担**：identity、checkpoint、completeness reconciliation、content hash、delta log、skipped-set capture、root disambiguation、cache bypass

无需进一步源码调查；剩余工作是设计，不是发现。

## 6. 结论

rclone 和 fsspec **都不能**解决 D02 留下的两个硬缺口：

1. **Stable identity**：rclone 的 `IDer` 是可选接口，关键 backend（local/webdav/s3）不实现；fsspec 无 per-object id 接口。两者都是 DRIVER_DEPENDENT 或 NO。
2. **Snapshot completeness**：两者都靠 iterator exhaustion 推断完成，无显式 done flag，无 checkpoint/resume，partial failure 不返回 structured skipped-set。

rclone 比 fsspec 和 AList/OpenList 都强（可选 ID、typed errors、ListR、ChangeNotify polling），但每个高级能力都是 DRIVER_DEPENDENT——在关键 backend 上缺失。

**Discovery 阶段结束。进入 Architecture Gate。**