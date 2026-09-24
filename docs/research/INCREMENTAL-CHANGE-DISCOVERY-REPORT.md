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
   → 截至 2026-09-24，在**已审计的公开 115 Open API 表面**中**未发现** native delta /
   change cursor（证据级别：DIRECT；不排除存在未公开/未文档化能力）。

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

5. **OpenList/AList 同时提供两条 115 接入路径，其中一条走 115 官方开放平台 API（Round 1 修正）。**
   - **方案 A「115 Cloud」**：社区库 `SheltonZhu/115driver` + `webapi.115.com` 私有接口 +
     Cookie/二维码；OpenList 官方文档自述为 "reverse-engineered interface based on legacy
     products"；115 官方《开发须知》禁止调用非公开接口 → 风险 **HIGH**、复用价值有限。
   - **方案 B「115 Open」**：走 **115 官方开放平台 API**（`proapi.115.com/open/*`），
     经**社区维护的 SDK wrapper**（OpenList `OpenListTeam/115-sdk-go`；AList `xhofe/115-sdk-go`）
     调用，认证为 OAuth2（AccessToken/RefreshToken）；OpenList 官方文档标注为
     "115 Open Platform API / Official Open API" → **风险为正常官方 API 风险、复用价值高**。
     注意：wrapper 由 OpenListTeam / xhofe 维护，**不是 115 官方发布的 SDK**（Round 2 修正）。
   → 「为避开私有接口只能自己重写 115 adapter」**不成立**：成熟轮子（官方 API + 社区 wrapper）已存在。
   （证据级别：DIRECT）

6. **Xiaoya（小雅）是「预生成清单 + 客户端 diff」的成熟实践，不是 provider-native change feed。**
   `.scan.list.gz` manifest 提供 path + 分钟级时间戳；`Last-Modified` 定义 generation；
   30 分钟跳过下载；`xiaoya-emby`（MIT）用「双 mirror 内容一致才清理 + 连续两代缺席才删除」做删除保护。
   → 证明「轻量 manifest + diff」可大幅降低变更发现成本，但**依赖外部生成端**
   （生成器源码未找到）。（证据级别：DIRECT / 上游 UNAVAILABLE）

7. **rclone 提供可选 `ChangeNotify` 接口（14/69 backend，全部 polling，非 webhook），但没有 115 backend。**
   → 对 115 不适用；对其它 backend 为 DRIVER_DEPENDENT。（证据级别：DIRECT）

8. **没有任何被审查来源（115 官方 / OpenList / AList / Xiaoya / rclone / fsspec）提供跨 provider 的
   native delta + durable cursor。**
   → Native Delta 只有在「某个 provider 明确暴露可信 change feed」时才可能落地，
   对 115 而言当前 **不可行**。

9. 当前证据支持的候选方向是 **Hybrid（Mutation Hint + Scoped Refresh + Adaptive Polling +
   Full Scan fallback）**，但：
   - 最终是否采用、先做哪条、SLA 定多少，**由 Architect 决定**；
   - 上传成功后 provider list 何时可见（最终一致性）仍为 **UNKNOWN，需要 live test**。

10. **（Round 2 新增）不能把 `refresh:false` 简单改成 `refresh:true` —— 会产生请求量爆炸。**
    IndexCore adapter 对**每个 HTTP page** 都请求一次 `/api/fs/list`
    （`pageSize=1000`，`internal/collector/alist/adapter.go:126-152`）。若每页都带
    `refresh=true`，则每页都会触发对**整个目录**的 provider 强制刷新：
    30k 目录 = ceil(30000/1000)=30 页 × 每次刷新约 150 次 provider 请求（115 Open 默认
    page 200）≈ **4500 次真实 provider 请求**。**正确候选语义**应为：
    **第 1 页 `refresh=true`（强制刷新一次整个目录并写入 OpenList cache），
    第 2~N 页 `refresh=false`（只从刚刷新的 cache 分页）**。
    （证据级别：DIRECT 源码 + DERIVABLE 推算）

11. **（Round 2 新增）`refresh=true` 需要 OpenList 侧写权限。**
    `server/handles/fsread.go:96-99`：`if req.Refresh && !canWriteContentAtPath` →
    403 `Refresh without permission`。因此 Scoped Refresh 要求 IndexCore 在 OpenList 的
    service account 具备 **write-capable** 权限（即便 IndexCore 本身不修改 provider）。
    建议：专用 service account / 最小权限 / 不暴露浏览器 / 不调用任何 mutation endpoint。
    （证据级别：DIRECT）

12. **（Round 2 新增）`fid` 未透传到 IndexCore。**
    115 Open 有稳定 `fid`，但链路 `115 Open → OpenList → /api/fs/list → IndexCore` 中，
    OpenList 的 `/api/fs/list` 响应（`toObjsResp`，`server/handles/fsread.go:228-248`）
    **不包含 provider `fid`**；IndexCore adapter 只解析
    `name/size/is_dir/modified/type/hash_info`（`adapter.go:208-213`）。
    → Scoped Refresh 能让 IndexCore **更快发现新增/修改**，但**不会自动获得更强的 provider ID
    身份能力**；move/rename 仍主要依赖现有 identity evidence 规则。（证据级别：DIRECT）

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
| 115 Open SDK wrapper（**社区维护**）— `OpenListTeam/115-sdk-go` | OpenList `go.mod` = `v0.2.6` | 2026-09-24 | DIRECT |
| 115 Open SDK wrapper（**社区维护**）— `xhofe/115-sdk-go` | AList `go.mod` = `v0.1.5` | 2026-09-24 | DIRECT |
| OpenList 项目文档（115 Open vs 115 Cloud 定性） | `doc.oplist.org/guide/drivers/115_open`、`/115` | 2026-09-24 | DIRECT |
| rclone | `cfb90e3ebed479119718e3ae44b1171b060079e9`（D03 记录）；本次复核 `fs/features.go` | 2026-09-24 | source |
| Xiaoya — `universonic/xiaoya-emby` | `2af6db2dd8511e4591e6a69b90604f2b3bd8a74c`（2026-09-13） | 2026-09-24 | source |
| Xiaoya — `xiaoyaDev/xiaoya_emd_go` | `3161a980453d87281198ae009290950cc0ed55ec`（2026-01-01） | 2026-09-24 | source |
| Xiaoya — `xiaoyaDev/xiaoya_db` | D01 记录；本轮引用其 `solid.py` 行号 | 2026-09-24 | source |
| Xiaoya — 上游生成器 | `index.zip` / `update.zip` / `.scan.list.gz` 生成端检索无结果 | 2026-09-24 | UNAVAILABLE |
| License | OpenList/AList = AGPL-3.0；115driver = 见其仓库；115-sdk-go (`OpenListTeam`/`xhofe`) = 见其仓库；rclone = MIT；fsspec = BSD-3；xiaoya-emby = MIT；xiaoya_emd_go = GPL-3.0；xiaoya_db = 无 LICENSE | 2026-09-24 | DIRECT |

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
| rate-limit / quota documentation | 「对所有 API 实施频率控制，细则**不公开**」 | 客户端 `LimitRate`（**115 Cloud 默认 2 r/s、115 Open 默认 1 r/s**）+ 服务端 Unknown | rclone `--tpslimit` 等本地限流 | 官方=UNKNOWN / driver=DIRECT | 见 §10 |
| token refresh failure semantics | OAuth2：7200s、refresh 轮换、终态错误码 40140116/19/20/37 | Cookie 失效 → 错误 | rclone 各 backend 不同 | DIRECT（官方）/ DRIVER_DEPENDENT | 见 §10 |
| search / by-time filtering | `ufile/search` 支持 `gte_day`/`lte_day`（天粒度），需 `search_value`/`file_label` | 搜索走内部索引，非 provider 全量 | — | DIRECT | 不足以替代 change feed |

