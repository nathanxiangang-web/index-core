# D03 Worker A: rclone Stable Identity / Metadata

> 调查者：Foreman（自查，非远程 Worker）
> 源码：rclone commit `cfb90e3`，`/tmp/d03/rclone`
> 日期：2026-09-23

## 1. 核心接口定义

### FACT 1.1: Object 接口不包含 ID()

`fs/types.go:82-101` — `Object` 接口继承 `ObjectInfo`，包含 `SetModTime`、`Open`、`Update`、`Remove`。**没有 `ID()` 方法。**

### FACT 1.2: ObjectInfo 接口不包含 ID()

`fs/types.go:103-113` — `ObjectInfo` 继承 `DirEntry`，增加 `Hash()` 和 `Storable()`。**没有 `ID()` 方法。**

### FACT 1.3: DirEntry 是最基础接口，只有 Remote/ModTime/Size

`fs/types.go:118-134` — `DirEntry` 包含 `Fs()`、`String()`、`Remote()`、`ModTime()`、`Size()`。**没有 `ID()`、`Hash()`、`ParentID()`。**

### FACT 1.4: ID() 是可选接口 IDer

`fs/types.go:166-170`:
```go
// IDer is an optional interface for Object
type IDer interface {
    // ID returns the ID of the Object if known, or "" if not
    ID() string
}
```

### FACT 1.5: ParentID() 也是可选接口

`fs/types.go:172-176`:
```go
// ParentIDer is an optional interface for Object
type ParentIDer interface {
    // ParentID returns the ID of the parent directory if known or nil if not
    ParentID() string
}
```

### FACT 1.6: FullObject 包含 IDer 但 Object 不包含

`fs/types.go:236-248` — `FullObject` 包含 `IDer` 等所有可选接口，但核心 `Object` 接口不包含。`ObjectOptionalInterfaces`（line 252-283）通过 type assertion 检测 `IDer` 是否实现。

## 2. Backend ID() 实现矩阵

### FACT 2.1: 以下 backend 实现了 Object.ID()

通过 `grep 'func (.*Object.*) ID() string' backend/` 查到 38 处实现（含 wrapper backend）。关键 backend：

| Backend | Object.ID() | 返回值 | 源码位置 |
|---------|-------------|--------|----------|
| onedrive | YES | `o.id`（OneDrive item ID） | `backend/onedrive/onedrive.go:2912` |
| drive | YES | `o.id`（Google Drive file ID） | `backend/drive/drive.go:4637` |
| dropbox | YES | `o.id`（Dropbox file rev/id） | `backend/dropbox/dropbox.go:1865` |
| box | YES | `o.id` | `backend/box/box.go:1777` |
| b2 | YES | `o.id` | `backend/b2/b2.go:2408` |
| mega | YES | `o.id` | `backend/mega/mega.go:1264` |

### FACT 2.2: 以下 backend 未实现 Object.ID()

| Backend | Object.ID() | 证据 |
|---------|-------------|------|
| **local** | NO | `grep` 无匹配；`Directory.ID()` 存在但返回 `""`（`backend/local/local.go:2023`） |
| **webdav** | NO | `grep` 无匹配 |
| **s3** | NO | `grep` 无匹配 |
| **azureblob** | NO | `grep` 无匹配 |
| **sftp** | NO | `grep` 无匹配 |
| **ftp** | NO | `grep` 无匹配 |
| **http** | NO | `grep` 无匹配 |

### FACT 2.3: ParentID() 实现极少

仅 6 个 backend 实现 `ParentID()`：drive、pikpak、opendrive、gofile、filen、drime（`backend/drive/drive.go:4642` 等）。onedrive、dropbox、box、s3、local、webdav 均未实现。

## 3. ID 如何暴露给用户

### FACT 3.1: lsjson 命令条件性填充 ID

`fs/operations/lsjson.go:207-209`:
```go
if do, ok := entry.(fs.IDer); ok {
    item.ID = do.ID()
}
```

如果 backend 不实现 `IDer`，`item.ID` 保持零值（空字符串）。用户无法区分"ID 未知"和"backend 不支持 ID"。

### FACT 3.2: metadata mapper 同样条件性填充

`fs/metadata.go:115-117`:
```go
if do, ok := o.(IDer); ok {
    in.ID = do.ID()
}
```

### FACT 3.3: operations/list RC 命令通过 lsjson 返回

`fs/operations/rc.go:59-80` — `rcList` 调用 `ListJSON`，返回 `list` 数组。无 completeness marker、无 total count、无 pagination cursor。

## 4. Hash 作为替代 identity 机制

### FACT 4.1: Hash() 在核心 ObjectInfo 接口中

`fs/types.go:108-109` — `Hash(ctx, ty hash.Type) (string, error)` 是 `ObjectInfo` 的方法。如果 hash 不可用返回 `""`。

### FACT 4.2: 各 backend Hash 支持不同

- **local**: 支持 md5/sha1 等（`backend/local/local.go:1267`，通过本地计算）
- **webdav**: 支持 provider 返回的 etag（`backend/webdav/webdav.go:1390`）
- **s3**: 仅支持 MD5（`backend/s3/s3.go:4145-4146`，`if t != hash.MD5 { return "", hash.ErrUnsupported }`）

### INFERENCE 4.1: Hash 不是 stable identity

Hash 是内容指纹，不是资源标识。同一文件 rename 后 hash 不变但 path 变了；不同文件可能有相同 hash（碰撞）。Hash 适合做内容去重，不适合做资源追踪。

## 5. 结论

### CONCLUSION 1: rclone 没有跨 backend 通用 stable resource identity

- `ID()` 是可选接口 `IDer`，不在核心 `Object` 接口中
- 7 个关键 backend（local、webdav、s3、azureblob、sftp、ftp、http）不实现 `ID()`
- 即使实现了 `ID()`，返回的是 provider native ID（OneDrive item ID、Drive file ID 等），格式和语义各不相同
- `lsjson` 对不实现 `IDer` 的 backend 静默返回空 ID，用户无法区分"不支持"和"未知"
- `ParentID()` 实现更少（仅 6 个 backend），无法构建统一的 ID+ParentID 树

### CONCLUSION 2: rclone 的 identity 能力是 DRIVER_DEPENDENT

- cloud backends（drive、onedrive、dropbox、box、b2）有 provider native ID，在 rename/move 后稳定
- protocol backends（local、webdav、s3、sftp、ftp、http）没有 ID，只有 path（`Remote()`）
- path 在 rename/move 后变化，不是 stable identity

### CONCLUSION 3: Hash 不能替代 stable identity

Hash 是内容指纹，不是资源标识。适合去重，不适合跨快照追踪资源。