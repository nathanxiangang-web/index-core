# AList / OpenList Collector 可行性调查报告 — Discovery 02

> 阶段：Discovery 02 — AList / OpenList Provider API 与 Collector 可行性调查
> 状态：READY_FOR_ARCH_REVIEW v3
> 日期：2026-09-23
> 产出：Foreman 统一整理 W-A / W-B / W-D 报告
> 约束：本报告只总结调查事实，不做最终架构决定
> 修订：v3 补 OneDrive driver / 修正 total/health check 为 weak sanity check；v2 修正 stable identity / snapshot completeness / W-C 职责 / rclone / license

---

## 一、调查概述

本报告由 Foreman 基于三份独立调查报告交叉核验后统一整理。

| 调查员 | 任务 | 报告 | 结论 |
|--------|------|------|------|
| W-A | AList/OpenList 搜索索引内部机制 | `d02/W-A-ALIST-OPENLIST-INDEXING.md` | PASS |
| W-B | AList/OpenList 公开 HTTP API 字段与行为 | `d02/W-B-ALIST-OPENLIST-PROVIDER-API.md` | PASS |
| W-D | Provider 抽象 / API 字段 / 完整遍历 / Collector 可行性 | `d02/W-D-PROVIDER-SNAPSHOT-MATRIX.md` | PASS |

### Worker 覆盖说明

原任务设计 4 个 Worker（W-A/W-B/W-C/W-D），实际交付 3 份报告。覆盖关系如下：

| 原任务 | 职责 | 实际覆盖 | 说明 |
|--------|------|----------|------|
| W-A | 搜索索引内部机制 | **W-A**（本轮新增） | 16 问全答，含 BuildIndex/Update/rename/move/checkpoint/staging/atomic reconcile |
| W-B | HTTP API 字段与行为 | **W-B**（独立完成） | 20 问详查，349 行 |
| W-C | Driver 元数据与能力差异 | **COVERED_BY_W_D** | W-D COUNTEREXAMPLES 覆盖 7 代表性 driver（Local/WebDAV/S3/GoogleDrive/Aliyundrive/115/OneDrive），建立 path-based 与 id-based 两种 identity 模型；能力维度（ID/hash/mtime/cache/error/pagination/rename/move）全覆盖。夸克未逐一调查，本阶段不作推断 |
| W-D | Provider 能力矩阵 / Collector 可行性 | **W-D**（子智能体替代） | 原 Bridge w04 造假已取消，子智能体独立完成 591 行 |

**结论**：4 个任务职责全部覆盖，无遗漏。

### 源码版本

| 仓库 | Commit SHA | Commit 日期 | License |
|------|------------|-------------|---------|
| AList (`alist-org/alist`) | `fb0731a6953012e7b72b89bf5473817caa4625f9` | 2026-09-19 | AGPL-3.0 |
| OpenList (`OpenListTeam/OpenList`) | `3a31b438a94af2532608499b74251c630ddf0f6f` | 2026-09-21 | AGPL-3.0 |

---

## 二、Provider 抽象（W-D Q1）

AList 与 OpenList 的 driver 抽象**几乎完全相同**：

- 核心接口 `Driver = Meta + Reader`，`Reader = List + Link`（两者逐字相同）
- 能力通过 **Go 可选接口 + 类型断言** 表达（`Getter`/`Mkdir`/`Put`/`ArchiveReader`…），非 bit field
- `Obj` 接口 8 方法：`GetSize/GetName/ModTime/CreateTime/IsDir/GetHash/GetID/GetPath`
- `model.Object` 8 字段：`ID/Path/Name/Size/Modified/Ctime/IsFolder/HashInfo`
- OpenList 是 AList 的**超集**：新增 `WithDetails`/`LinkCacheModeResolver`/`DirectUploader` 三个可选接口

**证据**：AList `internal/driver/driver.go:9-39`；OpenList `internal/driver/driver.go:9-14`；类型断言 `internal/op/fs.go:178`

---

## 三、API 字段分歧（W-B Q1-Q5, W-D Q2）