### 3.1 Xiaoya（成熟实践参考，非 provider 能力）

Xiaoya **不是** provider 接口能力的来源，而是「预生成清单 + 客户端 diff」的成熟工程实践参考：

| 实践 | 内容 | Evidence |
| --- | --- | --- |
| 轻量 manifest | `/.scan.list.gz`：path + 分钟级时间戳；无 size/hash/id | DIRECT |
| generation 判断 | HTTP `Last-Modified` 定义 manifest 代；只取最新代 mirror | DIRECT |
| 低成本跳过 | `Last-Modified` 与本地时间差 ≤ 30 分钟则跳过下载 | DIRECT |
| file-level diff | 本地文件集 vs manifest 条目集；per-row time base + content identity | DIRECT |
| 删除保护 | 双 mirror 内容一致才清理；连续两代缺席才删除；>50% / ≥20 文件门控 | DIRECT |
| 上游生成器 | 未找到 | UNAVAILABLE |

详见 §14。

### 3.2 115 的两条接入路径（Round 1 新增）

| Capability | 115 Cloud（`drivers/115`） | 115 Open（`drivers/115_open`） | Evidence |
| --- | --- | --- | --- |
| API 类型 | 私有 / 逆向 | **官方开放平台** | DIRECT |
| SDK | ❌（社区逆向库 `115driver`） | ✅ **社区维护 wrapper**（`OpenListTeam/115-sdk-go` / `xhofe/115-sdk-go`，调用官方 Open API） | DIRECT |
| 认证 | Cookie / 二维码 | OAuth2 AccessToken+RefreshToken | DIRECT |
| Scoped Refresh（`refresh=true`） | ✅ | ✅ | DIRECT |
| PageSize 默认/上限 | 1000 / 1150 | 200 / 1150 | DIRECT |
| 客户端限速默认 | 2 r/s | 1 r/s | DIRECT |
| 复用价值 | 有限（风险 HIGH） | **高**（正常官方风险） | DIRECT |

IndexCore 直连 115 Open（不经 OpenList/AList）：技术上可行，但需自行实现 OAuth/token 轮换/
分页/driver 兼容 → **默认 DO_NOT_BUILD**（重复造轮子；社区 SDK wrapper 已存在且已被 OpenList/AList 集成）。

详见 §6.2 与 §12。

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

结论：**截至 2026-09-24，在已审计的公开 115 Open API 表面中未发现 native delta /
change cursor**（不排除未公开能力）。

### 5.2 OpenList / AList

无 `since` / `cursor` / watch / SSE / webhook。变更通知只有内部 Go hook
（`HandleObjsUpdateHook`，用于驱动内部搜索索引），不对外提供（DIRECT，见 D02 报告）。

### 5.3 rclone

存在可选 `ChangeNotifier` 接口（`fs/features.go:569-581`），语义为：
实现（可为 polling）在对端发生变更时调用回调；文档明确「若使用 polling 应遵守给定
interval」。D03 已证实：14/69 backend 实现，**全部为 polling，不是 webhook**；且
**rclone 没有 115 backend**（`rclone.org/115` = 404）。
→ 对 115 不适用；对其它 backend 属于 DRIVER_DEPENDENT。

### 5.4 Xiaoya（不是 provider change feed）

- Xiaoya 的 `.scan.list.gz` 是**服务端预生成的静态清单**，不是 provider 暴露的 change feed；
  它不携带 create/update/move/delete 事件语义，也不提供 cursor / replay。
- 其「generation」是发布方的 `Last-Modified`，只能回答「清单整体是否更新」，
  不能回答「provider 自某点以来发生了什么」。
- 因此 Xiaoya 的增量能力**不能**计入 provider-native delta；它属于 §14 的「成熟实践参考」。
- 结论：**不改变本节「截至 2026-09-24 未发现 native delta」的判定。**

### 5.5 Overall

**截至 2026-09-24，没有任何被审查来源为 115 提供可信的 native delta / cursor**（公开、已审计范围内）。
即便未来某个 provider 支持，也必须逐 provider 验证 cursor 的持久性、可 replay 性、
过期/重置、gap 检测、retention、create/update/move/delete 完整度（blueprint §17 的 fail-closed 要求）。

---

## 6. Candidate strategy C — Scoped Refresh（本轮最关键方向）

> **Round 1 修正（2026-09-24）**：OpenList/AList 同时提供**两条 115 接入路径**；
> 本报告此前只覆盖了私有路径（方案 A），遗漏了官方路径（方案 B）。`refresh=true`
> 是 OpenList/AList 的**上层缓存语义**，与底层走哪个 driver 无关。

### 6.1 结构：IndexCore 复用 OpenList/AList，而非直连 provider

```text
IndexCore
   ↓
OpenList / AList HTTP  (/api/fs/list?refresh=true)
   ↓
storage driver
   ├─ 方案 A：115 Cloud  (drivers/115)      → 旧私有/逆向 API  → 风险 HIGH
   └─ 方案 B：115 Open   (drivers/115_open) → 官方开放平台 API → 重点复用候选
```

