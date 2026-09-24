# Incremental Change Discovery Research Report

> Status: **IN PROGRESS — EVIDENCE COLLECTED, ARCHITECT DECISION PENDING**
>
> Research issue: #59
>
> Parent architecture issue: #57
>
> Blueprint: `docs/architecture/POST-MVP-INCREMENTAL-BLUEPRINT.md`
>
> Production implementation: **NOT AUTHORIZED**
>
> 本报告只陈述事实与候选方案；**Final Architecture Decision 留给 Architect**。

---

## 1. Research question

Can IndexCore discover real provider changes materially faster than ordinary
AList/OpenList cache visibility while:

- avoiding repeated full-provider traversal;
- keeping real-provider request volume small and controlled;
- reusing mature provider/driver implementations;
- preserving existing IndexCore safety semantics?

Primary scenario:

```text
12:00 user/provider creates a file on the real cloud drive
        ↓
real cloud drive already contains it
        ↓
AList/OpenList cached view is still stale
        ↓
IndexCore wants earlier visibility
        ↓
Web should see the resource earlier
```

### 1.1 Executive Summary

1. **115 官方开放平台不提供 native change feed / delta / cursor / webhook。**
   官方 API 列表（文件管理/用户/视频/云下载共 40+ 接口）已全量核对，无 `since` /
   `cursor` / `change token` / `recent changes` / callback / event stream。
   → `native delta` 对 115 的判定为 **UNAVAILABLE**（证据级别：DIRECT）。

2. **`OpenList/AList /api/fs/list?refresh=true` 是真正绕过目录缓存、直达 provider 的强制刷新。**
   源码证实：`refresh=false` 时命中内存 `dirCache` 直接返回缓存；`refresh=true`
   跳过缓存并调用 `storage.List`（provider API）。
   → 这是当前唯一被源码证明的「比普通 AList/OpenList 缓存更早看到外部变更」的手段
   （证据级别：DIRECT）。

3. **`refresh=true` 只刷新被请求的那一个目录，不递归。**
   成功返回会覆盖/更新该目录自身的缓存；**子目录缓存不会随之失效**。
   空结果会删除该目录及子树的缓存。
   （证据级别：DIRECT）

4. **单次目录刷新不保证只有 1 次 provider 请求。**
   目录列表是「一次性拉全目录再内存分页」的模型：OpenList 的 115 driver
   `ListWithLimit` 会从 `offset=0` 循环分页直到取完整目录。
   一个 30k 直接子项目录 ≈ ⌈N / page_limit⌉ 次 provider 列表请求。
   （证据级别：DERIVABLE，来自源码调用链）

5. **OpenList/AList 提供的 115 支持依赖社区第三方库，走 115 web 客户端私有接口，而非官方开放平台。**
   `SheltonZhu/115driver` 使用 `webapi.115.com` / `proapi.115.com/app/...` +
   Cookie/二维码 + `115Browser` UA 登录。
   115 官方《开发须知》明确禁止「调用非公开接口」。
   → 私有 API 风险等级：**HIGH**（证据级别：DIRECT）。

6. **rclone 提供可选 `ChangeNotify` 接口（14/69 backend，全部 polling，非 webhook），但没有 115 backend。**
   → 对 115 不适用；对其它 backend 为 DRIVER_DEPENDENT。（证据级别：DIRECT）

7. **没有任何被审查来源（115 官方 / OpenList / AList / rclone / fsspec）提供跨 provider 的
   native delta + durable cursor。**
   → Native Delta 只有在「某个 provider 明确暴露可信 change feed」时才可能落地，
   对 115 而言当前 **不可行**。

8. 当前证据支持的候选方向是 **Hybrid（Mutation Hint + Scoped Refresh + Adaptive Polling +
   Full Scan fallback）**，但：
   - 最终是否采用、先做哪条、SLA 定多少，**由 Architect 决定**；
   - 上传成功后 provider list 何时可见（最终一致性）仍为 **UNKNOWN，需要 live test**。

---

## 2. Evidence vocabulary and source versions

### 2.1 Evidence level

- **DIRECT** — official docs / API / source proves it;
- **DERIVABLE** — behavior follows clearly from implementation/protocol;
- **DRIVER_DEPENDENT** — true only for specific provider/driver;
- **UNAVAILABLE** — checked and not supported;
- **UNKNOWN** — evidence insufficient;
- **COMMUNITY** — community/secondary source, not official.

