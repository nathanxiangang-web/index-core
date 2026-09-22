# Provider 能力聚焦调查

> 调查员：Discovery 02。只调查不开发。所有结论附源码证据（文件路径:行号 + 函数/结构名）。
> FACT = 源码直接证实；INFERENCE = 基于源码的推理。两者严格分开标注。

---

## SOURCE_VERSIONS

| 仓库 | URL | Branch | Commit SHA | Checked date | License |
|------|-----|--------|------------|--------------|---------|
| AList | https://github.com/alist-org/alist.git | main | `fb0731a6953012e7b72b89bf5473817caa4625f9` | 2026-09-23 | AGPL-3.0 |
| OpenList | https://github.com/OpenListTeam/OpenList.git | main | `3a31b438a94af2532608499b74251c630ddf0f6f` | 2026-09-23 | AGPL-3.0 |

- AList commit 时间：2026-09-19 20:13:50 +0800
- OpenList commit 时间：2026-09-21 16:54:22 +0800
- 两者 LICENSE 文件首行均为 `GNU AFFERO GENERAL PUBLIC LICENSE V3`（`/tmp/d02/alist/LICENSE:1`、`/tmp/d02/openlist/LICENSE:1`）
- AList drivers 目录条目数 95（含 `all.go` 聚合文件）；OpenList drivers 目录条目数 89。

---

## SUMMARY

1. **Provider 抽象**：AList 与 OpenList 的 driver 抽象**几乎完全相同**——核心 `Driver = Meta + Reader` 接口，能力通过 Go 可选接口（`Getter`/`Mkdir`/`Put`/`ArchiveReader`…）类型断言表达，非 bit field。OpenList 额外增加 `WithDetails`/`LinkCacheModeResolver`/`DirectUploader` 三个可选接口。`Obj` 接口暴露 `GetID()/GetPath()/GetHash()` 等 8 个方法，`model.Object` 结构含 `ID/Path/Name/Size/Modified/Ctime/IsFolder/HashInfo` 8 字段。
2. **API 字段**：**两者存在重大分歧**。AList 的 `/api/fs/list` 响应包含 `id`/`path`/`virtual_path` 以及完整分页元数据（`page`/`per_page`/`has_more`/`pages_total`/`total`）。OpenList 的 `/api/fs/list` 响应**完全不含** `id`/`path`/`virtual_path`，也**不含** 分页元数据（仅 `content`+`total`）。`hash_info`/`hashinfo` 两者都暴露但**driver 依赖**（local/webdav/aliyundrive 为空，google_drive/115 有值）。`is_dir`/`size`/`modified`/`created`/`name` 两者都直接提供。
3. **完整遍历 Root**：**有条件可靠**。driver.List 一次性返回目录全部内容，API 分页是内存切片（非服务端分页）。AList 有 `has_more` 信号；OpenList 需靠 `total` 与已获取数自行判断。`refresh=true` 可绕过 cache。多 root 通过 `/api/admin/storage/list`（需 admin）枚举 `MountPath`。**关键隐患**：遍历中途某 storage.List 出错时，若该路径下存在虚拟挂载点，错误被**静默吞没**（只 log，返回部分结果不报错），遍历者无法感知数据缺失。
4. **直接成为 IndexCore Collector**：**作为"全量快照型 Collector"可行，作为"增量/delta 型 Collector"不可行**。硬缺口：(a) OpenList 不暴露 provider object_id；(b) AList 的 id 字段 driver 依赖（local/webdav 返回空字符串）；(c) 无 native delta API；(d) 遍历错误静默吞没；(e) 分页为内存切片，超大目录有 OOM/超时风险。AList 自身搜索索引的 stable identity 就是 **path（Parent+Name）**，非 object_id 非 hash。若 IndexCore 接受 path 作为 identity + 全量遍历做 delta，则 AList/OpenList 够用，无需 rclone。

---

## Q1_PROVIDER_ABSTRACTION

### 1.1 AList Driver 接口（FACT）

`internal/driver/driver.go:9-14`：
```go
type Driver interface {
    Meta
    Reader
    //Writer
    //Other
}
```

`Meta` 接口（`driver.go:16-26`）：`Config() Config` / `GetStorage() *model.Storage` / `SetStorage(model.Storage)` / `GetAddition() Additional` / `Init(ctx) error` / `Drop(ctx) error`。

`Reader` 接口（`driver.go:32-39`）：
```go
type Reader interface {
    List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error)
    Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error)
}
```

可选能力接口（`driver.go:41-210`），通过**类型断言**检测：
- `GetRooter`（`GetRoot`）/ `Getter`（`Get`）
- `Mkdir` / `Move` / `Rename` / `Copy` / `Remove` / `Put` / `PutURL`
- `MkdirResult` / `MoveResult` / `RenameResult` / `CopyResult` / `PutResult` / `PutURLResult`（返回新 obj 的变体）
- `ArchiveReader`（`GetArchiveMeta`/`ListArchive`/`Extract`）/ `ArchiveGetter` / `ArchiveDecompress` / `ArchiveDecompressResult`
- `Reference`（`InitReference`）

类型断言用法示例，`internal/op/fs.go:178`：`if g, ok := storage.(driver.Getter); ok { ... }`；`internal/op/fs.go:188`：`if getRooter, ok := storage.(driver.GetRooter); ok { ... }`。