### 6.2 方案 A / 方案 B 对照（Architect Round 1 要求）

| 维度 | 方案 A：115 Cloud | 方案 B：115 Open |
| --- | --- | --- |
| driver | `drivers/115`（`Name: "115 Cloud"`） | `drivers/115_open`（`Name: "115 Open"`） |
| **API 类型** | **私有 / 逆向**（`webapi.115.com`） | **官方开放平台**（`proapi.115.com/open/*`） |
| OpenList 官方文档定性 | "reverse-engineered interface based on legacy products；项目组不会主动维护，请勿提逆向相关 issue" | "**115 Open Platform API** / Official Open API" |
| 认证 | Cookie / 二维码 + `115Browser` UA | **OAuth2** `AccessToken` + `RefreshToken`（SDK 自动轮换） |
| OpenList 使用库 | 社区逆向库 `SheltonZhu/115driver v1.3.5` | **社区维护 SDK wrapper** `OpenListTeam/115-sdk-go v0.2.6`（调用 115 官方 Open API） |
| AList 使用库 | `SheltonZhu/115driver`（replace `okatu-loli/115driver`） | **社区维护 SDK wrapper** `xhofe/115-sdk-go v0.1.5` |
| 列表 API | `webapi.115.com/files` | 官方 `GetFiles`（`/open/ufile/files`） |
| **Scoped Refresh（`refresh=true`）** | **可以** | **可以** |
| 客户端限速默认 | **2 r/s** | **1 r/s** |
| PageSize 默认 / 上限 | **1000** / 1150 | **200** / 1150 |
| token 失效语义 | Cookie 失效（无官方终态码） | 官方终态码 40140116/19/20/37 |
| **风险** | **高**（私有接口；官方《开发须知》禁止调用非公开接口） | **正常官方 API 风险**（限流细则仍不公开） |
| **复用价值** | **有限** | **高** |

### 6.3 OpenList `/api/fs/list` 调用链

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
daemon driver（drivers/115 = Cloud，或 drivers/115_open = Open）→ 115 API
```

证据：`internal/op/fs.go:31-122`；`server/handles/fsread.go:79-118`。

### 6.4 Answers to the twelve required questions

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

### 6.5 大目录成本与 refresh 分页陷阱（拆分 Cloud vs Open；Round 2 扩展）

两条路径都是**「循环分页拉全目录」**模型，但分页大小与限速不同：

**方案 A（115 Cloud）** — `drivers/115/meta.go`：`PageSize` 默认 **1000**，`LimitRate` 默认 **2 r/s**；
`drivers/115/util.go:62` → `SheltonZhu/115driver` 的 `ListWithLimit`（`dir.go:38-84`）
从 `offset=0` 循环分页（上限 `MaxDirPageLimit = 1150`），直到取完。

**方案 B（115 Open）** — `drivers/115_open/meta.go`：`PageSize` 默认 **200**（上限 **1150**），
`LimitRate` 默认 **1 r/s**；`drivers/115_open/driver.go:103-133` 的 `List` 循环调用官方
`sdk.Client.GetFiles({CID, Limit, Offset, ASC, O, ShowDir})`，`len(res) >= resp.Count` 时停止。
（证据级别：DIRECT）

含义：

```text
一次 refresh=true（某目录）≈ ⌈N / page_size⌉ 次 provider 列表请求（N = 该目录直接子项数）
```

| 目录直接子项 N | 方案 A：Cloud（page 1000） | 方案 B：Open（page 200） | 方案 B：Open（page 调到 1150） |
| --- | --- | --- | --- |
| 1,000 | 1 | 5 | 1 |
| 10,000 | 10 | 50 | 9 |
| 30,000 | 30 | **150** | **27** |

> **Round 1 修正**：30k 大目录在**默认配置**下，115 Open（page 200 / 1 r/s）成本显著高于
> 115 Cloud（page 1000 / 2 r/s）：约 150 次请求且受 1 r/s 限速，墙钟可能达分钟级；
> 把 Open 的 `page_size` 调到 1150 可降到约 27 次。**两者都可配置，成本差异取决于配置。**
> 红线不变：不能假设「一次 scoped refresh = 1 次 provider 请求」。（证据级别：DERIVABLE）

#### 6.5.1 分页陷阱（Round 2 新增，最关键）

IndexCore adapter 对**每一个 HTTP page** 都发一次 `/api/fs/list`：

```text
internal/collector/alist/adapter.go
  const pageSize = 1000                                          // :126
  for page := 1; ; page++ {                                      // :129
      items = listPage(ctx, token, dir, page, pageSize)         // 每页一次 /api/fs/list
  }
  listPage body: {"path","password","page","per_page","refresh":false}   // :152
```

因此：

- **错误做法**：把 `refresh:false` 全量改成 `refresh:true` →
  **每一页**都会触发**整个目录**的 provider 强制刷新。
  30k 目录 = 30 页 × ~150 次/刷新（115 Open 默认 page 200）≈ **4500 次真实 provider 请求**。
- **正确候选语义**：
  1. **第 1 页** `refresh=true` → 强制刷新一次整个目录，并写入 OpenList cache；
  2. **第 2~N 页** `refresh=false` → 只从刚刷新的 cache 分页读取。

  这样 30k 目录是 **~30 次 HTTP 调用 + 1 次完整 provider 刷新（~150 次 provider 请求）**，
  而不是 30 × 150。

> 该语义属**候选**（是否采用由 Architect 决定）；目的是避免把「减少真实网盘请求」做成
> 「请求量暴涨几十倍」。（证据级别：DIRECT 源码 + DERIVABLE）

#### 6.5.2 分页一致性 / 快照同代性（Round 3 新增，UNKNOWN / prototype concern）

即使采用「第 1 页 `refresh=true`、其余页 `refresh=false`」，仍存在一个**尚未证明**的竞态：

```text
第 1 页 refresh=true → 把目录刷新到 OpenList cache（代 D1）
        ↓
IndexCore 依次读取第 2、3、…、N 页（refresh=false，命中 cache）
        ↓
期间发生：另一个写操作 / cache invalidation / TTL 过期 / 另一次 refresh
        ↓