### 2.2 Source version record

| Source | Ref | Date checked | Evidence |
| --- | --- | --- | --- |
| 115 官方开放平台（文档，社区镜像） | `truewhile/MeBox@115doc/115开放平台` (HEAD, 2026-09 抓取) | 2026-09-24 | 官方文档镜像 |
| 115 官方开放平台（端点实测） | `https://proapi.115.com/open/ufile/files` 返回 `40140123 access_token 格式错误` | 2026-09-24 | DIRECT |
| OpenList | `90acfa18e461e052482c6f80528f464d15ed1365`（2026-09-24） | 2026-09-24 | source |
| AList | `fb0731a6953012e7b72b89bf5473817caa4625f9`（2026-09-19） | 2026-09-24 | source |
| `SheltonZhu/115driver` | `542720cb0034954750454e89f32b4852add6e2a9`（2026-09-14） | 2026-09-24 | source |
| rclone | `cfb90e3ebed479119718e3ae44b1171b060079e9`（D03 记录）；本次复核 `fs/features.go` | 2026-09-24 | source |
| License | OpenList/AList = AGPL-3.0；115driver = 见其仓库；rclone = MIT；fsspec = BSD-3 | 2026-09-24 | DIRECT |

> 115 官方文档 `open.115.com` 为 SPA，直接抓取拿不到正文；本报告采用其公开文档的
> 社区镜像 `truewhile/MeBox:115doc/115开放平台`（内容标注为官方文档结构），并以
> `proapi.115.com` 端点实测交叉验证。（证据级别：官方端点 DIRECT + 文档镜像 COMMUNITY）

---

## 3. Capability matrix

| Capability | 115 官方开放平台 | OpenList / AList | rclone | Evidence level | Notes |
| --- | --- | --- | --- | --- | --- |
| native change feed | 无 | 无 | 无（仅可选 ChangeNotify） | UNAVAILABLE / DRIVER_DEPENDENT | 全量 API 列表核对；rclone ChangeNotify 非 feed |
| durable provider cursor | 无 | 无 | 无 | UNAVAILABLE | 无 change token |
| recent / modified-since listing | 无（仅单目录列表可按 `user_utime` 排序 + 搜索有 `gte_day/lte_day`） | 无 | 无 | UNAVAILABLE（全局）/ DIRECT（局部排序） | 见 §5 |
| webhook / callback / SSE | 无 | 无 | 无 | UNAVAILABLE | OpenList 变更通知仅内部 Go hook |
| targeted directory refresh | 无「refresh」概念，但有 `cid` 目录级 list | 有，`refresh=true` | 无（重新 List 目录） | DIRECT | Scoped Refresh 基础 |
| bypass cache / force refresh | N/A（无缓存层） | `refresh=true` 跳过 `dirCache` 直达 provider | DirCache TTL / `--dir-cache-time` | DIRECT | 关键能力 |
| stable object ID | **有** `fid`（+ `pid` 父目录 ID） | AList 部分 driver 有 `id`；OpenList public API **不暴露 id** | 可选 `IDer`（backend-dependent） | 115=DIRECT / OpenList=UNAVAILABLE / rclone=DRIVER_DEPENDENT | 见 §12/§14 |
| move/rename fidelity | 有 `ufile/move`、`ufile/update`；对象 ID 不变 | 有，但 public API 不返回 old+new id | DRIVER_DEPENDENT | DIRECT（官方）/ DRIVER_DEPENDENT（driver） | |
| delete fidelity | 有 `ufile/delete` + 回收站列表（含 `dtime`、原 `cid`） | 有 remove + rb | DRIVER_DEPENDENT | DIRECT（官方能力存在） | 事件流仍 UNAVAILABLE |
| pagination guarantees | offset+limit，`limit` 最大 1150；搜索 `offset+limit ≤ 10000` | 内存切片分页；`driver.List` 一次拉全目录 | `ListP` 可选，7 backend | DIRECT | 大目录放大见 §9 |
| rate-limit / quota documentation | 「对所有 API 实施频率控制，细则**不公开**」 | 客户端 `LimitRate`（115 driver 默认 2 r/s）+ 服务端 Unknown | rclone `--tpslimit` 等本地限流 | 官方=UNKNOWN / driver=DIRECT | 见 §10 |
| token refresh failure semantics | OAuth2：7200s、refresh 轮换、终态错误码 40140116/19/20/37 | Cookie 失效 → 错误 | rclone 各 backend 不同 | DIRECT（官方）/ DRIVER_DEPENDENT | 见 §10 |
| search / by-time filtering | `ufile/search` 支持 `gte_day`/`lte_day`（天粒度），需 `search_value`/`file_label` | 搜索走内部索引，非 provider 全量 | — | DIRECT | 不足以替代 change feed |