### 1.2 Features/Capabilities 表达方式（FACT）

**不是 bit field，是 Go 接口 + Config 布尔标志混合**：

`driver.Config` 结构（`internal/driver/config.go:3-16`）：
```go
type Config struct {
    Name              string
    LocalSort         bool
    OnlyLocal         bool
    OnlyProxy         bool
    NoCache           bool
    NoUpload          bool
    NeedMs            bool
    DefaultRoot       string
    CheckStatus       bool
    Alert             string
    NoOverwriteUpload bool
    ProxyRangeOption  bool
}
```

能力检测双轨制：
- 写能力（Mkdir/Move/Put 等）：通过可选接口断言（`storage.(driver.Mkdir)`）
- 读/缓存/代理特性：通过 `Config` 的布尔字段（`NoCache`/`OnlyProxy`/`LocalSort`）

### 1.3 Obj 接口与 Object 结构（FACT）

`internal/model/obj.go:27-39`：
```go
type Obj interface {
    GetSize() int64
    GetName() string
    ModTime() time.Time
    CreateTime() time.Time
    IsDir() bool
    GetHash() utils.HashInfo
    GetID() string    // driver 内部信息
    GetPath() string
}
```

`internal/model/object.go:41-50`：
```go
type Object struct {
    ID       string
    Path     string
    Name     string
    Size     int64
    Modified time.Time
    Ctime    time.Time
    IsFolder bool
    HashInfo utils.HashInfo
}
```

扩展类型（`object.go:106-119`）：`ObjThumb`（+Thumbnail）/ `ObjectURL`（+Url）/ `ObjThumbURL`（+Thumbnail+Url）。`HashInfo` 是 `map[*HashType]string`（`pkg/utils/hash.go:184-186`），支持 MD5/SHA1/SHA256（`hash.go:80-87`）。

### 1.4 OpenList Driver 抽象差异（FACT）

`internal/driver/driver.go:9-14`（OpenList）与 AList **逐字相同**（`Driver = Meta + Reader`，Reader.List/Link 签名一致）。

OpenList **新增**三个可选接口（`driver.go:199-220`）：
```go
type WithDetails interface {
    GetDetails(ctx context.Context) (*model.StorageDetails, error)
}
type LinkCacheModeResolver interface {
    ResolveLinkCacheMode(path string) LinkCacheMode
}
type DirectUploader interface {
    GetDirectUploadTools() []string
    GetDirectUploadInfo(ctx context.Context, tool string, dstDir model.Obj, fileName string, fileSize int64) (any, error)
}
```

OpenList 的 `Obj` 接口（`internal/model/obj.go:26-38`）与 AList 一致（8 方法）。`model.Object` 结构（`object.go:22-30`）含相同 8 字段。OpenList 额外有 `ObjDisplayName`/`ObjWithProvider` 接口（`obj.go:22-24,86-88`）。

**结论（FACT）**：driver 层抽象两者兼容；OpenList 是 AList 的超集（多了 3 个可选接口）。

---

## Q2_API_FIELDS

### 2.1 AList `/api/fs/list` 响应结构（FACT）

路由：`server/router.go:223` → `g.Any("/list", handles.FsList)`，路径 `/api/fs/list`，需登录（`Auth` 中间件，`router.go:69,111`）。

`FsListResp`（`server/handles/fsread.go:52-64`）：
```go
type FsListResp struct {
    Content       []ObjLabelResp `json:"content"`
    Total         int64          `json:"total"`
    FilteredTotal int64          `json:"filtered_total"`
    Page          int            `json:"page"`
    PerPage       int            `json:"per_page"`
    HasMore       bool           `json:"has_more"`
    PagesTotal    int            `json:"pages_total"`
    Readme        string         `json:"readme"`
    Header        string         `json:"header"`
    Write         bool           `json:"write"`
    Provider      string         `json:"provider"`
}
```

`ObjLabelResp`（`fsread.go:66-82`）字段：`id`/`path`/`virtual_path`/`name`/`size`/`is_dir`/`modified`/`created`/`sign`/`thumb`/`type`/`hashinfo`/`hash_info`/`label_list`/`storage_class`。

字段来源（`toObjsResp`，`fsread.go:301-339`）：
- `Id: obj.GetID()`（`fsread.go:321`）— **来自 driver**
- `Path: obj.GetPath()`（`fsread.go:322`）— **来自 driver**
- `VirtualPath: stdpath.Join(parent, obj.GetName())`（`fsread.go:323`）— **AList 计算**
- `Name/Size/IsDir/Modified/Created`（`fsread.go:324-328`）— **来自 driver**
- `HashInfoStr/HashInfo: obj.GetHash()`（`fsread.go:329-330`）— **来自 driver**
- `Sign: common.Sign(...)`（`fsread.go:331`）— **AList 计算**
- `Thumb: model.GetThumb(obj)`（`fsread.go:318`）— **来自 driver（可选）**
- `Provider`（`fsread.go:155`）= `storage.GetStorage().Driver` — **AList 填充**

### 2.2 AList `/api/fs/get` 响应结构（FACT）

`ObjResp`（`fsread.go:35-50`）与 `ObjLabelResp` 基本相同（无 `label_list`，有 `storage_class`）。`FsGetResp`（`fsread.go:346-354`）= `ObjResp` + `raw_url`/`readme`/`header`/`provider`/`web_proxy`/`related`。