后续页可能来自不同代的 cache（D2），与前几页混合
```

- IndexCore 当前的 `len(all) == total`（`internal/collector/alist/adapter.go`）**只能证明
  最后一页的数量对得上**，**不能证明这 N 页来自同一代缓存**。
- 后果：Scoped Refresh 的「全量重扫」可能**拼出混代（部分旧代 + 部分新代）的目录视图**，
  使参与 reconcile 的 entry set 语义不成立。
- 这是 **UNKNOWN / prototype concern**（不是已知 bug，也尚未证明安全）：OpenList 的
  `dirCache` 是否在**一次多页读取期间**保证单目录快照一致性，**未从源码/文档得到保证**。
- 若未来采用该候选语义，必须验证或在应用层提供一致性边界（例如：一次性取全目录、
  纳入 generation/etag 校验、或检测 cache 变更后重试）。具体做法由 Architect 决定，
  本阶段只登记风险。

> Round 3 要求：正式登记为 UNKNOWN / prototype concern，并补 prototype/live 测试（见 §17 LT-7）。
> （证据级别：DIRECT 源码调用链 + UNKNOWN 一致性保证）

### 6.6 两条路径的 provider 来源与风险

**方案 A（115 Cloud，私有/逆向）**：

- 依赖社区库 `github.com/SheltonZhu/115driver v1.3.5`。
- 列表端点 `https://webapi.115.com/files`；写操作走 `webapi.115.com/files/*`、`aps.115.com`、`uplb.115.com`。
- 认证为 Cookie（`UID/CID/SEID/KID`）或二维码，UA 伪装 `115Browser/<ver>`。
- OpenList 官方文档自述为 **"reverse-engineered interface based on legacy products"**，
  且明确"项目组不会主动维护、请勿提逆向相关 issue"。
- 115 官方《开发须知》禁止「调用非公开接口」，保留限流/接口冻结/资质回收处置权。
→ 风险等级 **HIGH**；复用价值有限。（DIRECT）

**方案 B（115 Open，官方开放平台）**：

- 使用**社区维护的 SDK wrapper**（封装 115 官方 Open API，**非 115 官方发布**）：
  OpenList `github.com/OpenListTeam/115-sdk-go v0.2.6`；AList `github.com/xhofe/115-sdk-go v0.1.5`。
- 端点对应官方 `proapi.115.com/open/*`（`GetFiles` / `GetFolderInfo` / `UserInfo` / `OfflineTaskList`）。
- 认证为官方 OAuth2（`AccessToken` + `RefreshToken`），SDK 通过 `WithOnRefreshToken`
  回调自动轮换并保存新 token。
- OpenList 官方文档明确标注为 **"115 Open Platform API" / "Official Open API"**。
→ 风险为**正常官方 API 风险**（限流细则仍不公开）；**复用价值高**。（DIRECT）

**但 `fid` 不会经 OpenList 传到 IndexCore（Round 2 新增）**：链路为
`115 Open → OpenList → /api/fs/list → IndexCore`，而 OpenList 的 list 响应
（`toObjsResp`，`server/handles/fsread.go:228-248`）只含
`name / size / is_dir / modified / created / hashinfo / sign / thumb / type / mount_details`，
**不含 provider `fid`**；IndexCore 侧也只解析 `name/size/is_dir/modified/type/hash_info`
（`adapter.go:208-213`）。→ Scoped Refresh 能让 IndexCore **更快发现新增/修改**，
**但不会自动获得更强的 provider ID 身份能力**；move/rename 仍主要依赖既有 identity evidence
规则处理。（证据级别：DIRECT）

### 6.7 IndexCore 现状缺口（Round 1 新增，DIRECT）

Architect 指出「上游具备强制刷新能力 ≠ IndexCore 现在已经会用」，核实如下：

- IndexCore `internal/collector/alist/adapter.go:152` **硬编码 `"refresh": false`**：

  ```go
  body, _ := json.Marshal(map[string]any{"path": dir, "password": "", "page": page, "per_page": perPage, "refresh": false})
  ```

- 含义：即使把 storage 配置为 **115 Open 官方 driver**，IndexCore 当前 adapter 请求
  `/api/fs/list` 时仍带 `refresh=false`，即**始终读 OpenList/AList 目录缓存**，
  不会触发对 115 的强制刷新。
- 因此「方案 B 可复用该社区维护驱动」是**上游能力事实**；要真正受益，IndexCore adapter 侧
  还需具备触发 `refresh=true` 的能力（是否启用、触发策略、权限与风险控制由 Architect 决定）。
- 本阶段**不修改代码**，仅记录该缺口（证据级别：DIRECT，代码位置已核实）。

**`refresh=true` 的权限前提（Round 2 新增）**：`server/handles/fsread.go:96-99` 要求调用者
满足 `canWriteContentAtPath`，否则返回 **403 `Refresh without permission`**。因此即便 IndexCore
不修改 provider，做 Scoped Refresh 时其 OpenList **service account 必须具备 write-capable 权限**。
推荐：**专用 service account + 最小权限 + 不暴露浏览器 + 不调用任何 mutation endpoint**。
（证据级别：DIRECT）

### 6.8 AList 对照

AList 与 OpenList 行为基本一致：`refresh=true` 绕过 cache 且需写权限；cache TTL 默认 30 分钟；
无 native delta；且**同样同时提供 `115`（Cloud）与 `115_open`（Open）两个 driver**。
本报告补充 OpenList 新增的 `CustomCachePolicies` 与更细的缓存失效语义。（DIRECT / D02 复用）

---

## 7. Candidate strategy D — Adaptive Polling

### 7.1 Fact base

- 不存在 provider 侧推送；发现只能靠**主动 list**（轮询）。（DIRECT）
- `refresh=true` 会绕过缓存，因此「轮询 + refresh=true」= 每次都真实打 provider。
- 不传 `refresh=true` 的轮询会命中缓存，**不会**提前发现（直到 TTL 过期）。（DIRECT）

### 7.2 Research questions

| Question | Finding | Level |
| --- | --- | --- |
| safe request budget per account/root | 115 官方未公开限流细则；OpenList 客户端默认 **115 Cloud 2 r/s / 115 Open 1 r/s** | UNKNOWN（官方）/ DIRECT（driver） |
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

假设：对某目录做一次 `refresh=true` 的 provider 请求数 ≈ `ceil(N / page)`。
**Round 1 修正**：Cloud 与 Open 的分页大小、客户端限速不同，必须分开算。
**Round 2 修正**：还要乘以 **IndexCore 的 HTTP 分页次数**——`refresh=true` 只应放在第 1 页
（见 §6.5.1），后续页必须 `refresh=false`，否则总请求数会按页数倍增。