| 字段 | AList `/api/fs/list` | OpenList `/api/fs/list` | 分类 |
|------|---------------------|------------------------|------|
| `name` | ✅ DIRECT | ✅ DIRECT | 两者都有，始终非空 |
| `size` | ✅ DIRECT | ✅ DIRECT | 两者都有 |
| `is_dir` | ✅ DIRECT | ✅ DIRECT | 两者都有，唯一文件/目录判据 |
| `modified` (mtime) | ✅ DIRECT | ✅ DIRECT | 两者都有 |
| `created` (ctime) | ✅ DIRECT（可能回退 mtime） | ✅ DIRECT（可能回退 mtime） | `CreateTime()` 零值回退 `ModTime()` |
| `hash_info` | ⚠️ DRIVER_DEPENDENT | ⚠️ DRIVER_DEPENDENT | local/webdav/s3 为空；google_drive/115 有值 |
| `provider` (driver 名) | ✅ DIRECT | ✅ DIRECT | 如 "Aliyundrive"，非 storage 实例 ID |
| `virtual_path` | ✅ DIRECT | ❌ UNAVAILABLE | AList 计算 `Join(parent, name)`；OpenList 不暴露 |
| `id` (provider file_id) | ⚠️ DRIVER_DEPENDENT | ❌ UNAVAILABLE | AList 仅 cloud driver 有值；OpenList API 层完全不暴露 |
| `path` (driver 内部) | ⚠️ DRIVER_DEPENDENT | ❌ UNAVAILABLE | local driver 泄露宿主机绝对路径 |
| `has_more` / `pages_total` | ✅ DIRECT | ❌ UNAVAILABLE | OpenList 仅返回 `total` |
| `storage_id` (实例) | ❌ UNAVAILABLE | ❌ UNAVAILABLE | 仅 admin `/api/admin/storage/list` |
| `raw_url` | ✅ DIRECT（仅 `/api/fs/get`） | ✅ DIRECT（仅 `/api/fs/get`） | 每次请求生成，会过期，非 identity |
| `sign` | ✅ DIRECT | ✅ DIRECT | HMAC 下载令牌，非 identity |

### Driver 能力差异（W-D COUNTEREXAMPLES）

| Driver | 设置 `ID`? | 设置 `Hash`? | Identity 模型 |
|--------|-----------|-------------|--------------|
| **Local** | ❌ | ❌ | path-based |
| **WebDAV** | ❌ | ❌ | path-based |
| **S3** | ❌（源码注释掉） | ❌ | path-based (key) |
| **GoogleDrive** | ✅ (`f.Id`) | ✅ (MD5/SHA1/SHA256) | id-based |
| **Aliyundrive** | ✅ (`f.FileId`) | ❌ | id-based |
| **115** | ✅ | ✅ (SHA1) | id-based |
| **OneDrive** | ✅ (`f.Id`) | ❌ (hashes not read) | id-based |

---

## 四、完整遍历（W-B Q8-Q15, W-D Q3）

### 可靠的部分

1. **递归遍历可行**：从 `/` 出发，`list` → 对 `is_dir=true` 递归
2. **分页可判断完整性**：AList 有 `has_more`/`pages_total`/`total`；OpenList 默认 `PerPage=MaxInt`，靠 `total` 与 `len(content)` 判断
3. **`refresh=true` 可绕过 cache**：两者一致，但 AList 需写权限
4. **多 root 可枚举**：`/api/admin/storage/list`（需 admin 权限）

### 硬隐患

1. **遍历错误静默吞没（高）**：`storage.List` 出错但 `virtualFiles` 非空时，错误被吞没，返回部分结果，HTTP 200。遍历者无法感知。
   - 证据：AList `internal/fs/list.go:32-38`，OpenList `internal/fs/list.go:32-39`
2. **cache 过期（中）**：不传 `refresh=true` 返回缓存（TTL 默认 30 分钟）
3. **分页为内存切片（中）**：`driver.List` 一次性返回全部，API 分页在内存中切片。超大目录有 OOM/超时风险
4. **`total` 是后过滤计数**：role 过滤后计数，无法得知被隐藏的子条目数

---

## 五、Snapshot Completeness（修正后结论）

**不能仅凭一次成功递归遍历，把 Snapshot 标记为 `complete=true` 并授权 destructive removal。**

理由：

1. **静默吞没**：`storage.List` 出错 + `virtualFiles` 存在 → error 被吞掉 → 返回部分数据 → HTTP 200。一次 HTTP 200 的完整递归遍历**不等于** provider-complete snapshot。
2. **`len(content) == total` 不可靠**：`total` 是根据当前已返回的列表计算的。如果上游已少返回，两者仍然可以相等。
3. **cache 过期**：不传 `refresh=true` 时返回缓存列表，可能含幽灵条目或缺失新增文件。

**正确结论**：AList/OpenList 可以作为 **Snapshot candidate / source**。但 Snapshot 的 `complete=true` 标记需要**独立于 API 响应的外部校验机制**。