`FsGet` 填充（`fsread.go:454-477`）：`Id: obj.GetID()`（`fsread.go:456`）、`Path: obj.GetPath()`（`fsread.go:457`）、`HashInfo: obj.GetHash().Export()`（`fsread.go:465`）。

### 2.3 OpenList `/api/fs/list` 响应结构（FACT）

路由：`server/router.go:196` → `g.Any("/list", handles.FsListSplit)`，路径 `/api/fs/list`，`Auth(true)`（允许 disabled guest，`router.go:109`）。

`FsListResp`（`server/handles/fsread.go:49-58`，OpenList）：
```go
type FsListResp struct {
    Content            []ObjResp `json:"content"`
    Total              int64     `json:"total"`
    Readme             string    `json:"readme"`
    Header             string    `json:"header"`
    Write              bool      `json:"write"`
    WriteContentBypass bool      `json:"write_content_bypass"`
    Provider           string    `json:"provider"`
    DirectUploadTools  []string  `json:"direct_upload_tools,omitempty"`
}
```

`ObjResp`（`fsread.go:35-47`，OpenList）：
```go
type ObjResp struct {
    Name         string                     `json:"name"`
    Size         int64                      `json:"size"`
    IsDir        bool                       `json:"is_dir"`
    Modified     time.Time                  `json:"modified"`
    Created      time.Time                  `json:"created"`
    Sign         string                     `json:"sign"`
    Thumb        string                     `json:"thumb"`
    Type         int                        `json:"type"`
    HashInfoStr  string                     `json:"hashinfo"`
    HashInfo     map[*utils.HashType]string `json:"hash_info"`
    MountDetails *model.StorageDetails      `json:"mount_details,omitempty"`
}
```

**OpenList `toObjsResp`（`fsread.go:228-248`）不设置 `Id`/`Path`/`VirtualPath`**：
```go
resp = append(resp, ObjResp{
    Name:         obj.GetName(),
    Size:         obj.GetSize(),
    IsDir:        obj.IsDir(),
    Modified:     obj.ModTime(),
    Created:      obj.CreateTime(),
    HashInfoStr:  obj.GetHash().String(),
    HashInfo:     obj.GetHash().Export(),
    Sign:         common.Sign(obj, parent, encrypt),
    Thumb:        thumb,
    Type:         utils.GetObjType(obj.GetName(), obj.IsDir()),
    MountDetails: mountDetails,
})
```

**关键差异（FACT）**：
| 字段 | AList `/api/fs/list` | OpenList `/api/fs/list` |
|------|---------------------|------------------------|
| `id`（provider object_id） | ✅ `obj.GetID()` | ❌ 不暴露 |
| `path`（driver 内部路径） | ✅ `obj.GetPath()` | ❌ 不暴露 |
| `virtual_path` | ✅ AList 计算 | ❌ 不暴露 |
| `hash_info` | ✅ driver 依赖 | ✅ driver 依赖 |
| `page`/`per_page`/`has_more`/`pages_total` | ✅ 全部提供 | ❌ 全部缺失 |
| `total` | ✅ | ✅ |
| `mount_details`/`direct_upload_tools` | ❌ | ✅ OpenList 新增 |

### 2.4 文件/目录区分、size、mtime、hash、provider_object_id 汇总（FACT）

- `is_dir`：两者都直接提供（`obj.IsDir()`）
- `size`：两者都直接提供（`obj.GetSize()`，int64）
- `modified`（mtime）：两者都直接提供（`obj.ModTime()`，time.Time）
- `created`（ctime）：两者都直接提供（`obj.CreateTime()`，`object.go:63-68`：若 Ctime 零值则回退到 ModTime）
- `hash`（`hash_info`）：两者 API 都有字段，但**值 driver 依赖**（见 COUNTEREXAMPLES）
- `provider_object_id`：AList 的 `id` 字段 driver 依赖；OpenList **API 层完全不暴露**
- driver 内部 `file_id` 是否暴露到 API：AList **是**（`id` 字段）；OpenList **否**

---

## Q3_COMPLETE_TRAVERSAL

### 3.1 分页机制（FACT）

**AList**：
- `ListReq` 嵌入 `model.PageReq{Page, PerPage}`（`fsread.go:22-27`，`internal/model/req.go:3-6`）
- `normalizeListPage`（`fsread.go:253-269`）：`PerPage=0 → 200`（DefaultPerPage）；`PerPage<0 → -1`（AllPerPage，返回全部）；`PerPage>500 → 500`（MaxPerPage）
- `pagination`（`fsread.go:284-299`）：**对已获取的全部 objs 做内存切片**，`start=(Page-1)*PerPage`，`end=start+PerPage`
- `HasMore = PerPage != AllPerPage && Page*PerPage < total`（`fsread.go:142`）
- `PagesTotal = ceil(total/PerPage)`（`fsread.go:271-282`）

**OpenList**：
- `PageReq.Validate()`（`internal/model/req.go:13-20`）：`Page<1 → 1`；`PerPage<1 → MaxInt`（即默认返回全部）
- `pagination`（`fsread.go:214-226`）：同样内存切片
- **响应不回传 `Page`/`PerPage`/`HasMore`/`PagesTotal`**（`FsListResp` 无这些字段，`fsread.go:49-58`）

