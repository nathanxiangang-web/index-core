# W-A — AList / OpenList 搜索索引内部机制调查 (D02)

> 调查员：Discovery 02。只调查不开发。所有结论附源码证据（文件路径:行号 + 函数/结构名）。
> FACT = 源码直接证实；INFERENCE = 基于源码的推理。两者严格分开标注。

---

## SOURCE_VERSIONS

| 仓库 | URL | Branch | Commit SHA | Checked date | License |
|------|-----|--------|------------|--------------|---------|
| AList | https://github.com/alist-org/alist.git | main | `fb0731a6953012e7b72b89bf5473817caa4625f9` | 2026-09-23 | AGPL-3.0 |
| OpenList | https://github.com/OpenListTeam/OpenList.git | main | `3a31b438a94af2532608499b74251c630ddf0f6f` | 2026-09-23 | AGPL-3.0 |

---

## SUMMARY

AList/OpenList 搜索索引是一个**轻量级目录名搜索辅助工具**，不是 Canonical Inventory。它以 `Parent + Name` 为 key，通过目录级 name diff 做增量更新，无 checkpoint/resume、无 staging、无 atomic reconcile。Provider 返回部分数据时，缺失文件会被错误删除。rename/move 在索引中表现为 delete-old-name + add-new-name，不是 rename 操作。

---

## Q1. BuildIndex / rebuild 入口

**FACT**：两个 HTTP 入口（admin 权限）：

| 端点 | Handler | 行为 |
|------|---------|------|
| `/api/admin/index/build` | `handles.BuildIndex` (`server/handles/index.go:21`) | 先 `search.Clear`（清空全部索引），再 `search.BuildIndex(["/"], ignorePaths, maxDepth=20, count=true)` |
| `/api/admin/index/update` | `handles.UpdateIndex` (`server/handles/index.go:42`) | 对指定 paths：先 `search.Del(path)` 逐个删除，再 `search.BuildIndex(paths, ignorePaths, req.MaxDepth, count=false)` |

`BuildIndex` handler 是**全量重建**（clear + build from root）。`UpdateIndex` handler 是**指定路径重建**（delete + build specific paths）。

**FACT**：`StopIndex` (`index.go:74`) 通过向 `Quit` channel 发信号停止构建。`ClearIndex` (`index.go:87`) 清空索引并重置 progress。`GetProgress` (`index.go:102`) 返回 `IndexProgress`。

---

## Q2. BuildIndex 完整调用链

**FACT**（AList `internal/search/build.go:33-188`）：

```
handles.BuildIndex
  → search.Clear(ctx)                          // 清空 DB
  → search.BuildIndex(ctx, ["/"], ignorePaths, 20, true)
      → Quit.CompareAndSwap(nil, &quit)         // 防并发（line 42）
      → indexMQ = mq.NewInMemoryMQ[]()          // 内存消息队列（line 47）
      → go func() { ... }                       // 消费者 goroutine（line 53）
      → fs.WalkFS(ctx, maxDepth, indexPath, fi, walkFn)  // 遍历（line 182）
          → walkFn: indexMQ.Publish(ObjWithParent{Obj: info, Parent: path.Dir(indexPath)})  // line 169
      → 消费者: indexMQ.ConsumeAll → BatchIndex → instance.BatchIndex(searchNodes)  // line 73-84
      → DB: db.CreateInBatches(nodes, 1000)     // internal/db/searchnode.go:28
```

**关键**：`indexMQ` 是**内存消息队列**，每秒或满 1000 条触发一次 `BatchIndex`（`build.go:66-67`）。进程重启丢失。

---

## Q3. SearchNode 数据模型

**FACT**（AList `internal/model/search.go:23-28`，OpenList `internal/model/search.go:23-28` — 两者相同）：

```go
type SearchNode struct {
    Parent string `json:"parent" gorm:"index"`
    Name   string `json:"name"`
    IsDir  bool   `json:"is_dir"`
    Size   int64  `json:"size"`
}
```

**4 个字段**：`Parent`（父目录虚拟路径）、`Name`（文件名）、`IsDir`、`Size`。

**不含**：`ID`、`Hash`、`Modified`、`Created`、`Path`、`Provider`。