---

## 六、Stable Identity 问题（修正后结论）

### 已证实的事实

- AList Search 使用 `Parent + Name` 作为 **search-node key**（`model.SearchNode` 仅含 `Parent/Name/IsDir/Size`，`internal/model/search.go:23-28`）。这是 **path-based matching key**，不是 stable resource identity。
- rename 后 `Name` 改变 → key 改变。move 后 `Parent` 改变 → key 改变。因此 **path identity != stable resource identity**。
- AList 的 provider `id` 字段：仅部分 Driver 存在（GoogleDrive/Aliyundrive/115 有；Local/WebDAV/S3 为空）。**不是通用 stable identity**。
- OpenList public FS API：**没有** provider object ID（`ObjResp` 无 `id` 字段）。

### 结论

当前研究发现一个**硬缺口**：AList/OpenList public API 不提供跨所有 driver 的通用 stable resource identity。

- `virtual_path` / `Parent + Name`：是 path-based matching key，rename/move 后变化，**不是 stable identity**。
- AList `id`：driver 依赖，Local/WebDAV/S3 为空，**不是通用 identity**。
- OpenList `id`：API 层不暴露，**不可用**。
- `hash`：driver 依赖，Local/WebDAV/S3 为空，且是 content-addressed（rename 不变但 content 变则变）。

---

## 七、搜索索引内部机制（W-A）

### SearchNode 模型

`SearchNode` 仅含 `Parent/Name/IsDir/Size`（4 字段）。无 ID、Hash、Modified、Created。

### BuildIndex

- 入口：`/api/admin/index/build` → 先 `Clear` 再从 `/` 全量重建（`server/handles/index.go:21-40`）
- 遍历：`fs.WalkFS` + 内存 MQ 批量写入 DB（`internal/search/build.go:33-188`）
- 无 checkpoint/resume：中断后从头开始
- 无 staging：`indexMQ` 是内存 buffer，`BatchIndex` 直接写 DB

### 自动 Update

- 触发：`op.List` 成功后异步调用 `HandleObjsUpdateHook` → `Update`（`internal/op/fs.go:146-149`）
- 机制：**目录级 name diff** — `toDelete = old.Difference(now)`, `toAdd = now.Difference(old)`（`build.go:224-234`）
- 无 atomic reconcile：先 delete 后 add 无事务，中途失败无 rollback
- Provider partial failure → 缺失文件被错误删除（`!op.HasStorage` 只保护挂载点，不保护存储内文件）

### rename/move 在索引中

- **delete + add**（不是 rename）：name diff 发现旧 name 消失、新 name 出现
- AList：rename/move 不直接更新索引，延迟到下次 `list` 触发 Update
- OpenList：rename/move 直接异步触发 `objsUpdateHook`

### Search index 作为 Canonical Inventory

**不具备**。字段不足、无 atomic reconcile、无 checkpoint/resume、无 staging、provider 故障可致错误删除、无身份连续性、非 source of truth。

---

## 八、Collector 可行性判定（W-B Q16-Q20, W-D Q4）

| Collector 类型 | 可行性 | 条件 |
|---------------|--------|------|
| **全量快照型** | ⚠️ 有条件可行 | 可递归枚举，但 `complete=true` 需外部校验（见 §五） |
| **增量/delta 型** | ❌ 不可行 | 无 native delta API（无 `since`/`cursor`/`watch`） |
| **mtime-based delta** | ⚠️ 部分可行 | `modified` 可做客户端过滤，但目录 mtime 不保证反映子文件变更 |

### 硬缺口（IndexCore Kernel 该拥有的）

1. **Stable Identity**：无跨 driver 通用 stable resource identity（见 §六）
2. **Safe Reconcile**：遍历错误静默吞没 + `len(content)==total` 不可靠 → 无法仅凭 API 响应保证完整性（见 §五）
3. **Canonical Inventory**：无 native delta，需全量遍历做对比。AList 搜索索引不具备此条件（见 §七）
4. **无 native delta API**：`ListReq` 仅 `Page/Path/Password/Refresh`，无 `since`/`cursor`
5. **无 webhook/SSE/WebSocket**：变更通知仅限内部 Go hook

### NEED_D03_RCLONE_OR_FSSPEC: YES

AList/OpenList 已发现两个核心硬缺口（stable identity 不统一、snapshot completeness 无法通过 public API 可靠证明）。值得进行一个窄范围 D03 对照调查，比较其它成熟 Collector / provider abstraction 是否能补这两个缺口。D02 范围为 AList/OpenList only，不对 rclone 做具体技术能力判断。