### 9.1 单次刷新（拆分 Cloud / Open）

| 场景（直接子项 N） | 115 Cloud（page 1000，2 r/s） | 115 Open 默认（page 200，1 r/s） | 115 Open（page 1150，1 r/s） |
| --- | --- | --- | --- |
| 小目录（<1000） | 1 | ≤5 | 1 |
| 10k | ~10 | ~50 | ~9 |
| 30k | ~30 | **~150** | **~27** |

> 默认配置下 115 Open 的单目录刷新请求数约为 Cloud 的 5 倍，且限速 1 r/s（Cloud 2 r/s），
> 30k 目录刷新墙钟可能达分钟级；把 Open 的 `page_size` 调到上限 1150 可显著降低请求数。
> 两条路径的 `page_size` / `limit_rate` 均可配置。（DIRECT 源码 + DERIVABLE）

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
- 115 Cloud：客户端默认 2 r/s，大目录刷新耗时可能达数十秒。
- 115 Open：客户端默认 1 r/s、page 默认 200；服务端限流细则未知（官方不公开）。

### 9.5 分页陷阱对照（30k 目录，115 Open 默认 page 200，Round 2 新增）

| 策略 | provider 请求数 | 说明 |
| --- | --- | --- |
| 每页 `refresh=true`（**错误**） | **~4,500** | 30 页 × ~150 次/完整刷新 |
| 第 1 页 `refresh=true`，其余 `refresh=false`（**正确候选**） | **~150** + 30 次缓存分页 | 1 次完整刷新 + 30 次 HTTP 分页 |

> 结论：Scoped Refresh 的收益成立的前提是「**只刷新一次**、其余页读缓存」；
> 简单把参数全量改成 `refresh=true` 会把收益反转为数十倍放大。（DIRECT 源码 + DERIVABLE）

---

## 10. Risk register

| # | Risk | Level | Evidence / Note |
| --- | --- | --- | --- |
| 1 | provider 限流 / 风控 | HIGH | 115 官方「频率控制细则不公开且动态优化」；保留限流/冻结/资质回收（DIRECT）。客户端默认限速 **115 Cloud = 2 r/s、115 Open = 1 r/s**，均非服务端承诺 |
| 2 | 私有/未公开 API 依赖（**仅方案 A**） | HIGH | 115 Cloud driver 走 `webapi.115.com`（web 私有接口）+ Cookie/UA 伪装；OpenList 官方文档自述 reverse-engineered；官方《开发须知》禁止非公开接口（DIRECT）。**方案 B（115 Open）用官方 API，可规避此项** |
| 2b | 官方 API 限流 / 配额（方案 B） | MEDIUM | 115 Open 用官方开放平台；限流细则官方不公开，仍受账号风控（DIRECT / UNKNOWN） |
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
| 13 | `refresh=true` 需 OpenList 写权限 | MEDIUM | 403 `Refresh without permission`（`fsread.go:96-99`）；service account 需 write-capable；建议专用账号 + 最小权限 + 不调 mutation endpoint（DIRECT，Round 2 新增） |
| 14 | 每页 `refresh=true` 请求量爆炸 | HIGH | 30k 目录 ≈4500 次 provider 请求；须第 1 页 refresh、其余页读缓存（§6.5.1，Round 2 新增） |

---

## 11. Duplicate-wheel analysis

分类：`USE_EXISTING` / `WRAP_EXISTING` / `ADAPT` / `CLEAN_ROOM_REWRITE` / `BUILD_NEW` / `DO_NOT_BUILD`

| Capability | OpenList/AList 已解决 | rclone 已解决 | 115 Open（官方 API / 社区 wrapper） | IndexCore 候选归属 |
| --- | --- | --- | --- | --- |
| provider 登录 / token / Cookie | ✅（115 Cloud Cookie/QR） | ✅（多 backend） | ✅ OAuth2 | **USE_EXISTING**（不要自建 115 登录） |
| 115 官方 OAuth / token 轮换（方案 B） | ✅ `115_open` driver | ❌（无 115 backend） | ✅ 社区 SDK wrapper（调用官方 Open API） | **USE_EXISTING / WRAP_EXISTING**（IndexCore 不自写 115 Open client；直连默认 DO_NOT_BUILD） |
| 分页 / driver 差异 | ✅ | ✅ | 官方分页 | **USE_EXISTING** |
| 目录级强制刷新（绕过缓存） | ✅ `refresh=true`（对 Cloud/Open driver 都生效） | 部分（重新 List） | N/A | **WRAP_EXISTING**（IndexCore adapter 当前硬编码 `refresh:false`，见 §6.7） |
| 目录缓存 / per-path TTL | ✅（+ `CustomCachePolicies`） | ✅ DirCache | — | **USE_EXISTING** |
| native change feed / cursor | ❌ | ❌（仅 backend-dependent ChangeNotify） | ❌ | **DO_NOT_BUILD**（无需求依据） |
| 稳定对象 ID | AList 部分 / OpenList ❌ | backend-dependent | ✅ `fid` | **USE_EXISTING / ADAPT**（按 provider 能力） |
| 变更发现编排 / dirty scope | ❌ | ❌ | ❌ | **BUILD_NEW（IndexCore 侧，薄层）** |
| cursor/continuity/fail-closed 语义 | ❌ | ❌ | ❌ | **BUILD_NEW**（若走 native 才需要；当前无 native） |
| canonical identity / reconcile / journal | ❌ | ❌ | ❌ | **USE_EXISTING（Kernel，已冻结）** |
| provider 错误传播 / completeness | 弱（静默吞没） | 较强（传播 error） | — | 由 Collector 边界承载，Kernel 不重遍历（INV-023） |
| 预生成清单 + client-level diff（Xiaoya 参考实践） | ❌（不提供 provider 能力，仅模式参考） | ❌ | ❌ | **ADAPT / 借思想**（见 §14；依赖外部生成端） |

### 11.1 直接结论

- **Provider 登录、token refresh、分页、driver 差异、目录缓存、scoped refresh** 这些
  「成熟轮子」已被 OpenList/AList（115）或 rclone 解决，**IndexCore 默认不要重造**。
- IndexCore 真正需要自己拥有的，是**变更发现策略的编排 + dirty scope + 安全 fallback**，
  以及（未来若真有 native feed 时的）cursor continuity 语义——这是一层薄逻辑，
  **不是** 又一个 provider client。