---

## 4. Candidate strategy A — Mutation Hint

### 4.1 Fact base

当「我们自己的写入路径」完成一次已知 mutation（上传/移动/重命名/离线下载完成）时，
系统已知目标目录，可立即触发一次 targeted verification。

- OpenList **写操作后主动维护自身缓存**（DIRECT）：
  - `op.Put` 成功后：`Cache.linkCache.DeleteKey(...)` 并 `cache.UpdateObject(newObj.GetName(), newObj)`
    —— **就地更新父目录缓存**，并异步触发 `objsUpdateHook`
    （`internal/op/fs.go:610-668`）。
  - `Rename/Move/Copy/Remove` 会调用 `Cache.deleteDirectoryTree(...)` 失效相关目录树
    （`internal/op/fs.go:423-429, 486-489, 554, 602, 681, 749`）。
- 但该机制只覆盖「经 OpenList 执行」的写入。**外部**新增（场景主体：用户在真实云盘上传）
  不会自动进入 OpenList 缓存（DIRECT，`refresh=false` 命中缓存即返回）。

### 4.2 Answers to research questions

| Question | Finding | Level |
| --- | --- | --- |
| target scope precision | 由调用方已知（父目录路径/`cid`），精确到单目录 | DIRECT |
| provider list 一致性（mutation 成功后多久可见） | **未知**，取决于 115 侧最终一致性；官方文档未承诺 | UNKNOWN |
| 能否只刷新该 scope | 可以（`refresh=true` + 单目录 path） | DIRECT |
| retry/backoff | 无现成官方保证；需要 IndexCore 侧定义 | UNKNOWN |

### 4.3 Assessment

- Mutation Hint 本身**不是变更发现**，而是「触发一次 scoped refresh 的事件源」。
- 对**外部**上传无信息 → 仍需要轮询或其它手段覆盖。
- 需要 retry/backoff 应对最终一致性（否则会「刚上传完，provider 还没可见」）。

---

## 5. Candidate strategy B — Native Delta

### 5.1 115 官方开放平台

官方 API 列表（文件管理 15 项、用户 1 项、视频 5 项、云下载 7 项、接入 6 项）**全量核对**，
**不存在** change feed / delta / cursor / recent changes / webhook / event stream。

相关但不等价的接口：

- `GET /open/ufile/files`：单目录分页列表，支持 `o=user_utime`（更新时间排序）。
  **没有 `since` / `after` / `cursor` 参数**（DIRECT）。
- `GET /open/ufile/search`：`search_value` / `file_label` 至少一个，支持
  `gte_day` / `lte_day` 日期范围与 `cid` 目录过滤，`offset+limit ≤ 10000`。
  这是**按关键词/标签 + 日期**的搜索，**不是**「自上次以来发生了什么」的变更流（DIRECT）。
- `GET /open/rb/list`：回收站列表（含 `dtime`、原父目录 `cid`），可轮询发现删除，
  但仍需全量比对，且不覆盖 move/rename（DIRECT）。

结论：**115 官方 native delta = UNAVAILABLE。**

### 5.2 OpenList / AList

无 `since` / `cursor` / watch / SSE / webhook。变更通知只有内部 Go hook
（`HandleObjsUpdateHook`，用于驱动内部搜索索引），不对外提供（DIRECT，见 D02 报告）。

### 5.3 rclone

存在可选 `ChangeNotifier` 接口（`fs/features.go:569-581`），语义为：
实现（可为 polling）在对端发生变更时调用回调；文档明确「若使用 polling 应遵守给定
interval」。D03 已证实：14/69 backend 实现，**全部为 polling，不是 webhook**；且
**rclone 没有 115 backend**（`rclone.org/115` = 404）。
→ 对 115 不适用；对其它 backend 属于 DRIVER_DEPENDENT。

