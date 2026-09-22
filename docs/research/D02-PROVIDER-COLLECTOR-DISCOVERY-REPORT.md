# Provider / Collector 可行性调查报告 — Discovery 02

> 阶段：Discovery 02 — AList / OpenList Provider API 与 Collector 可行性调查
> 状态：READY_FOR_ARCH_REVIEW
> 日期：2026-09-23
> 产出：Foreman 统一整理 Worker B + 聚焦调查（W-D）报告
> 约束：本报告只总结调查事实，不做最终架构决定

---

## 一、调查概述

本报告由 Foreman 基于两份独立调查报告交叉核验后统一整理。

| 调查员 | 任务 | 报告 | 结论 |
|--------|------|------|------|
| Worker B (Bridge w02) | AList/OpenList 公开 HTTP API 字段与行为（20 问） | `d02/W-B-ALIST-OPENLIST-PROVIDER-API.md` | PASS |
| 聚焦调查 (子智能体) | Provider 抽象 / API 字段 / 完整遍历 / Collector 可行性 | `d02/W-D-PROVIDER-SNAPSHOT-MATRIX.md` | PASS |

> Worker A（远程 w01）报告未取回；Worker C（rclone）经调查确认不需要；Worker D（Bridge w04）造假已取消，由子智能体替代。

### 源码版本

| 仓库 | Commit SHA | Commit 日期 | License |
|------|------------|-------------|---------|
| AList (`alist-org/alist`) | `fb0731a6953012e7b72b89bf5473817caa4625f9` | 2026-09-19 | AGPL-3.0 |
| OpenList (`OpenListTeam/OpenList`) | `3a31b438a94af2532608499b74251c630ddf0f6f` | 2026-09-21 | AGPL-3.0 |

---

## 二、Provider 抽象（W-D Q1）

### 结论

AList 与 OpenList 的 driver 抽象**几乎完全相同**：

- 核心接口 `Driver = Meta + Reader`，`Reader = List + Link`（两者逐字相同）
- 能力通过 **Go 可选接口 + 类型断言** 表达（`Getter`/`Mkdir`/`Put`/`ArchiveReader`…），非 bit field
- `Obj` 接口 8 方法：`GetSize/GetName/ModTime/CreateTime/IsDir/GetHash/GetID/GetPath`
- `model.Object` 8 字段：`ID/Path/Name/Size/Modified/Ctime/IsFolder/HashInfo`
- OpenList 是 AList 的**超集**：新增 `WithDetails`/`LinkCacheModeResolver`/`DirectUploader` 三个可选接口

### 证据

- AList `internal/driver/driver.go:9-39`：`Driver = Meta + Reader`，`Reader.List/Link` 签名
- OpenList `internal/driver/driver.go:9-14`：与 AList 逐字相同
- 类型断言用法：`internal/op/fs.go:178` — `if g, ok := storage.(driver.Getter); ok { ... }`
- `model.Object` 结构：AList `internal/model/object.go:41-50`，OpenList `object.go:22-30`

---

## 三、API 字段分歧（W-B Q1-Q5, W-D Q2）

### 关键发现

**AList 和 OpenList 在公开 HTTP API 上存在重大分歧**：

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
| `raw_url` | ✅ DIRECT（仅 `/api/fs/get`） | ✅ DIRECT（仅 `/api/fs/get`） | 每次请求生成，会过期，非稳定 identity |
| `sign` | ✅ DIRECT | ✅ DIRECT | HMAC 下载令牌，非 identity |

### Driver 能力差异（反例）

| Driver | 设置 `ID`? | 设置 `Hash`? | Identity 模型 |
|--------|-----------|-------------|--------------|
| **Local** | ❌ | ❌ | path-based |
| **WebDAV** | ❌ | ❌ | path-based |
| **S3** | ❌（源码注释掉） | ❌ | path-based (key) |
| **GoogleDrive** | ✅ (`f.Id`) | ✅ (MD5/SHA1/SHA256) | id-based |
| **Aliyundrive** | ✅ (`f.FileId`) | ❌ | id-based |
| **115** | ✅ | ✅ (SHA1) | id-based |

**关键洞察**：driver 分为 **path-based**（local/webdav/s3，provider 无稳定 file id）和 **id-based**（cloud drives，provider 有 file_id，AList 暴露但 OpenList 隐藏）。Collector **不能假设 `id` 非空**。

### 证据