- **基于 115 官方 Open API 的社区维护驱动已存在**（OpenList/AList 均集成社区 SDK wrapper）→
  对「避开私有接口」的目标，候选是**复用该驱动**而非自写 115 adapter；
  IndexCore 直连 115 Open 默认 **DO_NOT_BUILD**。
- Xiaoya 提供的是**消费端模式参考**（manifest / generation / diff / 跨代删除确认），
  不是可复用的 provider 能力；其**生成端未找到**，故「清单由谁生成」仍需 IndexCore 自己承担。

---

## 12. 115 specific conclusion

**Round 1 修正：115 必须按「两条接入路径」分别判定，而非笼统归到私有接口。**

### 12.1 方案 A / 方案 B 判定对照（Architect 要求）

| | 方案 A：115 Cloud | 方案 B：115 Open |
| --- | --- | --- |
| API 类型 | 私有 / 逆向 | **官方开放平台** |
| Scoped Refresh | **可以** | **可以** |
| 风险 | **高** | 正常官方 API 风险（限流细则不公开） |
| 复用价值 | **有限** | **高** |

### 12.2 IndexCore 自己直连 115 Open

| 维度 | 判定 |
| --- | --- |
| API 类型 | 官方（`proapi.115.com/open/*`） |
| 技术可行性 | **可以** |
| 重复造轮子成本 | 高（需自实现 OAuth / token 轮换 / 分页 / driver 兼容） |
| 默认归属 | **DO_NOT_BUILD**（社区 SDK wrapper 已存在且被 OpenList/AList 集成） |

### 12.3 官方能力判定（与所选路径无关）

| 能力 | 判定 |
| --- | --- |
| 官方 change feed / delta / cursor / recent changes | 截至 2026-09-24，**已审计的公开 API 表面未发现**（不排除未公开能力） |
| 官方 webhook / callback / SSE | **UNAVAILABLE** |
| 官方稳定对象 ID | **DIRECT**（`fid` + `pid`） |
| 官方目录级 list（scoped refresh 基础） | **DIRECT**（`cid` + `offset/limit`，limit ≤ 1150） |
| 官方按时间过滤 | **DIRECT 但不足**（`o=user_utime` 排序；`search` 的 `gte_day/lte_day` 为天粒度且需关键词） |
| 官方限流细则 | **UNKNOWN**（明确不公开） |
| 官方 scoped refresh 是否可行 | **可行**（`refresh=true` 经 115 Open driver 触发官方 list） |
| 官方 scoped refresh 是否便宜 | 取决于目录大小与 `page_size` / `limit_rate` 配置（§9.1） |

**115 最合适的路径候选（供 Architect 判定，非结论）：**
115 无 native delta，候选为 **Mutation Hint + Scoped Refresh（优先落到基于 115 官方 Open API 的社区维护驱动）
+ Adaptive Polling + Full Scan fallback**；同时 IndexCore adapter 需解决 `refresh:false` 缺口（§6.7）。
是否保留 115 Cloud 作为回退路径由 Architect 决定。

---

## 13. OpenList / AList specific conclusion

- `/api/fs/list` + `refresh=true` 是**已被源码证明**的「绕过目录缓存、直达 provider」手段；
  能实现「比普通 AList/OpenList 缓存更早可见」。
- **只刷新单目录、不递归**；空结果删子树缓存；非空覆盖该目录缓存。
- 支持 storage 级 `NoCache` / `CacheExpiration`，以及 OpenList 的 per-path `CustomCachePolicies`。
- 自身写操作会维护缓存；**外部变更不会**。
- 无 native delta / webhook / cursor。
- **115 支持有两条路径**：`115 Cloud`（私有/逆向，风险 HIGH，复用价值有限）与
  **`115 Open`（官方开放平台 + 社区 SDK wrapper，复用价值高）**；`refresh=true` 对两者都生效。
- 大目录刷新 = 多次 provider 请求 + OpenList 内存放大（Cloud/Open 分页与限速不同，见 §9.1）。

---

## 14. Xiaoya / xiaoya-* specific conclusion（成熟实践参考）

> 复用既有 `docs/research/XIAOYA-INDEX-ARCHITECTURE-REPORT.md`（Discovery 01）与
> `d01/W-A`、`d01/W-B`、`d01/W-D`。本轮**不重复调查「小雅是什么」**，只补「变更发现 / 增量」相关缺口。
> **小雅不是 provider-native change feed 的实现**；它是「预生成索引 / 清单分发 + 客户端 diff」的成熟实践，
> 用于回答：能否用轻量 manifest / version / scoped refresh 大幅降低变更发现成本。

### 14.1 index.zip / update.zip / .scan.list.gz 如何更新

| 文件 | 生成 / 发布方 | 客户端如何判断更新 | 是否增量 | 证据 |
| --- | --- | --- | --- | --- |
| `index.zip`（搜索索引） | 服务端预生成（生成器**未找到**） | 客户端比对 `version.txt`，变化则整包下载替换 | **否（全量替换）** | DIRECT（W-A: `service.sh` version 比对） |
| `update.zip`（AList `x_storages` 挂载 SQL） | 服务端预生成 | 同上（`version.txt`） | 否（整包替换） | DIRECT（W-B / W-A：`update.zip` 不是索引增量） |
| `.scan.list.gz`（元数据 manifest） | 服务端 mirror 发布（生成器**未找到**） | `Last-Modified` + generation；并用 30 分钟跳过 | **是（相对客户端本地文件的 file-level diff）** | DIRECT（`xiaoya_emd_go` / `xiaoya-emby` 源码） |

### 14.2 `.scan.list.gz` manifest 语义（DIRECT）

以 `universonic/xiaoya-emby`（MIT，commit `2af6db2`，2026-09-13）与
`xiaoyaDev/xiaoya_emd_go`（GPL-3.0，commit `3161a98`）源码为准：

- **内容是「路径 + 分钟级时间戳」清单**：每行格式 `YYYY-MM-DD HH:MM /absolute/path`；
  时间戳**分钟截断**、按生成者**本地时区**写（非 GMT），因此**不能直接与 HTTP `Last-Modified` 比较**
  （`xiaoya-emby:engine/metadata.go:42-52`）。**manifest 不含 size / hash / object id。**