**关键（FACT）**：两者的 `driver.List` 都**一次性返回目录全部内容**，API 分页是在**内存中对完整列表切片**。这不是服务端游标分页。含义：单次 `driver.List` 即完整目录列表；但超大目录（10万+文件）可能 OOM 或 HTTP 超时。

### 3.2 完整遍历信号（FACT）

- AList：`has_more`（bool）+ `pages_total`（int）+ `total`（int64）→ **有明确完整遍历信号**
- OpenList：仅 `total`（int64）→ **无 `has_more`**，需靠 `total` 与已获取 `len(content)` 自行判断是否遍历完毕。由于默认 `PerPage=MaxInt`，单次请求即返回全部，通常无需分页。

### 3.3 Cache 对遍历完整性的影响（FACT）

AList `internal/op/fs.go:111-170`（`List` 函数）：
```go
if !args.Refresh {
    if files, ok := listCache.Get(key); ok {
        return files, nil  // 命中缓存直接返回
    }
}
// ... storage.List ...
if !storage.Config().NoCache {
    listCache.Set(key, files, cache.WithEx(time.Minute*time.Duration(storage.GetStorage().CacheExpiration)))
}
```

OpenList `internal/op/fs.go:26-125` 同理：`if !args.Refresh { if dirCache, exists := Cache.dirCache.Get(key); exists { ... return objs, nil } }`（`fs.go:37-48`）。

- Cache TTL = `storage.CacheExpiration` 分钟（`model/storage.go:12`，默认 30，见 `op/driver.go:79`）
- `Refresh=true` **可绕过 cache**（两者一致）
- **影响（INFERENCE）**：cache 命中时返回的列表可能过期（文件新增/删除/重命名未反映）。遍历前若不传 `refresh=true`，可能拿到不完整或含幽灵条目的列表。`refresh=true` 需要写权限（AList `fsread.go:118-121`：`!HasPermission(PermWrite) && !CanWrite(meta,path) && req.Refresh → 403`）。

### 3.4 多 storage/root 区分（FACT）

- 枚举所有 storage：`/api/admin/storage/list` → `ListStorages`（`server/handles/storage.go:16-33`），返回 `[]model.Storage`（含 `MountPath`/`Driver`/`ID` 等）。**需 admin 权限**（`router.go:121`：`admin(auth.Group("/admin", middlewares.AuthAdmin))`）。
- `model.Storage`（`internal/model/storage.go:7-22`）：`ID uint`（主键）/ `MountPath string`（`gorm:"unique"`）/ `Driver string` / `CacheExpiration int` / `Status` / `Addition` / `Disabled` / `DisableIndex`。
- 虚拟根聚合：`fs/list.go:15-47`（AList）调用 `op.GetStorageVirtualFilesByPath(path)`（`op/storage.go:388-417`）返回挂载点名作为虚拟目录，与 `storage.List` 结果 `om.Merge`（`list.go:45`）。OpenList `fs/list.go:16-49` 同理。
- 路径路由：`op.GetStorageAndActualPath`（`op/path.go:15-30`）→ `GetBalancedStorage`（`op/storage.go:422-437`）按最长前缀匹配 `MountPath`，多个匹配时**轮询负载均衡**（`balanceMap`，`storage.go:433-435`）。balance storage 通过 `.balance` 后缀区分（`pkg/utils/balance.go:12-17`）。

**结论（FACT）**：多 root 通过 admin API 枚举 `MountPath`；每个 `MountPath` 是一个遍历起点。通常 `MountPath` 唯一（`gorm:"unique"`），负载均衡仅在显式配置 `.balance` 后缀时生效。

### 3.5 遍历中途 Provider 错误处理（FACT）

AList `internal/fs/list.go:25-39`：
```go
if storage != nil {
    _objs, err = op.List(ctx, storage, actualPath, ...)
    if err != nil {
        if !args.NoLog { log.Errorf("fs/list: %+v", err) }
        if len(virtualFiles) == 0 {
            return nil, errors.WithMessage(err, "failed get objs")
        }
        // 若 virtualFiles != 0，**不返回 error**，继续
    }
}
objs := om.Merge(_objs, virtualFiles...)
return objs, nil
```

OpenList `internal/fs/list.go:26-49` **相同逻辑**。

**关键（FACT）**：当 `storage.List` 出错但该路径下存在虚拟挂载点（`virtualFiles` 非空）时，**错误被静默吞没**，返回部分结果（仅虚拟目录，缺失实际文件），HTTP 响应仍为 200 成功。遍历者**无法通过 API 响应感知**该 storage 数据缺失。

`WalkFS`（AList `internal/fs/walk.go:17-45`，OpenList `internal/fs/walk.go:18-46`）同样：`if err != nil { return walkFnErr }`（walkFnErr 通常是 nil）→ 跳过该目录继续遍历，不上抛错误。

### 3.6 内部遍历辅助（FACT）

两者都有 `fs.WalkFS(ctx, depth, name, info, walkFn)`（有限深度递归遍历），但**仅用于内部搜索索引构建**（AList `internal/search/build.go:182`），**不暴露为 HTTP API**。

OpenList 额外有 `/api/scan/start`（`server/handles/scan.go:14-25`，admin）触发 `op.BeginManualScan` → `RecursivelyList`（`op/recursive_list.go:52-125`），但 `/api/scan/progress`（`scan.go:41-47`）**仅返回 `obj_count`/`is_done`，不返回文件列表**。该 API 用于预热缓存/构建索引，对 Collector 取数无用。