---

## 九、SnapshotEntry 字段映射

| SnapshotEntry 字段 | AList | OpenList | 分类 |
|---------------------|-------|----------|------|
| `name` | `content[].name` | `content[].name` | **DIRECT** |
| `size` | `content[].size` | `content[].size` | **DIRECT** |
| `is_dir` | `content[].is_dir` | `content[].is_dir` | **DIRECT** |
| `modified` | `content[].modified` | `content[].modified` | **DIRECT** |
| `created` | `content[].created` | `content[].created` | **DIRECT**（语义弱，可能 = modified） |
| `provider` | `FsListResp.provider` | 同 | **DIRECT**（driver 名，非实例 ID） |
| `path` (virtual) | `content[].virtual_path` | reconstruct `Join(req.path, name)` | **DIRECT** (AList) / **DERIVABLE** (OpenList) |
| `id` (provider) | `content[].id` | absent | **DRIVER_DEPENDENT** (AList) / **UNAVAILABLE** (OpenList) |
| `hash` | `content[].hashinfo` | 同 | **DRIVER_DEPENDENT** |
| `thumb` | `content[].thumb` | 同 | **DRIVER_DEPENDENT** |
| `raw_url` | `FsGet.raw_url` | 同 | **DERIVABLE**（需额外 `get` 调用；不稳定/过期） |
| `storage_id` | not in FS response | not in FS response | **UNAVAILABLE**（仅 admin API） |

---

## 十、风险清单

| # | 风险 | 等级 | 缓解 |
|---|------|------|------|
| 1 | 遍历完整性静默失败 | **高** | 对比 `len(content)` 与 `total`、storage health check 最多只是 **weak sanity check**，不能证明 provider-complete snapshot，更不能单独授权删除；`complete=true` 需独立外部校验 |
| 2 | 无 native delta API | **高** | 全量遍历 + 客户端 path 对比做 delta；或接受全量 |
| 3 | 无通用 stable resource identity | **高** | path-based matching key 可用但 rename/move 后变化；AList `id` driver 依赖；OpenList 无 `id` |
| 4 | 搜索索引 provider 故障可致错误删除 | **高** | name diff 删除不保护存储内文件；IndexCore 不应依赖 AList 搜索索引做 Canonical Inventory |
| 5 | cache 过期 | **中** | Collector 用 admin token + `refresh=true` |
| 6 | 超大目录 OOM/超时 | **中** | 限制单 storage 规模，或分拆 mount path |
| 7 | OpenList API 字段缺失 | **中** | 统一用 path-based matching key；便携 Collector 应 target OpenList 子集 |
| 8 | driver 实现质量参差 | **中** | 不假设任何字段非空，按 driver 类型降级处理 |

---

## 十一、License

AList: **AGPL-3.0**（`LICENSE:1`）

OpenList: **AGPL-3.0**（`LICENSE:1`）

当前工程策略：
- 不复制其源码
- 不链接其源码进入 Kernel
- 优先独立进程 / public API 边界
- 实际采用前单独进行许可证审查

---

## 十二、对 IndexCore 架构的启示

1. **Collector 应 target OpenList 子集 API**：只用 `name`/`size`/`is_dir`/`modified`/`created`/`hash_info`/`total` + 重构 path。同时兼容 AList 和 OpenList。
2. **Path-based matching key**：`Parent + Name` 可用作 matching key，但**不是 stable resource identity**。rename/move 后 key 变化。IndexCore 需要独立解决 stable identity 问题。
3. **Snapshot candidate，非 self-certifying complete**：AList/OpenList 可作 Snapshot source，但 `complete=true` 需外部校验，不可仅凭一次 API 遍历授权 destructive removal。
4. **全量 + 客户端 delta**：唯一可行的 Collector 模式。Kernel 需实现 Safe Reconcile。
5. **不依赖 AList 搜索索引**：AList 搜索索引不具备 Canonical Inventory 条件（字段不足、无 atomic reconcile、provider 故障可致错误删除）。
6. **NEED_D03**：两个硬缺口（stable identity + snapshot completeness）值得 D03 窄范围对照调查。

---

## 十三、已证实事实清单

> 全部为源码直接证实（FACT），附证据。