- **一次下载替代递归爬取**：xiaoya-emby 原文 "One download replaces the recursive crawl of autoindex pages"。
- **generation 由 `Last-Modified` 定义**：xiaoya-emby 并发探测所有 mirror，只使用
  `Last-Modified` **恰好等于最新代**的 mirror，避免混代；持久化的是解析内容的代（bodyHash）
  而非探测时间戳（`metadata.go:320-372, 2387-2390`）。
- **低成本「远端是否变了」判断**：`xiaoya_emd_go:checkAndUpdateScanList`（main.go:1515-1560）
  GET `/.scan.list.gz` → 读 `Last-Modified` → 与持久化 `config.ScanListTime`（或本地 mtime）比较，
  **`serverTime.Sub(compareTime) <= 30*time.Minute` 直接跳过下载**。
- **无 manifest 时回退**：所有 mirror 都拿不到 manifest → 回退 legacy HTML 递归爬取（`--force-crawl`）。

### 14.3 真正的 diff / 增量逻辑（DIRECT）

- `xiaoya_emd_go:compareAndPrepareSync`（main.go:857-925）：
  `toUpdate = !exists OR serverTS - localTS > 600`（10 分钟容差）；`toDelete = 本地有、服务器列表无`。
- `xiaoya_db/solid.py`（Python）：`need_download`（265-287）按 **不存在 / 大小不同 / 时间戳更新** 判定；
  另有 `Complete Gate`：`gap = |len(temp) - total_amount| < 10 AND total_amount > 0` 才 purge（465）。
- **xiaoya-emby 最成熟**：manifest 只给 path+mtime；每个 file row 另带 **time base**
  （`manifest`/`http`/`unknown`）+ **content identity**（强 ETag 用 `etag:size`，否则 materialization ID）；
  **时间戳不跨 base 比较**，foreign base 必须先用一次 HTTP HEAD 重新 identify 才能改时间戳（README）。

### 14.4 删除安全（对 IndexCore 最有借鉴价值的部分，DIRECT）

xiaoya-emby（MIT）实现了一组与 IndexCore 蓝图高度同构的删除保护：

- **双 mirror 内容一致才允许 cleanup**：至少两个不同 mirror serving **完全相同最新代 manifest**
  （`sha256` 相等）才 `cleanupAuthorized`；否则跳过（`metadata.go:800-816`）。
- **跨代确认删除**：某 path 过去被 manifest 覆盖、现在缺失 → 只有它**连续两个 manifest generation
  都缺席**（`pending_root_drops` + 代变化）才删除，否则 deferred（`metadata.go:844-877`）。
  = 「missing 不等于 deleted，需独立再次确认」。
- **删除数量门控**：拒绝删除超过本地库一半，或任何 ≥20 文件的 root；malformed manifest 直接禁删（README）。
- 与 Kernel 已冻结的 `INV-003/005/022`、`C-7/C-9`（completeness gate）方向一致——属于**借思想**，
  而非需要复用的 provider 能力。

### 14.5 刷新调度 / 更新时间判断 / 目录差异模式（可借鉴）

- **调度**：xiaoya-emby 支持 cron（默认 `0 0 * * *`）与手动 `incremental` / `full-relaxed` /
  `full-strict` 触发；一次只跑一个 job，重入 `409 busy`。
- **更新时间判断**：`Last-Modified` + 30 分钟跳过（emd-go）；manifest 最新代选择（emby）。
- **目录差异模式**：`local file set` vs `manifest entry set` 的 path-level diff；
  per-row time base + content identity 决定是否真正重传。
- **原子写入 / 软删除**：emd-go 用 `.tmp + os.Rename` 与 `recycle_bin/`；xiaoya-emby 用持久化
  `full_sync_state`（可恢复 rebuild）与 quarantine。

### 14.6 上游生成器是否找到（本轮待查项）

- `index.zip` / `update.zip` / `.scan.list.gz` 的**服务端生成器源码仍未找到**；
  本轮 GitHub 代码 / 仓库检索（`scan.list.gz` 生成端、`xiaoya metadata`、mirror/generator）
  **无结果** → **UNAVAILABLE / UNKNOWN**（与 Discovery 01 结论一致）。
- 因此 manifest 的**生成成本、更新频率、覆盖范围**不可直接复用；只能借鉴**消费端**的
  「manifest + Last-Modified/generation + diff + 跨代删除确认」**思想**。

### 14.7 可复用 / 借思想 分类

| 对象 | 分类 | 说明 |
| --- | --- | --- |
| `universonic/xiaoya-emby`（MIT） | **ADAPT / 借思想（同构度高）** | manifest generation 选择、per-row time base、跨代删除确认、双 mirror 一致门控 |
| `xiaoyaDev/xiaoya_emd_go`（GPL-3.0） | **IDEA_ONLY** | `Last-Modified` + 30 分钟跳过、diff 规则 |
| `xiaoyaDev/xiaoya_db`（无 LICENSE） | **IDEA_ONLY** | `need_download` 三条件、gap Complete Gate |
| `index.zip` / `update.zip` 分发机制 | **IDEA_ONLY** | `version.txt` 轻量版本判断 |
| 上游生成器 | **UNAVAILABLE** | 源码未找到 |

### 14.8 小结（供 Architect 判定，非结论）

小雅证明：**「预生成清单 + `Last-Modified`/generation + 客户端 file-level diff + 跨代删除确认」
确实能把「发现变化」的成本压到「下载一个小清单 + 按需 head/下载」**，而**无需 provider-native
change feed**。但它依赖一个**外部生成端**持续产出清单；IndexCore 若要复用这种模式，必须先解决
「清单从哪来」——对 115 只能由 scoped list / 轮询产出，或仅借鉴其**消费端 diff 与删除确认**思想。

---

## 15. rclone specific conclusion

- 提供可选 `ChangeNotifier`（`fs/features.go:569-581`），**polling 非 webhook**；
  D03 证实 14/69 backend 实现。
- **无 115 backend** → 对本项目主场景不适用。
- 不能提供跨 backend 稳定 identity；不能防 silent backend truncation（D03）。
- 无 scan checkpoint/resume 持久化；RC 不返回 structured skipped-set（D03）。
- 定位：可作为**其它 provider** 的候选 Collector（DRIVER_DEPENDENT），
  但不是 115 增量发现的答案。

---

## 16. UNKNOWNs requiring live tests