**FACT**：`IndexProgress`（`model/search.go:8-13`）：`ObjCount uint64`、`IsDone bool`、`LastDoneTime *time.Time`、`Error string`。存储在 settings 表中（`search/util.go:17-39`），非独立表。

---

## Q4. 自动 Update 如何触发

**FACT**（AList）：

1. `op.List` (`internal/op/fs.go:111-170`) 在 `storage.List` 成功后，异步调用 hook：
   ```go
   if !args.NoUpdateIndex {
       go func(reqPath string, files []model.Obj) {
           HandleObjsUpdateHook(reqPath, files)
       }(utils.GetFullPath(storage.GetStorage().MountPath, path), files)
   }
   ```
   （`fs.go:146-149`）

2. `HandleObjsUpdateHook` (`internal/op/hook.go:27-31`) 遍历所有注册的 hook 并调用。

3. `search.Update` 通过 `init()` 注册为 hook（`build.go:270-272`）：
   ```go
   func init() {
       op.RegisterObjsUpdateHook(Update)
   }
   ```

4. `Update` 的触发条件（`build.go:202-218`）：
   - `instance != nil`（搜索器已初始化）
   - `instance.Config().AutoUpdate` 为 true
   - `setting.GetBool(conf.AutoUpdateIndex)` 为 true
   - `!Running()`（BuildIndex 不在运行）
   - `!isIgnorePath(parent)`（不在忽略路径中）
   - `progress.IsDone` 为 true（索引已构建完成）

**FACT**（OpenList 差异）：
- OpenList `Update` 签名多了 `ctx context.Context` 参数（`build.go:202`）
- OpenList `HandleObjsUpdateHook` 也多了 `ctx` 参数（`op/hook.go:29`）
- OpenList 额外有 `needHandleObjsUpdateHook()` 门控（`op/fs.go:858-864`）：检查 setting `HandleHookAfterWriting` 是否为 true
- OpenList 有 `conf.SkipHookKey` 和 `model.ListArgs.SkipHook` 可跳过 hook

---

## Q5. HandleObjsUpdateHook 等更新 Hook

**FACT**（AList `internal/op/hook.go:17-31`）：

```go
type ObjsUpdateHook = func(parent string, objs []model.Obj)

var objsUpdateHooks = make([]ObjsUpdateHook, 0)

func RegisterObjsUpdateHook(hook ObjsUpdateHook) {
    objsUpdateHooks = append(objsUpdateHooks, hook)
}

func HandleObjsUpdateHook(parent string, objs []model.Obj) {
    for _, hook := range objsUpdateHooks {
        hook(parent, objs)
    }
}
```

**FACT**：唯一注册的 hook 是 `search.Update`（`build.go:270-272`）。无其他 hook 注册调用。

**FACT**（OpenList 差异）：OpenList 的 `ObjsUpdateHook` 签名含 `ctx`（`op/hook.go:19`）：
```go
type ObjsUpdateHook = func(ctx context.Context, parent string, objs []model.Obj)
```

---

## Q6. Update 是目录级 diff 还是其它机制

**FACT**：**目录级 name diff**。AList `build.go:202-268`：

```go
func Update(parent string, objs []model.Obj) {
    // ...gating checks...
    nodes, err := instance.Get(ctx, parent)     // 获取索引中该 parent 的所有节点
    now := mapset.NewSet[string]()
    for i := range objs { now.Add(objs[i].GetName()) }
    old := mapset.NewSet[string]()
    for i := range nodes { old.Add(nodes[i].Name) }
    toDelete := old.Difference(now)              // 索引有但当前列表没有 → 删除
    toAdd := now.Difference(old)                 // 当前列表有但索引没有 → 新增
    // ...delete and add...
}
```

- `instance.Get(ctx, parent)` → `db.GetSearchNodesByParent(parent)` → `WHERE parent = ?`（精确匹配，`db/searchnode.go:48-55`）
- 比较的是 **Name 集合**，不是 ID、不是 path、不是 hash
- 是 **目录级**（单个 parent 目录下的 name diff），不是全局 diff

---

## Q7. 删除节点如何判定

**FACT**（AList `build.go:235-244`）：

```go
for i := range nodes {
    if toDelete.Contains(nodes[i].Name) && !op.HasStorage(path.Join(parent, nodes[i].Name)) {
        err = instance.Del(ctx, path.Join(parent, nodes[i].Name))
    }
}
```