### 5.4 Overall

**没有任何被审查来源为 115 提供可信的 native delta / cursor。**
即便未来某个 provider 支持，也必须逐 provider 验证 cursor 的持久性、可 replay 性、
过期/重置、gap 检测、retention、create/update/move/delete 完整度（blueprint §17 的 fail-closed 要求）。

---

## 6. Candidate strategy C — Scoped Refresh（本轮最关键方向）

### 6.1 OpenList `/api/fs/list` 调用链

```text
HTTP  /api/fs/list  {path, refresh}
        ↓
server/handles/fsread.go  FsList  ← refresh 需写权限（否则 403）
        ↓
internal/fs  List
        ↓
internal/op/fs.go  list()
   ├─ if !args.Refresh && dirCache hit → 直接返回缓存（不打 provider）
   └─ else → singleflight → storage.List(ctx, dir, args)   ← 真实 provider 调用
        ↓
daemon driver（如 drivers/115）→ provider API
```

证据：`internal/op/fs.go:31-122`；`server/handles/fsread.go:79-118`。

### 6.2 Answers to the twelve required questions

1. **`refresh=false` 是否优先读缓存？** 是。命中 `dirCache` 即返回，不打 provider。（DIRECT）
2. **`refresh=true` 到底做什么？** 跳过缓存读取，直接 `storage.List` 调 provider；成功后
   覆盖/更新该目录缓存（非空）或删除该目录子树缓存（空结果）。（DIRECT）
3. **是否「删缓存后重新 list」？** 语义上是「绕过缓存读并刷新缓存」，不是先删再读；
   空结果时才会 `deleteDirectoryTree`。（DIRECT）
4. **是否一定打到 provider？** 是（在 storage 状态正常、权限允许前提下）。（DIRECT）
5. **是否只刷新当前目录？** 是，仅被请求目录。（DIRECT）
6. **是否会递归？** 否，不递归刷新后代。（DIRECT）
7. **Directory Cache 粒度？** 进程内内存缓存，key = `GetFullPath(MountPath, path)`
   （挂载路径 + 目录路径），即 **per-directory**。（DIRECT，`internal/op/cache.go:35`）
8. **不同 Storage 能否不同缓存？** 可以：`driver.Config.NoCache`、`Storage.CacheExpiration`
   为 storage 级配置。（DIRECT，`internal/model/storage.go:12-14`）
9. **不同路径能否不同缓存？** **可以**：OpenList 新增 `CustomCachePolicies`，
   支持 `pattern:ttl` 的 glob 级 per-path TTL（DIRECT，`internal/op/fs.go:85-105`）。
   AList 无此字段（D02）。
10. **有没有现成事件/索引机制？** 有内部搜索索引 `HandleObjsUpdateHook`，但**不对外**，
    且 D02 已判定其不具备 Canonical Inventory 条件。（DIRECT）
11. **有没有主动 cache invalidation？** 有：写操作（rename/move/copy/remove）失效相关目录树；
    Put 成功后就地更新父目录缓存。（DIRECT）
12. **上传/复制/移动后 OpenList 自己是否会刷新缓存？**
    - 经 OpenList 自身的写操作：会（Put 更新父目录缓存；move/rename/remove 失效目录树）。
    - **外部**变更（不经 OpenList）：不会，必须 `refresh=true` 或等待 TTL 过期。（DIRECT）

### 6.3 大目录成本（重要）

OpenList 的 115 driver：

- `drivers/115/meta.go`：`PageSize` 默认 **1000**；`LimitRate` 默认 **2 r/s**（客户端限流）。
- `drivers/115/util.go:62`：`getFiles` → `client.ListWithLimit(fileId, PageSize)`。
- `115driver/pkg/driver/dir.go:38-84`：`ListWithLimit` 从 `offset=0` **循环分页**，
  每页 `limit`（上限 `MaxDirPageLimit = 1150`），直到 `offset >= count`。
- OpenList 再对整个切片做内存分页（`fsread.go` 的 `pagination`）。

含义：

```text
一次 refresh=true（某目录）
≈ ⌈N / 1000⌉ 次 provider 列表请求（N = 该目录直接子项数）
```