- AList `FsListResp`：`server/handles/fsread.go:52-64`，含 `id`/`path`/`virtual_path`/`has_more`/`pages_total`
- OpenList `FsListResp`：`server/handles/fsread.go:49-58`，不含上述字段
- OpenList `toObjsResp`：`fsread.go:228-248`，不设 `Id`/`Path`/`VirtualPath`
- local driver：`drivers/local/driver.go:191-204`，未设 `ID`/`HashInfo`
- S3 driver：`drivers/s3/util.go:111` — `//Id: *object.Key`（注释掉）

---

## 四、完整遍历可行性（W-B Q8-Q15, W-D Q3）

### 结论

**有条件可靠**，但存在硬隐患。

#### 可靠的部分

1. **递归遍历可行**：从 `/` 出发，`list` → 对 `is_dir=true` 递归 `Join(parent, name)`，与 `FsDirs` 树形 UI 逻辑一致
2. **分页可判断完整性**：
   - AList：`has_more` + `pages_total` + `total` → 有明确完整遍历信号
   - OpenList：默认 `PerPage=MaxInt`（单次返回全部），靠 `total` 与 `len(content)` 判断
3. **`refresh=true` 可绕过 cache**：两者一致，但 AList 需写权限
4. **多 root 可枚举**：`/api/admin/storage/list` 返回所有 `MountPath`（需 admin 权限）

#### 硬隐患

1. **遍历错误静默吞没（高）**：当 `storage.List` 出错但该路径下存在虚拟挂载点时，错误被吞没，返回部分结果，HTTP 仍为 200。遍历者**无法通过 API 响应感知数据缺失**。
   - 证据：AList `internal/fs/list.go:32-38`，OpenList `internal/fs/list.go:32-39`
2. **cache 过期（中）**：不传 `refresh=true` 返回缓存（TTL 默认 30 分钟），可能含幽灵条目或缺失新增文件。AList 只读账户无法强制刷新。
3. **分页为内存切片（中）**：`driver.List` 一次性返回目录全部内容，API 分页在内存中切片。超大目录（10万+文件）有 OOM/超时风险。
4. **`total` 是后过滤计数**：role 过滤后计数，Collector 无法得知被隐藏的子条目数。
5. **无 bulk/parallel 原语**：每目录一次 HTTP 调用，深树慢。

### 证据

- 分页内存切片：AList `fsread.go:284-299`，OpenList `fsread.go:214-226`
- cache 逻辑：AList `op/fs.go:111-170`，OpenList `op/fs.go:26-125`
- 静默吞没：`if len(virtualFiles) == 0 { return nil, err }` — 有 virtualFiles 时不返回 error

---

## 五、Collector 可行性判定（W-B Q16-Q20, W-D Q4）

### 结论

| Collector 类型 | 可行性 | 条件 |
|---------------|--------|------|
| **全量快照型** | ✅ 可行 | 接受 path 作 identity + 全量遍历 + 客户端做 delta 对比 |
| **增量/delta 型** | ❌ 不可行 | 无 native delta API（无 `since`/`cursor`/`watch`） |
| **mtime-based delta** | ⚠️ 部分可行 | `modified` 可做客户端过滤，但目录 mtime 不保证反映子文件变更 |

### 硬缺口（IndexCore Kernel 该拥有的）

1. **Stable Identity**：无跨 driver 稳定 opaque id。AList `id` driver 依赖（local/webdav 空），OpenList API 层完全不暴露。最强跨项目 identity = **virtual path**（`Parent + Name`）。
2. **Safe Reconcile**：遍历错误静默吞没，无法通过 API 保证完整性。需外部一致性校验。
3. **Canonical Inventory**：无 native delta，需全量遍历做对比。AList 自身搜索索引即此模式（`search.Update` 用 name 集合差异检测新增/删除）。
4. **无 native delta API**：`ListReq` 仅 `Page/Path/Password/Refresh`，无 `since`/`cursor`。
5. **无 webhook/SSE/WebSocket**：变更通知仅限内部 Go hook（`HandleObjsUpdateHook`），不对外。

### AList 自身 identity 设计（FACT）

AList 搜索索引 `model.SearchNode` 仅含 `Parent/Name/IsDir/Size`（`internal/model/search.go:23-28`），**无 ID/Hash 字段**。索引构建用 `Parent + Name`（路径）作 identity，增量更新用 `name` 集合差异。**AList 自身设计就是以 path 作 stable identity**，非 object_id 非 hash。

### 是否需要 rclone

**不需要**。对于"全量快照型 Collector"，AList/OpenList 够用，AList 更优（暴露 `id`/`has_more`）。rclone 无额外优势——rclone 同样不普遍提供 native delta（除少数 backend 如 S3 versioning），且 rclone `--metadata` 暴露 provider id 的能力 AList 已有。