删除条件：
1. `toDelete.Contains(nodes[i].Name)`：name 在索引中但不在当前列表中
2. `!op.HasStorage(path.Join(parent, nodes[i].Name))`：该路径**没有**挂载 storage

**FACT**：`op.HasStorage` 检查是否有 storage 挂载在该路径。对于**文件**（非挂载点），`HasStorage` 返回 false → `!HasStorage` 为 true → **删除执行**。

**FACT**：`instance.Del(ctx, path)` → `db.DeleteSearchNodesByParent(path)`（`db/searchnode.go:32-42`）：
```go
func DeleteSearchNodesByParent(path string) error {
    // 删除 parent LIKE 'path/%' OR parent = 'path' 的所有节点
    err := db.Where(whereInParent(path)).Delete(&model.SearchNode{}).Error
    // 再删除 parent = dir(path) AND name = base(path) 的节点
    return db.Where("parent = ? AND name = ?", dir, name).Delete(&model.SearchNode{}).Error
}
```
是 **prefix 删除** + 精确删除（删除该路径本身及其所有子节点）。

---

## Q8. rename 如何进入搜索索引

**FACT**（AList）：`op.Rename` (`internal/op/fs.go:407-439`) **不直接更新搜索索引**。它只更新缓存：
```go
case driver.RenameResult:
    newObj, err = s.Rename(ctx, srcObj, dstName)
    if err == nil {
        updateCacheObj(storage, srcDirPath, srcRawObj, model.WrapObjName(newObj))  // 更新缓存
    }
case driver.Rename:
    err = s.Rename(ctx, srcObj, dstName)
    if err == nil {
        ClearCache(storage, srcDirPath)  // 清除缓存
    }
```

索引更新发生在**下次 `list` 调用时**：`op.List` → `HandleObjsUpdateHook` → `Update` → name diff 发现旧 name 消失、新 name 出现 → delete + add。

**FACT**（OpenList 差异）：`op.Rename` (`internal/op/fs.go:450-508`) **直接触发 hook**（`fs.go:499-507`）：
```go
if ctx.Value(conf.SkipHookKey) != nil || !needHandleObjsUpdateHook() {
    return nil
}
dstDirPath := stdpath.Dir(srcPath)
if !srcObj.IsDir() {
    go objsUpdateHook(context.WithoutCancel(ctx), storage, dstDirPath, false)
} else {
    go objsUpdateHook(context.WithoutCancel(ctx), storage, stdpath.Join(dstDirPath, srcObj.GetName()), true)
}
```
OpenList rename 后**立即异步触发** `objsUpdateHook`（含递归处理目录），不需要等下次 `list`。

---

## Q9. move 如何进入搜索索引

**FACT**（AList）：`op.Move` (`internal/op/fs.go:364-405`) **不直接更新搜索索引**。与 rename 同理，只更新缓存（`delCacheObj` + `ClearCache`）。索引更新延迟到下次 `list`。

**FACT**（OpenList 差异）：`op.Move` (`internal/op/fs.go:~420-448`) **直接触发 hook**（`fs.go:439-446`），与 rename 同理。

---

## Q10. rename/move 在索引里是 rename 还是 delete + add

**FACT**：**delete + add**，不是 rename。

无论 AList（延迟触发）还是 OpenList（立即触发），`Update` 函数的机制都是 name diff：
- 旧 name 在索引中但不在当前列表 → `toDelete` → `instance.Del` 删除
- 新 name 在当前列表但不在索引中 → `toAdd` → `instance.Index` 新增

索引中**没有 rename 操作**。旧节点被删除，新节点被创建。如果 rename 前后 `Parent` 不变（同目录 rename），只有 `Name` 变了；如果 move 后 `Parent` 变了，旧 parent 目录下的节点被删，新 parent 目录下的节点被加。

**INFERENCE**：这意味着索引不跟踪文件身份的连续性。rename 前后的文件在索引中没有任何关联。

---

## Q11. checkpoint/resume 是否存在

**FACT**：**不存在**。