---

## Q4_COLLECTOR_FEASIBILITY

### 4.1 已证实的 Collector 能力（FACT）

| 能力 | AList | OpenList | 证据 |
|------|-------|----------|------|
| 列出目录内容 | ✅ `/api/fs/list` | ✅ `/api/fs/list` | `fsread.go:90`/`fsread.go:80` |
| 获取单文件元数据 | ✅ `/api/fs/get` | ✅ `/api/fs/get` | `fsread.go:356`/`fsread.go:283` |
| 区分文件/目录 | ✅ `is_dir` | ✅ `is_dir` | `fsread.go:326`/`fsread.go:236` |
| size | ✅ | ✅ | `fsread.go:325`/`fsread.go:235` |
| mtime / ctime | ✅ `modified`/`created` | ✅ `modified`/`created` | `fsread.go:327-328`/`fsread.go:237-238` |
| hash | ✅ driver 依赖 | ✅ driver 依赖 | `fsread.go:330`/`fsread.go:239` |
| 枚举所有 root | ✅ `/api/admin/storage/list` | ✅ `/api/admin/storage/list` | `storage.go:16`（需 admin） |
| 绕过 cache | ✅ `refresh=true` | ✅ `refresh=true` | `op/fs.go:118`/`op/fs.go:37` |
| 分页信号 | ✅ `has_more`/`pages_total` | ⚠️ 仅 `total` | `fsread.go:142`/`fsread.go:49-58` |
| provider 标识 | ✅ `provider` 字段 | ✅ `provider` 字段 | `fsread.go:155`/`fsread.go:124` |

### 4.2 硬缺口（FACT + INFERENCE）

1. **OpenList 不暴露 provider object_id**（FACT，`fsread.go:35-47` ObjResp 无 `id` 字段，`fsread.go:228-248` toObjsResp 不设 Id）→ OpenList 上 stable identity 只能用 path。
2. **AList 的 `id` 字段 driver 依赖且不保证非空**（FACT，见 COUNTEREXAMPLES：local/webdav 返回空字符串）→ AList 上 `id` 不能作为通用 stable identity。
3. **无 native delta API**（FACT）：`/api/fs/list` 无 `since`/`cursor`/`if_modified_since` 参数，`ListReq`（`fsread.go:22-27`）仅 `Page`/`Path`/`Password`/`Refresh`。无"列出变更"接口。
4. **遍历错误静默吞没**（FACT，`fs/list.go:32-38`）→ 遍历完整性无法通过 API 响应保证，需外部一致性校验。
5. **分页为内存切片非服务端分页**（FACT，`fsread.go:284-299`）→ 超大单目录（10万+文件）有 OOM/超时风险，且 `driver.List` 必须先全量获取才能分页。
6. **hash 无法服务端计算**（FACT）：`hash_info` 仅来自 driver（provider API 返回的），无法请求 AList/OpenList 对 local driver 文件计算 hash。local driver 的 `FileInfoToObj`（`local/driver.go:167-205`）不设置 `HashInfo`。
7. **`refresh=true` 需要写权限**（FACT，AList `fsread.go:118-121`）→ 只读账户无法强制刷新 cache，可能拿到过期数据。

### 4.3 Stable Identity 问题（FACT + INFERENCE）

AList 自身搜索索引的 identity 机制（FACT）：
- `model.SearchNode`（`internal/model/search.go:23-28`）：仅 `Parent`/`Name`/`IsDir`/`Size`，**无 ID/Hash 字段**。
- 索引构建（`internal/search/build.go:169-174`）：`ObjWithParent{Obj: info, Parent: path.Dir(indexPath)}` → identity = **Parent + Name（路径）**。
- 增量更新（`build.go:224-234`）：用 `name` 集合差异（`now.Difference(old)`）检测新增/删除，**基于 name 而非 ID**。

OpenList `model.SearchNode`（`internal/model/search.go:23-28`）**相同**：`Parent`/`Name`/`IsDir`/`Size`。

**结论（INFERENCE）**：AList/OpenList 自身设计就是以 **path（Parent+Name）** 作为 stable identity，而非 provider object_id 或 hash。path 在 rename/move 后变化 → identity 不稳定，但这是 AList 自身接受的设计取舍。若 IndexCore 沿用此模型，AList/OpenList 可直接适配。

### 4.4 Delta 问题（FACT + INFERENCE）

- **全量**：可行。递归遍历所有 root → 所有目录 → 收集全部条目，构建全量快照。
- **增量（native delta）**：**不可行**。无 `since`/`cursor` API（见 4.2 第 3 点）。
- **增量（对比 delta）**：可行但需客户端实现。全量遍历后与上次快照按 path 对比（新增/删除/修改）。AList 内部 `search.Update`（`build.go:202-268`）正是此模式。
- **mtime-based delta**：部分可行。`modified` 字段可做"自某时间点后变更"的客户端过滤，但 AList 不保证目录的 `modified` 在子文件变更时更新（driver 依赖）。

### 4.5 是否需要 rclone 补充（INFERENCE）