| 目录直接子项 N | 约 provider 请求/次刷新 |
| --- | --- |
| 100 | 1 |
| 1,000 | 1 |
| 10,000 | 10 |
| 30,000 | 30 |

> 因此**红线**：不能假设「一次 scoped refresh = 1 次 provider 请求」。
> 大目录会线性放大。（证据级别：DERIVABLE）

### 6.4 115 driver 的 provider 来源（风险）

- OpenList 115 driver 依赖 `github.com/SheltonZhu/115driver v1.3.5`（社区库）。
- 其列表端点为 `https://webapi.115.com/files`（web 客户端私有接口），
  上传/移动/复制/重命名/删除走 `webapi.115.com/files/*`、`aps.115.com`、`uplb.115.com`。
- 认证为 Cookie（`UID/CID/SEID/KID`）或二维码，UA 伪装 `115Browser/<ver>`。
- 115 官方《开发须知》明确禁止「调用非公开接口」，并保留「服务限流、接口冻结、
  资质回收」等处置权。
→ 依赖 OpenList 的 115 支持 = 依赖**私有/逆向接口**，风险等级 **HIGH**。（DIRECT）

### 6.5 AList 对照

D02 已证实 AList 与 OpenList 行为基本一致：`refresh=true` 绕过 cache 且需写权限；
cache TTL 默认 30 分钟；无 native delta。本报告仅补充 OpenList 新增的
`CustomCachePolicies` 与更细的缓存失效语义。（DIRECT / D02 复用）

---

## 7. Candidate strategy D — Adaptive Polling

### 7.1 Fact base

- 不存在 provider 侧推送；发现只能靠**主动 list**（轮询）。（DIRECT）
- `refresh=true` 会绕过缓存，因此「轮询 + refresh=true」= 每次都真实打 provider。
- 不传 `refresh=true` 的轮询会命中缓存，**不会**提前发现（直到 TTL 过期）。（DIRECT）

### 7.2 Research questions

| Question | Finding | Level |
| --- | --- | --- |
| safe request budget per account/root | 115 官方未公开限流细则；OpenList 115 driver 客户端默认 2 r/s | UNKNOWN（官方）/ DIRECT（driver） |
| jitter/backoff | 无现成约定，需 IndexCore 定义 | UNKNOWN |
| account throttling | 存在（官方保留限流/冻结/资质回收）；具体阈值不公开 | UNKNOWN |
| no-change cost over 24h | 见 §9 模型 | DERIVABLE |

### 7.3 Assessment

Adaptive Polling 在**不引入 native delta** 的前提下是覆盖「外部变更」的必要手段，
但其请求成本完全取决于「热 scope 的数量 × 刷新频率 × 目录大小」。若对所有 root/目录
统一高频刷新，会迅速放大（见 §9），并触达未知的 115 风控。
「2 分钟是否合理」**不能在本报告冻结**，需先有 provider 请求预算与 live test。

---

## 8. Candidate strategy E (Full Scan) and F (Hybrid)

- **Full Scan** 必须保留为：不支持增量的 provider 的 fallback、cursor 失效 fallback、
  大量 dirty scope fallback、周期性验证、人工强制验证。（Blueprint §11/§15/§23）
- **Hybrid**：Mutation Hint（已知写入立刻触发）+ Scoped Refresh（`refresh=true` 单目录）
  + Adaptive Polling（覆盖外部变更）+ Full Scan（backstop / 验证）是当前证据下
  最完整、也最不依赖「不存在的 native delta」的候选组合。
  是否采用由 Architect 决定。

---

## 9. Request amplification model

假设：对某目录做一次 `refresh=true` 的 provider 请求数 ≈ `ceil(N / page)`，
其中 115 官方 open API `page ≤ 1150`，OpenList 115 driver 默认 `page = 1000`。

### 9.1 单次刷新

| 场景 | provider 请求数 |
| --- | --- |
| 小目录（<1000 项） | 1 |
| 10k 项目录 | ~10 |
| 30k 项目录 | ~30 |

### 9.2 周期性轮询（小目录，1 请求/次）

| 间隔 | 请求/天/目录 |
| --- | --- |
| 10 s | 8,640 |
| 30 s | 2,880 |
| 2 min | 720 |
| 5 min | 288 |
| 15 min | 96 |
| 30 min | 48 |
| 60 min | 24 |

### 9.3 多 root 共享一个账号（2 min 轮询、每 root 1 个热目录）