- `BuildIndex` 写入 `IndexProgress{ObjCount, IsDone, LastDoneTime, Error}` 到 settings（`build.go:86-90, 115-121`），但这是**进度指示器**，不是可恢复的 checkpoint。
- 如果 `BuildIndex` 被中断（`StopIndex` 或进程退出），`Quit` 机制（`build.go:42, 94-126`）只负责优雅停止当前 goroutine，不保存进度。
- 重新构建调用 `BuildIndex` handler → 先 `Clear` → 从头开始。
- `indexMQ` 是内存队列，进程重启丢失。

---

## Q12. staging 是否存在

**FACT**：**不存在**。

- `BatchIndex` 直接写入数据库：`db.CreateInBatches(nodes, 1000)`（`db/searchnode.go:28-30`）。
- `indexMQ` 是内存 batching buffer（攒满 1000 条或每 5 秒 flush），不是持久化 staging area。进程重启丢失。
- 没有 "staging table"、"pending queue"、"write-ahead log" 等机制。

---

## Q13. atomic reconcile 是否存在

**FACT**：**不存在**。

- `Update` 函数先 delete 后 add，**无事务**（`build.go:235-267`）：
  - 逐个调用 `instance.Del` 删除旧节点
  - 逐个调用 `instance.Index`（或 `BuildIndex` for dirs）添加新节点
  - 如果中途失败（`err != nil` → `return`），索引处于**不一致状态**：部分旧节点已删，新节点未加。
  - 无 rollback 机制。

- OpenList 的 `lockUpdate`（`update_lock.go:17-37`）提供 **per-parent 互斥锁**，序列化同一目录的并发更新，但**不提供原子性**。

---

## Q14. Provider/List 失败时是否可能造成索引错误删除

**FACT**：**可能**。

场景：`storage.List` 返回部分结果（某些文件因 provider 错误未返回，但 `storage.List` 未返回 error — 见 D02 W-D §3.5 静默吞没机制）。

此时 `Update` 收到的 `objs` 是不完整的：
- `now` 集合缺少部分文件 name
- `old` 集合包含索引中的全部 name
- `toDelete = old.Difference(now)` 包含被遗漏的文件
- 对于文件（非挂载点），`!op.HasStorage(path)` 为 true → **删除执行**

**结果**：provider 临时故障导致文件从索引中被错误删除。

**FACT**：`!op.HasStorage(path.Join(parent, nodes[i].Name))` 安全检查（`build.go:236`）只保护**挂载点**（virtual dir），不保护**存储内文件**。存储仍挂载时，`HasStorage` 对文件路径返回 false → `!HasStorage` 为 true → 删除不受阻。

---

## Q15. AList 与 OpenList 在这套机制上的实际差异

**FACT**：差异清单：

| 维度 | AList | OpenList | 证据 |
|------|-------|----------|------|
| Update 签名 | `Update(parent string, objs []model.Obj)` | `Update(ctx, parent, objs)` | AList `build.go:202` / OpenList `build.go:202` |
| 并发控制 | 无 | `lockUpdate(parent)` per-parent 互斥锁 | OpenList `build.go:228-229` / `update_lock.go:17-37` |
| Meilisearch 异步 | 无 | `EnqueueUpdate` 异步队列 | OpenList `build.go:219-226` |
| 新增批处理 | 逐个 `Index`（文件）/ `BuildIndex`（目录） | 批量 `BatchIndex` | AList `build.go:245-266` / OpenList `build.go:257-275` |
| 新目录处理 | 递归 `BuildIndex` | 仅 `BatchIndex`（不递归） | AList `build.go:256-264` / OpenList `build.go:269-275` |
| rename/move 触发 | 仅更新缓存，延迟到下次 `list` | 直接异步触发 `objsUpdateHook` | AList `fs.go:407-439` / OpenList `fs.go:450-508` |
| hook 门控 | `!args.NoUpdateIndex` | `conf.SkipHookKey` + `needHandleObjsUpdateHook()` setting | AList `fs.go:146` / OpenList `fs.go:72,858-864` |
| 递归 hook | 无 | `recursivelyObjsUpdateHook` 递归处理子目录 | OpenList `fs.go:836-856` |
| UpdateIndex handler | 不写 progress | 写 progress（含 error） | AList `index.go:42-72` / OpenList `index.go:43-93` |
| context 传递 | `context.WithValue(ctx, "user", admin)` | `context.WithValue(ctx, conf.UserKey, admin)` | AList `build.go:182` / OpenList `build.go:182` |

---