- 若 IndexCore 的 Collector 需求为**全量快照 + path identity**：AList/OpenList **够用**，无需 rclone。AList 更优（暴露 `id`/`path`/`has_more`）。
- 若需要 **provider object_id 作 stable identity**：AList 部分满足（仅 id-setting driver），OpenList 不满足。rclone 的 `--metadata` 可暴露 provider id（如 Drive file_id），但 rclone 同样不提供 native delta（除少数 backend 如 S3 versioning）。
- 若需要 **native delta**：两者都不满足，rclone 也不普遍满足。需直接对接 provider SDK（如 Google Drive Changes API、OneDrive delta）。
- **结论**：对于"全量快照型 Collector"，AList/OpenList 够用，rclone 无额外优势。对于"delta 型 Collector"，三者都需 provider-specific 方案。

---

## CAPABILITY_MATRIX

> AList `/api/fs/list` × SnapshotEntry 字段。每格填 DIRECT / DERIVABLE / DRIVER_DEPENDENT / UNAVAILABLE。
> DIRECT = API 直接提供且非空；DERIVABLE = 需客户端计算/拼接；DRIVER_DEPENDENT = API 有字段但值取决于 driver 是否填充；UNAVAILABLE = API 无此字段。

| SnapshotEntry 字段 | AList `/api/fs/list` | OpenList `/api/fs/list` | 证据（AList） |
|--------------------|---------------------|------------------------|---------------|
| `path`（完整逻辑路径） | DIRECT（`virtual_path`） | DERIVABLE（parent + name 拼接） | `fsread.go:323,69` |
| `name` | DIRECT | DIRECT | `fsread.go:324,36` |
| `is_dir` | DIRECT | DIRECT | `fsread.go:326,38` |
| `size` | DIRECT | DIRECT | `fsread.go:325,37` |
| `mtime` | DIRECT（`modified`） | DIRECT（`modified`） | `fsread.go:327,39` |
| `ctime` | DIRECT（`created`，可能回退 mtime） | DIRECT（`created`，可能回退 mtime） | `fsread.go:328,40`；`object.go:63-68` |
| `hash` | DRIVER_DEPENDENT（`hash_info`） | DRIVER_DEPENDENT（`hash_info`） | `fsread.go:330,45`；COUNTEREXAMPLES |
| `provider_object_id` | DRIVER_DEPENDENT（`id`） | UNAVAILABLE | `fsread.go:321`；OpenList `fsread.go:35-47` 无 `id` |
| `provider`（driver 名） | DIRECT（`FsListResp.provider`） | DIRECT（`FsListResp.provider`） | `fsread.go:155,63` |
| `mount_path` | DERIVABLE（从 `/api/admin/storage/list`） | DERIVABLE（从 `/api/admin/storage/list`） | `storage.go:16` |
| `storage_id`（AList 内部） | DERIVABLE（从 storage list） | DERIVABLE（从 storage list） | `storage.go:8` |
| `sign`（下载签名） | DIRECT | DIRECT | `fsread.go:331,41` |
| `thumb`（缩略图 URL） | DRIVER_DEPENDENT | DRIVER_DEPENDENT | `fsread.go:318,42` |
| `storage_class` | DRIVER_DEPENDENT | UNAVAILABLE | `fsread.go:319,81`；OpenList 无 |
| `raw_url`（下载 URL） | DIRECT（仅 `/api/fs/get`） | DIRECT（仅 `/api/fs/get`） | `fsread.go:348,257` |
| `has_more`（分页信号） | DIRECT | UNAVAILABLE | `fsread.go:142,58`；OpenList `fsread.go:49-58` 无 |
| `total`（目录条目总数） | DIRECT | DIRECT | `fsread.go:144,51` |

---

## COUNTEREXAMPLES

> 反例：具体 driver 缺什么字段。全部为 FACT（源码直接验证）。

### C1. local driver — `id` 和 `hash` 均空

`drivers/local/driver.go:191-204`（`FileInfoToObj`）：
```go
file := model.ObjThumb{
    Object: model.Object{
        Path:     filePath,
        Name:     f.Name(),
        Modified: f.ModTime(),
        Size:     size,
        IsFolder: isFolder,
        Ctime:    ctime,
    },
    Thumbnail: model.Thumbnail{Thumbnail: thumb},
}
```
**未设置 `ID` 和 `HashInfo`**。→ AList API `id` 字段为空字符串，`hash_info` 为空 map。

### C2. webdav driver — `id`/`path`/`hash` 均空

`drivers/webdav/driver.go:55-62`（`List`）：
```go
return utils.SliceConvert(files, func(src os.FileInfo) (model.Obj, error) {
    return &model.Object{
        Name:     src.Name(),
        Size:     src.Size(),
        Modified: src.ModTime(),
        IsFolder: src.IsDir(),
    }, nil
})
```
**仅设置 Name/Size/Modified/IsFolder**，未设置 `ID`/`Path`/`HashInfo`。→ AList API `id`/`path`/`hash_info` 均空。

### C3. aliyundrive driver — `id` 有值，`hash` 空

`drivers/aliyundrive/types.go:34-45`（`fileToObj`）：
```go
return &model.ObjThumb{
    Object: model.Object{
        ID:       f.FileId,   // ✅ 设置 provider file_id
        Name:     f.Name,
        Size:     f.Size,
        Modified: f.UpdatedAt,
        IsFolder: f.Type == "folder",
    },
    Thumbnail: model.Thumbnail{Thumbnail: f.Thumbnail},
}
```
**设置 `ID = f.FileId`，未设置 `HashInfo`**。→ AList API `id` 有值（阿里云盘 file_id），`hash_info` 空。OpenList API `id` 不暴露（即使 driver 设置了）。