1. **最终一致性**：上传/移动成功后，115 侧 list 何时可见？（决定 Mutation Hint 的 retry/backoff 与可达到的延迟下限）
2. **OpenList 115 driver 的外部变更可见延迟**：`refresh=true` 后，外部上传在新会话中多久出现？
3. **115 服务端限流阈值**：在何种 QPS/日请求量下触发限流或风控？（官方不公开）
4. **大目录实际耗时/请求数**：10k/30k 目录一次 `refresh=true` 的真实 provider 请求数与墙钟时间。
5. **AList/OpenList 缓存 TTL 的默认与实际生效值**（storage 配置差异）。
6. **回收站轮询**能否可靠发现删除（含 move/rename 是否出现）。
7. **多 root 共享账号**下的实际风控表现。
8. **115 官方 open API** 的实际限流、`user_utime` 排序在增量识别中的可用性与精度。
9. OpenList 版本升级对缓存语义（`CustomCachePolicies`、失效规则）的稳定性影响。
10. **Xiaoya `.scan.list.gz` 上游生成器**：生成成本、频率、覆盖范围未证实（生成器源码未找到）。
11. **轻量 manifest 模式对 IndexCore 的适配方式**：「清单由谁生成」尚无证据——115 侧只能由
    scoped list / 轮询产出，需评估其成本与收益。
12. **115 Open 官方接入的增量可用性**：社区 SDK wrapper 是否暴露任何「变更 / 最近」辅助接口
    （当前 API 列表未见）；官方 OAuth token 在长期轮询下的稳定性与配额表现。
13. **115 Open vs 115 Cloud 的实际可见延迟与成本差异**：外部上传后两条路径经 `refresh=true`
    的可见时间与请求成本对比（决定优先路径）。
14. **115 Open 服务端限流阈值**：官方不公开；需在专用测试账号上测 QPS / 日请求量边界。
15. **分页一致性 / 快照同代性（Round 3）**：采用「第 1 页 `refresh`、其余页读 cache」后，
    多页读取期间 OpenList cache 发生写 / invalidation / TTL / refresh 时，IndexCore 是否可能
    拼出**混代目录数据**？`len(all)==total` 不能证明同代。需 prototype/live 测试（§6.5.2、LT-7）。
16. **stable provider ID 透传（Round 3）**：是否存在**不耦合 driver internals** 的安全方式，
    把 stable provider ID（如 115 `fid`）经 OpenList/AList 传给 IndexCore？若没有，则**保持现有
    identity assurance，不因 Scoped Refresh 提升身份可信度**（移动/重命名仍靠 identity evidence）。

---

## 17. Live test plan（仅在 Architect 授权且有可控账号时执行）

> 本阶段禁止对生产账号压测。以下为**候选**测试设计，需显式请求预算与账号隔离。

| Test | 目标 | 最小设计 | 预算上限 |
| --- | --- | --- | --- |
| LT-1 cache vs forced refresh | 证明 `refresh=true` 能提前看到外部新文件 | 外部上传 → 分别 `refresh=false`/`true` 观测可见时间 | 数十次请求 |
| LT-2 provider 一致性曲线 | 上传成功到 list 可见的延迟分布 | 已知 mutation 后按 1s/5s/10s… 轮询观测 | 单次实验 <100 请求 |
| LT-3 大目录刷新成本 | 量化 10k/30k 目录 refresh 的请求数与耗时 | 观测 provider 请求计数 + 墙钟 | 每个规模 1–3 次 |
| LT-4 无变化轮询 | 24h 无变化时 refresh 的实际代价与是否触发限流 | 固定间隔低 QPS 连续观测 | 需明确总请求预算 |
| LT-5 token 失效 | refresh 失败/终态错误的可观测信号 | 使用可丢弃的授权 | 低 |
| LT-6 多 root 共账号 | 请求聚合对风控的影响 | staging 账号 | 需 Architect 明确上限 |
| LT-7 分页一致性（Round 3） | 验证多页读取是否可能拼出**混代数据** | 第 1 页 `refresh=true` 后，在读取第 2..N 页期间**故意触发**写操作 / cache invalidation / TTL / 另一次 refresh，检查返回集合是否自洽（同代） | 低（可控目录） |

**执行前置**：显式授权、专用测试账号/目录、请求预算、可回滚；不得使用生产 115 账号。

---

## 18. Final options（供 Architect 决策）

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
- `PROTOTYPE_SCOPED_REFRESH` — 有源码级 DIRECT 证据支持其**技术可行性**；且可落到
  **基于 115 官方 Open API 的社区维护驱动**（规避私有接口风险、复用社区 SDK wrapper）。**须采用「第 1 页
  `refresh=true`、其余页 `refresh=false`」的候选语义（§6.5.1），且需 OpenList 写权限（§6.7）**；
  请求放大（Cloud/Open 不同，§9.1）、IndexCore adapter 的 `refresh:false` 缺口（§6.7）、
  最终一致性仍待处理 / 验证。
- `PROTOTYPE_115_OPEN_REUSE`（候选命名，供参考）— 复用 OpenList/AList 的 **基于 115 官方 Open API 的社区维护驱动**
  做 scoped refresh；与本报告「不重造轮子」方向一致。是否作为独立选项由 Architect 决定。
- `PROTOTYPE_MUTATION_HINT` — 只覆盖「已知写入」，须与轮询/验证配合。
- `PROTOTYPE_HYBRID` — 覆盖最完整，但复杂度与请求预算最高。
- `KEEP_FULL_SCAN_ONLY` / `STOP` — 在请求放大或风控不可接受时的有效退路。

**Whether to proceed, which option, and what latency target to freeze is an
Architect decision. This report does not select a strategy and does not
authorize implementation.**

### 18.1 What this report does NOT claim

- 不声称 115 有 cursor / change feed——准确表述：截至 2026-09-24，在已审计的公开
  115 Open API 中**未发现** native delta / change cursor（不排除未公开能力）。
- 不声称 115 Cloud（私有接口）是 115 的**唯一**接入路径（已有**基于 115 官方 Open API 的
  社区维护驱动**）。
- 不声称 `refresh=true` 安全或廉价（大目录会放大）。
- 不声称一次目录刷新 = 一次 provider 请求（不成立）。
- 不声称 2 分钟轮询合理（需预算与 live test）。
- 不声称可以简单把 `refresh:false` 全量改成 `refresh:true`（会按 HTTP 页数放大 provider 请求，见 §6.5.1）。
- 不声称删除事件可信（首次实现不得直接 canonical 删除）。
- 不声称 rclone 所有 backend 行为一致（DRIVER_DEPENDENT）。
- 不替 Architect 决定最终方案。