---

## 六、SnapshotEntry 字段映射

> 假设 SnapshotEntry 需要：`id`, `path`, `name`, `size`, `is_dir`, `modified`, `created`, `hash`, `provider`, `storage_id`

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

## 七、风险清单

| # | 风险 | 等级 | 缓解 |
|---|------|------|------|
| 1 | 遍历完整性静默失败 | **高** | 遍历后对比目录数与 `total`；对每个 storage 做健康检查 |
| 2 | 无 native delta | **高** | 全量遍历 + 客户端 path 对比做 delta；或接受全量 |
| 3 | stable identity 不稳定（path 在 rename 后变化） | **中** | AList 自身接受此风险；若需跨 rename 稳定，用 `id`（仅 id-setting driver）或 hash |
| 4 | cache 过期 | **中** | Collector 用 admin token + `refresh=true` |
| 5 | 超大目录 OOM/超时 | **中** | 限制单 storage 规模，或分拆 mount path |
| 6 | OpenList API 字段缺失 | **中** | 统一用 path identity；便携 Collector 应 target OpenList 子集 |
| 7 | driver 实现质量参差 | **中** | 不假设任何字段非空，按 driver 类型降级处理 |
| 8 | AGPL-3.0 传染性 | **高**（非技术） | 通过 HTTP API 集成不触发传染；嵌入/修改源码需开源。需法务确认 |

---

## 八、对 IndexCore 架构的启示

1. **Collector 应 target OpenList 子集 API**：只用 `name`/`size`/`is_dir`/`modified`/`created`/`hash_info`/`total` + 重构 path。这样同时兼容 AList 和 OpenList。
2. **Stable Identity = virtual path**：与 AList 自身设计一致。IndexCore 若接受 path 作 identity，AList/OpenList 可直接适配。
3. **全量快照 + 客户端 delta**：这是唯一可行的 Collector 模式。IndexCore Kernel 需实现 Safe Reconcile（全量快照对比 + 差异检测 + 一致性校验）。
4. **遍历完整性需外部保证**：API 不报错不代表数据完整。Kernel 需独立校验机制（如对比上次快照规模、storage 健康检查）。
5. **无需 rclone**：AList/OpenList API 够用。rclone 无额外优势。
6. **AGPL 合规**：通过 HTTP API 调用不触发传染，但需法务确认最终结论。

---

## 九、已证实事实清单

> 全部为源码直接证实（FACT），附证据。

1. AList `Driver` 接口 = `Meta + Reader`，`Reader = List + Link`（`internal/driver/driver.go:9-39`）
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
19. AList 搜索索引 identity = `Parent + Name`（`model.SearchNode` 仅 `Parent/Name/IsDir/Size`，`search.go:23-28`）
20. `created` 零值回退 `modified`（`internal/model/object.go:63-68`）
21. `total` 是 role 过滤后计数（`fsread.go:132-148`）
22. `FsGet` 返回 500（非 404）for not-found（`fsread.go:380-384`）
23. `sign` 是 HMAC-SHA256 of virtual path，非 content hash（`common/sign.go:12-17`）
24. 两者 License 均为 AGPL-3.0（`LICENSE:1`）

---

## 十、未证实 / 推理

> 标注为 INFERENCE，不应作为事实使用。

1. **(INFERENCE)** provider file_id 在 rename/move 后不变（cloud driver 源码支持，但非 API 契约，未实测）
2. **(INFERENCE)** 超大目录 OOM/超时行为（源码显示一次性返回，未实测具体 driver）
3. **(INFERENCE)** `raw_url` 同一路径跨调用变化（presign 代码支持，未穷举所有 driver）
4. **(INFERENCE)** OpenList 移除 `id`/`path` 是有意设计（可能安全考虑，未查 commit message）
5. **(UNVERIFIED)** AList `id` 在 rename 后是否稳定（未查各 provider 文档）
6. **(UNVERIFIED)** OpenList 是否有其他 API 暴露 object_id（仅查了 `fsread.go`，未全面扫描所有 handler）

---

## 附录：报告索引

| 报告 | 路径 | 行数 | 内容 |
|------|------|------|------|
| Worker B | `docs/research/d02/W-B-ALIST-OPENLIST-PROVIDER-API.md` | 349 | AList/OpenList HTTP API 20 问详查 |
| 聚焦调查 | `docs/research/d02/W-D-PROVIDER-SNAPSHOT-MATRIX.md` | 591 | Provider 抽象 / API 字段 / 完整遍历 / Collector 可行性 / 能力矩阵 / 反例 |