### C4. 115 driver — `id` 和 `hash` 均有值

`drivers/115/types.go:13-24`：
```go
type FileObj struct {
    driver.File   // 嵌入 115driver SDK 的 File（含 GetID()）
    ThumbURL string
}
func (f *FileObj) GetHash() utils.HashInfo {
    return utils.NewHashInfo(utils.SHA1, f.Sha1)  // ✅ SHA1
}
```
`drivers/115/driver.go:61-63`：`return utils.SliceConvert(files, func(src FileObj) (model.Obj, error) { return &src, nil })`。→ `id`（file_id）和 `hash`（SHA1）均有值。

### C5. google_drive driver — `id`/`hash` 均有值（多 hash）

`drivers/google_drive/types.go:40-66`（`fileToObj`）：
```go
obj := &model.ObjThumb{
    Object: model.Object{
        ID:       f.Id,           // ✅ Google file id
        ...
        HashInfo: utils.NewHashInfoByMap(map[*utils.HashType]string{
            utils.MD5:    f.MD5Checksum,
            utils.SHA1:   f.SHA1Checksum,
            utils.SHA256: f.SHA256Checksum,
        }),
    },
    ...
}
```
→ `id` + MD5/SHA1/SHA256 均有值（取决于 Google Drive 返回）。

### C6. OpenList API 层反例 — driver 设 ID 但 API 不暴露

OpenList `drivers/aliyundrive/types.go:37` 设置 `ID: f.FileId`（与 AList 一致），但 `server/handles/fsread.go:228-248`（`toObjsResp`）不读取 `obj.GetID()`。→ **driver 内部有 provider object_id，HTTP API 不暴露**。

---

## VERIFIED_FACTS

> 全部为源码直接证实（FACT）。

1. AList `Driver` 接口 = `Meta + Reader`，`Reader = List + Link`（`internal/driver/driver.go:9-39`）。
2. OpenList `Driver` 接口与 AList 逐字相同（`internal/driver/driver.go:9-39`）。
3. 能力检测通过 Go 接口类型断言（`op/fs.go:178`：`storage.(driver.Getter)`），非 bit field。
4. `model.Obj` 接口 8 方法：GetSize/GetName/ModTime/CreateTime/IsDir/GetHash/GetID/GetPath（`internal/model/obj.go:27-39`）。
5. `model.Object` 8 字段：ID/Path/Name/Size/Modified/Ctime/IsFolder/HashInfo（`internal/model/object.go:41-50`）。
6. AList `/api/fs/list` 响应含 `id`/`path`/`virtual_path`/`has_more`/`pages_total`（`server/handles/fsread.go:52-82`）。
7. OpenList `/api/fs/list` 响应**不含** `id`/`path`/`virtual_path`/`has_more`/`pages_total`（`server/handles/fsread.go:35-58`）。
8. AList `toObjsResp` 设置 `Id: obj.GetID()`（`fsread.go:321`）；OpenList `toObjsResp` **不设置** Id（`fsread.go:228-248`）。
9. local driver 不设置 `ID`/`HashInfo`（`drivers/local/driver.go:191-204`）。
10. webdav driver 不设置 `ID`/`Path`/`HashInfo`（`drivers/webdav/driver.go:55-62`）。
11. aliyundrive driver 设置 `ID = f.FileId`，不设置 `HashInfo`（`drivers/aliyundrive/types.go:34-45`）。
12. 115 driver 实现 `GetHash()` 返回 SHA1（`drivers/115/types.go:22-24`）。
13. google_drive driver 设置 `ID` + MD5/SHA1/SHA256（`drivers/google_drive/types.go:40-66`）。
14. AList 分页为内存切片（`fsread.go:284-299`），`driver.List` 一次性返回目录全部内容。
15. OpenList 分页为内存切片（`fsread.go:214-226`），默认 `PerPage=MaxInt`（`req.go:13-20`）。
16. `Refresh=true` 绕过 cache（AList `op/fs.go:118`，OpenList `op/fs.go:37`）。
17. Cache TTL = `storage.CacheExpiration` 分钟（AList `op/fs.go:161`，OpenList `op/fs.go:107-108`）。
18. 多 storage 枚举需 admin（`/api/admin/storage/list`，`router.go:121,156-157`）。
19. 遍历中途 `storage.List` 出错且 `virtualFiles` 非空时，错误被吞没，返回部分结果（AList `fs/list.go:32-38`，OpenList `fs/list.go:32-39`）。
20. AList 搜索索引 identity = Parent + Name（`model.SearchNode` 仅 Parent/Name/IsDir/Size，`search.go:23-28`；`build.go:169-174`）。
21. OpenList `SearchNode` 同样仅 Parent/Name/IsDir/Size（`search.go:23-28`）。
22. 无 native delta API：`ListReq` 无 `since`/`cursor`（AList `fsread.go:22-27`，OpenList `fsread.go:22-27`）。
23. 两者都有内部 `WalkFS`（AList `fs/walk.go:17`，OpenList `fs/walk.go:18`），不暴露为 HTTP API。
24. OpenList `/api/scan/progress` 仅返回 `obj_count`/`is_done`，不返回文件列表（`scan.go:41-47`）。
25. AList `refresh=true` 需要写权限（`fsread.go:118-121`）。
26. `model.Storage.MountPath` 有 `gorm:"unique"` 约束（`storage.go:9`）。
27. balance storage 通过 `.balance` 后缀区分（`pkg/utils/balance.go:5,12-17`）。
28. `HashInfo` 支持 MD5/SHA1/SHA256（`pkg/utils/hash.go:80-87`）。
29. `CreateTime()` 若 Ctime 零值则回退 ModTime（`object.go:63-68`）。
30. 两者 License 均为 AGPL-3.0（`LICENSE:1`）。