| root 数 | 请求/天 |
| --- | --- |
| 10 | 7,200 |
| 50 | 36,000 |
| 100 | 72,000 |

> 注意：这只是「每 root 1 个热目录、每目录 1 次请求」的下界。
> 多目录、大目录、Mutation Hint 突发会显著放大。且「1 次 refresh = 1 次 provider 请求」
> 对大目录**不成立**（§6.3）。

### 9.4 其它成本

- OpenList 侧：`refresh=true` 触发 `storage.List`，大目录一次性构建完整切片 →
  OpenList 进程内存/CPU 放大。
- 115 侧：单目录列表分页 + 客户端 2 r/s 限流 → 大目录刷新耗时可能达数十秒。
- 官方 open API：单次 `limit` 最大 1150，同样分页；且限流细则未知。

---

## 10. Risk register

| # | Risk | Level | Evidence / Note |
| --- | --- | --- | --- |
| 1 | provider 限流 / 风控 | HIGH | 115 官方「频率控制细则不公开且动态优化」；保留限流/冻结/资质回收（DIRECT）。2 r/s 为 OpenList 客户端默认，非服务端承诺 |
| 2 | 私有/未公开 API 依赖 | HIGH | OpenList 115 driver 走 `webapi.115.com`（web 私有接口）+ Cookie/UA 伪装；官方《开发须知》禁止调用非公开接口（DIRECT） |
| 3 | 大目录请求放大 | MEDIUM-HIGH | 单目录刷新 ≈ ⌈N/page⌉ 次 provider 请求（DERIVABLE） |
| 4 | 最终一致性 | UNKNOWN | 上传 API 成功后 provider list 何时可见，官方无承诺；需 live test |
| 5 | token / auth 失败 | MEDIUM | access_token 7200s；refresh 轮换；终态错误 40140116/19/20/37 必须停止重试（DIRECT） |
| 6 | Token 失败被误当空目录 | 设计风险 | 若 refresh 失败：IndexCore 必须保留旧 canonical truth，绝不推断为「目录为空」（Blueprint §17 fail-closed；INV-003/022） |
| 7 | 多 root 共用账号 | MEDIUM | 请求量按账号聚合，放大风控暴露（DERIVABLE） |
| 8 | 删除事件可信度 | HIGH | 无 native delete feed；回收站列表只能轮询比对，且 move/rename 不在其中（DIRECT）。首次实现不得把 provider delete 直接变 canonical REMOVED（Blueprint §14） |
| 9 | 遍历错误静默吞没（OpenList/AList） | HIGH | `storage.List` 出错时可能返回部分数据 + HTTP 200（D02 DIRECT） |
| 10 | dirty scope 爆炸 | MEDIUM | 阈值超出应转 FULL_RESYNC_REQUIRED（Blueprint §15） |
| 11 | 缓存语义漂移 | MEDIUM | 直接依赖 OpenList 缓存行为，版本升级可能变化（`CustomCachePolicies` 等为近期新增） |
| 12 | 许可证 | MEDIUM | OpenList/AList AGPL-3.0；以外部进程/HTTP 边界使用而非链接/复制（D02/D03 策略） |

---

## 11. Duplicate-wheel analysis

分类：`USE_EXISTING` / `WRAP_EXISTING` / `ADAPT` / `CLEAN_ROOM_REWRITE` / `BUILD_NEW` / `DO_NOT_BUILD`

| Capability | OpenList/AList 已解决 | rclone 已解决 | 115 官方 SDK/API | IndexCore 候选归属 |
| --- | --- | --- | --- | --- |
| provider 登录 / token / Cookie | ✅（含 115 Cookie/QR） | ✅（多 backend） | ✅ OAuth2 | **USE_EXISTING**（不要自建 115 登录） |
| 分页 / driver 差异 | ✅ | ✅ | 官方分页 | **USE_EXISTING** |
| 目录级强制刷新（绕过缓存） | ✅ `refresh=true` | 部分（重新 List） | N/A | **WRAP_EXISTING** |
| 目录缓存 / per-path TTL | ✅（+ `CustomCachePolicies`） | ✅ DirCache | — | **USE_EXISTING** |
| native change feed / cursor | ❌ | ❌（仅 backend-dependent ChangeNotify） | ❌ | **DO_NOT_BUILD**（无需求依据） |
| 稳定对象 ID | AList 部分 / OpenList ❌ | backend-dependent | ✅ `fid` | **USE_EXISTING / ADAPT**（按 provider 能力） |
| 变更发现编排 / dirty scope | ❌ | ❌ | ❌ | **BUILD_NEW（IndexCore 侧，薄层）** |
| cursor/continuity/fail-closed 语义 | ❌ | ❌ | ❌ | **BUILD_NEW**（若走 native 才需要；当前无 native） |
| canonical identity / reconcile / journal | ❌ | ❌ | ❌ | **USE_EXISTING（Kernel，已冻结）** |
| provider 错误传播 / completeness | 弱（静默吞没） | 较强（传播 error） | — | 由 Collector 边界承载，Kernel 不重遍历（INV-023） |