## Q16. Search index 是否具备成为 Canonical Inventory 的条件

**FACT**：**不具备**。理由：

1. **字段不足**：SearchNode 仅 `Parent/Name/IsDir/Size`，无 ID、Hash、Modified、Created、Provider。不足以作为资源规范清单。
2. **无 atomic reconcile**：Update 先 delete 后 add 无事务，中途失败导致不一致（Q13）。
3. **无 checkpoint/resume**：重建从头开始，中断后无法恢复（Q11）。
4. **无 staging**：直接写 DB，无缓冲层（Q12）。
5. **Provider 故障可致错误删除**：partial list → name diff → 缺失文件被删（Q14）。
6. **无身份连续性**：rename = delete + add，不跟踪文件身份（Q10）。
7. **name-based diff 脆弱**：同目录同名不同文件无法区分；rename 后旧记录消失。
8. **非 source of truth**：索引是搜索辅助，数据可被 `Clear` 全部清除后重建。不保证与 provider 状态一致。

---

## VERIFIED_FACTS

> 全部为源码直接证实（FACT）。

1. `SearchNode` 仅含 `Parent/Name/IsDir/Size`（`internal/model/search.go:23-28`，两者相同）。
2. `BuildIndex` 入口：`handles.BuildIndex` 先 `Clear` 再从 `/` 构建（`server/handles/index.go:21-40`）。
3. `BuildIndex` 使用 `fs.WalkFS` 遍历 + `indexMQ`（内存 MQ）批量写入（`internal/search/build.go:33-188`）。
4. `BatchIndex` → `db.CreateInBatches(nodes, 1000)`（`internal/db/searchnode.go:28-30`）。
5. `Update` 通过 `init()` 注册为 `ObjsUpdateHook`（`build.go:270-272`）。
6. `HandleObjsUpdateHook` 在 `op.List` 成功后异步调用（`internal/op/fs.go:146-149`）。
7. `Update` 是目录级 name diff：`toDelete = old.Difference(now)`, `toAdd = now.Difference(old)`（`build.go:224-234`）。
8. 删除前检查 `!op.HasStorage(path)`，仅保护挂载点，不保护存储内文件（`build.go:236`）。
9. `DeleteSearchNodesByParent` 是 prefix 删除 + 精确删除（`db/searchnode.go:32-42`）。
10. AList `Rename`/`Move` 不直接更新索引，只更新缓存（`internal/op/fs.go:364-439`）。
11. OpenList `Rename`/`Move` 直接异步触发 `objsUpdateHook`（`internal/op/fs.go:439-446, 499-507`）。
12. 无 checkpoint/resume：`IndexProgress` 是进度指示器，非可恢复 checkpoint（`build.go:86-90, 115-121`）。
13. 无 staging：`indexMQ` 是内存 buffer，`BatchIndex` 直接写 DB（`build.go:47, 73-84`）。
14. 无 atomic reconcile：Update 先 delete 后 add 无事务，中途失败无 rollback（`build.go:235-267`）。
15. OpenList 有 `lockUpdate` per-parent 互斥锁（`internal/search/update_lock.go:17-37`）。
16. OpenList `Update` 支持 Meilisearch 异步 `EnqueueUpdate`（`build.go:219-226`）。
17. OpenList `UpdateIndex` handler 写 progress（`index.go:75-90`），AList 不写（`index.go:56-70`）。
18. `GetSearchNodesByParent` 精确匹配 `WHERE parent = ?`（`db/searchnode.go:48-55`）。
19. `whereInParent` 用于 search 和 delete：`parent LIKE 'path/%' OR parent = 'path'`（`db/searchnode.go:15-22`）。
20. `IndexProgress` 存储在 settings 表（`search/util.go:17-39`），非独立表。

---

## UNVERIFIED

1. **(INFERENCE)** OpenList `lockUpdate` 是否完全消除并发 race — 源码显示 per-parent 锁，但跨目录的并发更新仍可能产生不一致。
2. **(INFERENCE)** Meilisearch `EnqueueUpdate` 的具体实现 — 未读 `internal/search/meilisearch/` 源码。
3. **(UNVERIFIED)** OpenList `recursivelyObjsUpdateHook` 的 rate limiter 默认值 — 由 setting `HandleHookRateLimit` 控制，未查默认值。