1. AList `Driver` 接口 = `Meta + Reader`（`internal/driver/driver.go:9-39`）
2. OpenList `Driver` 接口与 AList 逐字相同（`internal/driver/driver.go:9-39`）
3. AList `/api/fs/list` 响应含 `id`/`path`/`virtual_path`/`has_more`/`pages_total`（`server/handles/fsread.go:52-82`）
4. OpenList `/api/fs/list` 响应**不含**上述字段（`server/handles/fsread.go:35-58`）
5. local driver 不设 `ID`/`HashInfo`（`drivers/local/driver.go:191-204`）
6. webdav driver 不设 `ID`/`Path`/`HashInfo`（`drivers/webdav/driver.go:55-62`）
7. S3 driver `ID` 被注释掉（`drivers/s3/util.go:111`）
8. google_drive driver 设 `ID` + MD5/SHA1/SHA256（`drivers/google_drive/types.go:40-66`）
9. aliyundrive driver 设 `ID = f.FileId`，不设 `HashInfo`（`drivers/aliyundrive/types.go:34-45`）
10. 115 driver 设 `ID` + SHA1（`drivers/115/types.go:13-24`）
11. `list` 返回 cache 除非 `refresh=true`（AList `op/fs.go:118-123`，OpenList `op/fs.go:37-48`）
12. AList `refresh=true` 需写权限（`fsread.go:118-121`）
13. Cache TTL = `CacheExpiration` 分钟，默认 30（`op/driver.go:78-82`）
14. 分页为内存切片，`driver.List` 一次性返回全部（AList `fsread.go:284-299`，OpenList `fsread.go:214-226`）
15. OpenList 默认 `PerPage=MaxInt`（`internal/model/req.go:13-20`）
16. 遍历中途 `storage.List` 出错且 `virtualFiles` 非空时，错误被吞没（AList `fs/list.go:32-38`，OpenList `fs/list.go:32-39`）
17. 无 native delta API：`ListReq` 无 `since`/`cursor`（`fsread.go:22-27`）
18. 无 webhook/SSE/WebSocket for FS changes（`server/router.go` 无相关路由）
19. `SearchNode` 仅含 `Parent/Name/IsDir/Size`（`internal/model/search.go:23-28`，两者相同）
20. AList 搜索索引 Update 是目录级 name diff（`build.go:224-234`）
21. AList rename/move 不直接更新索引（`internal/op/fs.go:364-439`）
22. OpenList rename/move 直接触发 `objsUpdateHook`（`internal/op/fs.go:439-446, 499-507`）
23. 搜索索引无 checkpoint/resume（`build.go:86-90, 115-121`）
24. 搜索索引无 staging（`build.go:47, 73-84`）
25. 搜索索引无 atomic reconcile（`build.go:235-267`）
26. 搜索索引 provider partial failure 可致错误删除（`build.go:236` — `!HasStorage` 只保护挂载点）
27. `created` 零值回退 `modified`（`internal/model/object.go:63-68`）
28. `total` 是 role 过滤后计数（`fsread.go:132-148`）
29. `sign` 是 HMAC-SHA256 of virtual path，非 content hash（`common/sign.go:12-17`）
30. 两者 License 均为 AGPL-3.0（`LICENSE:1`）

---

## 十四、未证实 / 推理

> 标注为 INFERENCE，不应作为事实使用。

1. **(INFERENCE)** provider file_id 在 rename/move 后不变（cloud driver 源码支持，但非 API 契约，未实测）
2. **(INFERENCE)** 超大目录 OOM/超时行为（源码显示一次性返回，未实测具体 driver）
3. **(INFERENCE)** `raw_url` 同一路径跨调用变化（presign 代码支持，未穷举所有 driver）
4. **(INFERENCE)** OpenList 移除 `id`/`path` 是有意设计（可能安全考虑，未查 commit message）
6. **(UNVERIFIED)** AList `id` 在 rename 后是否稳定（未查各 provider 文档）
7. **(UNVERIFIED)** OpenList 是否有其他 API 暴露 object_id（仅查了 `fsread.go`，未全面扫描所有 handler）

---

## 附录：报告索引

| 报告 | 路径 | 行数 | 内容 |
|------|------|------|------|
| W-A | `docs/research/d02/W-A-ALIST-OPENLIST-INDEXING.md` | — | 搜索索引内部机制 16 问 |
| W-B | `docs/research/d02/W-B-ALIST-OPENLIST-PROVIDER-API.md` | 349 | AList/OpenList HTTP API 20 问详查 |
| W-D | `docs/research/d02/W-D-PROVIDER-SNAPSHOT-MATRIX.md` | 591 | Provider 抽象 / API 字段 / 完整遍历 / Collector 可行性 / 能力矩阵 / 反例 |