### 11.1 直接结论

- **Provider 登录、token refresh、分页、driver 差异、目录缓存、scoped refresh** 这些
  「成熟轮子」已被 OpenList/AList（115）或 rclone 解决，**IndexCore 默认不要重造**。
- IndexCore 真正需要自己拥有的，是**变更发现策略的编排 + dirty scope + 安全 fallback**，
  以及（未来若真有 native feed 时的）cursor continuity 语义——这是一层薄逻辑，
  **不是** 又一个 provider client。

---

## 12. 115 specific conclusion

| 能力 | 判定 |
| --- | --- |
| 官方 change feed / delta / cursor / recent changes | **UNAVAILABLE**（官方 API 全量核对） |
| 官方 webhook / callback / SSE | **UNAVAILABLE** |
| 官方稳定对象 ID | **DIRECT**（`fid` + `pid`） |
| 官方目录级 list（可作 scoped refresh 基础） | **DIRECT**（`cid` + `offset/limit`，limit ≤ 1150） |
| 官方按时间过滤 | **DIRECT 但不足**（`o=user_utime` 排序；`search` 的 `gte_day/lte_day` 为天粒度且需关键词） |
| 官方限流细则 | **UNKNOWN**（明确不公开） |
| 官方接入门槛 | 需 OAuth 授权；接入指南要求合规、禁止非公开接口（DIRECT） |
| OpenList 115 支持所依赖的接口性质 | **私有 web API（`webapi.115.com`）**，非官方 open API（DIRECT） |
| OpenList scoped refresh 对 115 是否可行 | **可行**（`refresh=true` 触发 115 driver 真实列表） |
| OpenList scoped refresh 是否便宜 | **取决于目录大小**，大目录线性放大（DERIVABLE） |

**115 最合适的路径候选（供 Architect 判定，非结论）：**
由于 115 无 native delta，候选落在 **Mutation Hint + Scoped Refresh（经 OpenList `refresh=true`）
+ Adaptive Polling + Full Scan fallback**。若选择完全不依赖私有接口，则只能走**官方 open API**
自行实现 115 adapter（且仍无 delta，只是 scoped list）。

---

## 13. OpenList / AList specific conclusion

- `/api/fs/list` + `refresh=true` 是**已被源码证明**的「绕过目录缓存、直达 provider」手段；
  能实现「比普通 AList/OpenList 缓存更早可见」。
- **只刷新单目录、不递归**；空结果删子树缓存；非空覆盖该目录缓存。
- 支持 storage 级 `NoCache` / `CacheExpiration`，以及 OpenList 的 per-path `CustomCachePolicies`。
- 自身写操作会维护缓存；**外部变更不会**。
- 无 native delta / webhook / cursor。
- 115 支持依赖**私有接口 + 社区库**（风险 HIGH）。
- 大目录刷新 = 多次 provider 请求 + OpenList 内存放大。

---

## 14. rclone specific conclusion

- 提供可选 `ChangeNotifier`（`fs/features.go:569-581`），**polling 非 webhook**；
  D03 证实 14/69 backend 实现。
- **无 115 backend** → 对本项目主场景不适用。
- 不能提供跨 backend 稳定 identity；不能防 silent backend truncation（D03）。
- 无 scan checkpoint/resume 持久化；RC 不返回 structured skipped-set（D03）。
- 定位：可作为**其它 provider** 的候选 Collector（DRIVER_DEPENDENT），
  但不是 115 增量发现的答案。

---

## 15. UNKNOWNs requiring live tests