---

## UNVERIFIED

> 以下未在本次源码调查中直接验证，标注为 INFERENCE 或需进一步确认。

1. **(INFERENCE)** 超大目录（10万+文件）的实际 OOM/超时行为——源码显示 `driver.List` 一次性返回全部，但未实测具体 driver（如 Google Drive 分页 API 在 driver 内部如何聚合）。Google Drive driver 内部用 `nextPageToken` 循环聚合（`drivers/google_drive/util.go:197` 提到 `nextPageToken`），聚合后一次性返回——超大目录可能触发 provider API 限流或 AList 内存压力，但未实测。
2. **(INFERENCE)** OpenList 移除 `id`/`path`/分页字段是否为有意设计决策（可能是安全考虑，避免暴露 driver 内部路径），未查 commit message / RFC。
3. **(INFERENCE)** `storage_class` 字段（AList 有，OpenList 无）是否影响 Collector——仅 S3 类 driver 有意义，未深究。
4. **(UNVERIFIED)** AList 的 `id` 字段在 rename 后是否稳定——provider file_id 通常 rename 不变，但 local/webdav 的 `id` 为空无所谓稳定，aliyundrive 的 file_id 由 provider 保证。未查各 provider 文档。
5. **(UNVERIFIED)** OpenList 是否有其他 API（非 `/api/fs/list`）暴露 object_id——仅查了 `fsread.go`，未全面扫描所有 handler。
6. **(INFERENCE)** `WalkFS` 的 `depth` 限制——AList 搜索索引默认 `MaxIndexDepth=20`（`build.go:260`），足够深，但非无限。全量遍历需 depth 足够大或无限制。
7. **(UNVERIFIED)** 两者是否有 WebSocket / streaming list API——仅查了 REST handler，未查 `server/mcp` 等其他模块。

---

## RISKS

1. **遍历完整性静默失败（高）**：`storage.List` 出错时返回部分结果不报错（`fs/list.go:32-38`）。Collector 若不做事后校验，索引会静默缺数据。**缓解**：遍历后对比目录数与 `total`，或对每个 storage 做健康检查。
2. **stable identity 不稳定（中）**：path 在 rename/move 后变化。AList 自身接受此风险（搜索索引用 path）。若 IndexCore 需要跨 rename 稳定 identity，需用 AList `id` 字段（仅 id-setting driver）或 hash（仅 hash-setting driver），且 OpenList 完全无 `id`。
3. **cache 过期（中）**：不传 `refresh=true` 时返回缓存列表，可能过期。`refresh=true` 需写权限（AList）。**缓解**：Collector 用 admin token + `refresh=true`。
4. **超大目录 OOM/超时（中）**：分页是内存切片，`driver.List` 必须先全量获取。单目录 10万+文件可能 OOM 或 HTTP 超时。**缓解**：限制单 storage 规模，或分拆 mount path。
5. **无 native delta（高）**：全量遍历做 delta，大规模 root 频繁全量成本高。**缓解**：用 mtime 客户端过滤（不保证目录 mtime 反映子文件变更），或接受全量。
6. **OpenList API 字段缺失（中）**：无 `id`/`path`/`has_more`。若 IndexCore 设计依赖 provider object_id，OpenList 不可用，只能用 AList。**缓解**：统一用 path identity。
7. **balance storage 路由不确定性（低）**：多 storage 同 mount path 前缀时轮询（`storage.go:433-435`），遍历可能命中不同 storage。但 `MountPath` unique，仅显式 `.balance` 配置时触发。**缓解**：不使用 balance storage 配置。
8. **AGPL-3.0 传染性（高，非技术）**：AList/OpenList 均为 AGPL-3.0。若 IndexCore 以网络服务形式集成（调用其 API 不传染，但嵌入/修改源码传染），需法务确认。本调查仅通过 HTTP API 集成，不修改其源码，风险较低。
9. **driver 实现质量参差（中）**：95/89 个 driver，每个对 `ID`/`HashInfo`/`Path` 的填充不一致（见 COUNTEREXAMPLES）。Collector 不能假设任何字段非空，必须按 driver 类型降级处理。

---

## LICENSE

- AList: **GNU Affero General Public License v3**（`/tmp/d02/alist/LICENSE:1`，661 行完整文本）
- OpenList: **GNU Affero General Public License v3**（`/tmp/d02/openlist/LICENSE:1`，661 行完整文本）

**合规说明（INFERENCE，非法务结论）**：通过 HTTP API 调用 AList/OpenList（作为独立服务部署）通常不触发 AGPL 传染（API 调用是"交互"而非"衍生作品"）。但嵌入其源码、修改后分发需开源。IndexCore 作为 Collector 客户端通过 API 集成，风险较低。最终需法务确认。