1. **最终一致性**：上传/移动成功后，115 侧 list 何时可见？（决定 Mutation Hint 的 retry/backoff 与可达到的延迟下限）
2. **OpenList 115 driver 的外部变更可见延迟**：`refresh=true` 后，外部上传在新会话中多久出现？
3. **115 服务端限流阈值**：在何种 QPS/日请求量下触发限流或风控？（官方不公开）
4. **大目录实际耗时/请求数**：10k/30k 目录一次 `refresh=true` 的真实 provider 请求数与墙钟时间。
5. **AList/OpenList 缓存 TTL 的默认与实际生效值**（storage 配置差异）。
6. **回收站轮询**能否可靠发现删除（含 move/rename 是否出现）。
7. **多 root 共享账号**下的实际风控表现。
8. **115 官方 open API** 的实际限流、`user_utime` 排序在增量识别中的可用性与精度。
9. OpenList 版本升级对缓存语义（`CustomCachePolicies`、失效规则）的稳定性影响。

---

## 16. Live test plan（仅在 Architect 授权且有可控账号时执行）

> 本阶段禁止对生产账号压测。以下为**候选**测试设计，需显式请求预算与账号隔离。

| Test | 目标 | 最小设计 | 预算上限 |
| --- | --- | --- | --- |
| LT-1 cache vs forced refresh | 证明 `refresh=true` 能提前看到外部新文件 | 外部上传 → 分别 `refresh=false`/`true` 观测可见时间 | 数十次请求 |
| LT-2 provider 一致性曲线 | 上传成功到 list 可见的延迟分布 | 已知 mutation 后按 1s/5s/10s… 轮询观测 | 单次实验 <100 请求 |
| LT-3 大目录刷新成本 | 量化 10k/30k 目录 refresh 的请求数与耗时 | 观测 provider 请求计数 + 墙钟 | 每个规模 1–3 次 |
| LT-4 无变化轮询 | 24h 无变化时 refresh 的实际代价与是否触发限流 | 固定间隔低 QPS 连续观测 | 需明确总请求预算 |
| LT-5 token 失效 | refresh 失败/终态错误的可观测信号 | 使用可丢弃的授权 | 低 |
| LT-6 多 root 共账号 | 请求聚合对风控的影响 | staging 账号 | 需 Architect 明确上限 |

**执行前置**：显式授权、专用测试账号/目录、请求预算、可回滚；不得使用生产 115 账号。

---

## 17. Final options（供 Architect 决策）

Blueprint §12 要求 D0 结束时由 Architect 从以下选择其一：

```text
STOP
PROTOTYPE_MUTATION_HINT
PROTOTYPE_NATIVE_DELTA
PROTOTYPE_SCOPED_REFRESH
PROTOTYPE_HYBRID
KEEP_FULL_SCAN_ONLY
```

本报告的证据含义（**不构成建议**）：

- `PROTOTYPE_NATIVE_DELTA` — 对 115 **无证据支持**（UNAVAILABLE）；仅当先选定一个明确
  支持可信 change feed 的其它 provider 时才可能有意义。
- `PROTOTYPE_SCOPED_REFRESH` — 有源码级 DIRECT 证据支持其**技术可行性**；
  但请求放大、私有接口风险、最终一致性仍待 live test。
- `PROTOTYPE_MUTATION_HINT` — 只覆盖「已知写入」，须与轮询/验证配合。
- `PROTOTYPE_HYBRID` — 覆盖最完整，但复杂度与请求预算最高。
- `KEEP_FULL_SCAN_ONLY` / `STOP` — 在请求放大或风控不可接受时的有效退路。

**Whether to proceed, which option, and what latency target to freeze is an
Architect decision. This report does not select a strategy and does not
authorize implementation.**

### 17.1 What this report does NOT claim

- 不声称 115 有 cursor / change feed（已证无）。
- 不声称 `refresh=true` 安全或廉价（大目录会放大）。
- 不声称一次目录刷新 = 一次 provider 请求（不成立）。
- 不声称 2 分钟轮询合理（需预算与 live test）。
- 不声称删除事件可信（首次实现不得直接 canonical 删除）。
- 不声称 rclone 所有 backend 行为一致（DRIVER_DEPENDENT）。
- 不替 Architect 决定最终方案。
</content>
</invoke>
