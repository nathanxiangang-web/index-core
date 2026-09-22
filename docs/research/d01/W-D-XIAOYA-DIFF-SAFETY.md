# Worker D — Diff / 安全删除 / 同步机制调查

## STATUS
PASS

## SUMMARY
小雅元数据同步器存在两个独立实现：Python 版（xiaoya_db/solid.py，763 行）通过 HTTP 爬取 HTML 目录列表 + SQLite 本地数据库做 Diff，使用 asyncio 并发，purge 前有"扫描数量差距 < 10"的完整性门控（Complete Gate），但删除是硬删除（os.remove）无回收站；Go 版（xiaoya_emd_go/main.go，2464 行）通过预生成的 `.scan.list.gz` 索引文件 + 内存 Map 做 Diff，使用 goroutine + channel 并发，删除时移动到 `recycle_bin/`（软删除/Removal Guard），下载时用 `.tmp` + rename 做原子写入（Staging/Atomic Commit），但**没有扫描完整性门控**——只要本地有、服务器没有就移入回收站，不检查扫描是否完整。两者都没有 checkpoint/resume（断点续传）。Go 版 GPL v3，Python 版无 LICENSE（默认 All Rights Reserved）。

## STATE_FLOW

```
Python (solid.py):
  fetch_pool_from_url() ──> pick_a_pool_member() ──> url
       │                                              │
       ▼                                              ▼
  .scan.list.gz ──> total_amount              HTTP 爬取 HTML 目录
       │                                    (BeautifulSoup 解析 <a>)
       ▼                                              │
  os.walk(media) ──> .localfiles.db                  ▼
       │                                    parse() -> (url, filename, ts, size)
       ▼                                              │
  bulk_crawl_and_write() ──> .tempfiles.db           ▼
       │                                    need_download()? 
       │                                      exists? size==? ts<=?
       ▼                                              │
  compare_databases()                              ▼
    gap = |len(temp) - total_amount|          download() (asyncio.Semaphore(100))
    gap < 10 AND total_amount > 0?                │
       │ YES                          │ NO         ▼
       ▼                              ▼        aiofiles.write()
  diff = local - temp            return []       (直接覆盖，无 .tmp)
       │                              │
       ▼                              ▼
  os.remove(media + file)     SKIP PURGE
  remove_empty_folders()
  rename(tempdb -> localdb)

Go (main.go):
  pickBestServers() ──> 1主2备 (并行探测 Last-Modified)
       │
       ▼
  checkAndUpdateScanList() ──> .scan.list.gz (30min 内不重下)
       │
       ▼
  generateServerMap() ──> FileInfoMap{path: timestamp}
       │
       ▼
  scanLocalFilesToMap() ──> FileInfoMap{path: timestamp}  (filepath.Walk)
       │
       ▼
  compareAndPrepareSync(local, server)
    toUpdate: !exists OR serverTS - localTS > 600 (10min)
    toDelete: local 有, server 无
       │
       ▼
  syncFilesCore():
    Phase 1: deleteLocalFile() ──> os.Rename -> recycle_bin/  (软删除)
    Phase 2: downloadFile() ──> .tmp + os.Rename  (原子写入)
       │
       ▼
  config.ScanListTime = serverTime  (saveConfig)
```

## DIFF_RULES

| 判定类型 | 条件 | 动作 | 安全检查 |
|----------|------|------|----------|
| ADD (Python) | 本地文件不存在 (`!os.path.exists`) | `download()` 直接写入 | 无 staging，直接覆盖 |
| UPDATE (Python) | 大小不同 OR 远端时间戳更新 (`filesize != current OR timestamp > current_mtime`) | `download()` 直接写入 | NFO 有 `--nfo` 开关控制 |
| DELETE (Python) | 本地有, 远端无 (`local_filenames - temp_filenames`) | `os.remove()` 硬删除 | **gap < 10 AND total_amount > 0** 才执行 |
| ADD (Go) | 本地不存在 (`!exists`) | `downloadFile()` .tmp+rename | 原子写入 |
| UPDATE (Go) | 服务器比本地新超 10 分钟 (`serverTS - localTS > 600`) | `downloadFile()` .tmp+rename | 原子写入 |
| DELETE (Go) | 本地有, 服务器无 (`!exists in serverInfo.Files`) | `deleteLocalFile()` 移到 recycle_bin | **无数量门控**，仅靠回收站兜底 |

## FAILURE_MATRIX

| 故障场景 | Python 处理 | Go 处理 | 是否安全 |
|----------|------------|---------|----------|
| 单个文件下载失败 | `except Exception` 记日志，跳过，继续下一个 | 尝试所有 3 个服务器，全失败则加入 failedFiles，继续 | Python: 文件缺失但不影响 purge 判定；Go: 同 |
| 服务器不可用（HTTP 错误） | `pick_a_pool_member` 随机打乱逐个试，返回第一个含"每日更新"的 | `pickBestServers` 并行探测，选最新时间戳+最快响应；无可用则等 5 分钟重试 | 两者都有 fallback |
| 服务器全部不可用 | `pick_a_pool_member` 返回 None → `sys.exit(1)` | 等待 5 分钟后 continue 重试 | Python 直接退出；Go 持续重试 |
| 扫描不完整（远端列表残缺） | **gap >= 10 时跳过 purge**（Complete Gate） | **无保护**，直接对比后移入回收站 | Python 安全；Go 有风险（但回收站可恢复） |
| 本地 DB 不完整 | `abs(total_amount - local_amount) > 1000` 时重新生成 localdb | 无本地 DB，每次全量扫描内存 Map | Python 有自愈；Go 每次全扫 |
| 下载中断/进程被杀 | 无 checkpoint，下次从头开始 | 无 checkpoint，但 `.tmp` 残留会被下次启动时清理 | 两者都无续传，Go 有 .tmp 清理 |
| 404 文件不存在 | 不单独处理（作为下载失败） | 立即返回错误，不尝试其他服务器 | Go 更合理 |
| 同名文件冲突 | 无处理 | recycle_bin 中同名加时间戳后缀 `name_20060102_150405.ext` | Go 安全 |

## REMOVAL_SAFETY

| 安全机制 | Python (xiaoya_db) | Go (xiaoya_emd_go) | 对应蓝图概念 |
|----------|--------------------|--------------------|--------------|
| 扫描完整性门控 | **有**: `gap = abs(len(temp) - total_amount); gap < 10 AND total_amount > 0` (solid.py:465) | **无**: 直接对比即删除 | **Complete Gate** |
| 回收站/软删除 | **无**: `os.remove()` 硬删除 (solid.py:488) | **有**: `os.Rename -> recycle_bin/` (main.go:1029) | **Removal Guard** |
| 数量异常保护 | **有**: 本地 DB 与远端差距 > 1000 时重建 (solid.py:724) | **无** | **Unexpected Shrink Guard** (部分) |
| 服务器错误保护 | **部分**: pick_a_pool_member 选可用服务器，全不可用则退出 | **有**: pickBestServers 选最新+最快，无可用等 5 分钟 | **Provider Error Guard** |
| 原子写入 | **部分**: tempdb → rename localdb (solid.py:755) | **有**: `.tmp` + `os.Rename` (main.go:970,980) | **Staging/Atomic Commit** |
| 删除前确认 | **无** | **无** | (蓝图未要求) |
| 删除数量阈值 | **无** (仅 gap 检查) | **无** | (蓝图未要求) |
| 回收站清理 | N/A | 手动 API `/api/recycle-bin/clear` (main.go:2153) | 人工确认 |

## CONCURRENCY

**Python (solid.py)**:
- 模型：`asyncio` + `aiohttp.ClientSession`
- 并发控制：`asyncio.Semaphore(args.count)`，默认 100
- 下载批控：`download_files` 中 `len(download_tasks) > 100` 时 `asyncio.gather` 等待
- 递归爬取：`bulk_crawl_and_write` 为每个目录创建 task，`task.add_done_callback(download_tasks.discard)`
- DB 写入：`aiosqlite` 异步，`INSERT OR REPLACE`
- 锁：无显式锁（asyncio 单线程）
- 连接：`TCPConnector(ssl=False, limit=0, ttl_dns_cache=600)`，timeout 36000s

**Go (main.go)**:
- 模型：`goroutine` + `sync.WaitGroup`
- 并发控制：`semaphore := make(chan struct{}, maxConcurrency)`，配置可调
- 服务器探测：并行请求所有服务器，`sync.Mutex` 保护结果
- 本地扫描：并行扫描每个路径，`sync.Mutex` 保护 Map
- 配置读写：`sync.RWMutex` (configMu)
- 同步状态：`sync.Mutex` (syncStateMu)
- 日志：`sync.Mutex` (logsMu)
- 限速：`golang.org/x/time/rate` 令牌桶（带宽限制）
- 上下文取消：`context.WithCancel` 支持手动终止

## CHECKPOINT_RESUME

**Python**: **无 checkpoint/resume 机制**。每次运行：
1. 重新爬取远端全量目录列表
2. 重新生成或复用本地 DB（但 purge 后会删除 localdb）
3. 无断点续传：如果中途崩溃，已下载的文件保留（need_download 会跳过），但 purge 不会执行
4. `.scan.list.gz` 每次重新下载到临时位置，不持久化

**Go**: **无 checkpoint/resume 机制**，但有部分状态持久化：
1. `config.ScanListTime` 持久化到 config.json，用于跳过检测（30 分钟内不重下 .scan.list.gz）
2. `.scan.list.gz` 下载后保留到本地，下次可复用
3. 无断点续传：如果中途崩溃，已下载的文件保留（时间戳对比会跳过），但同步状态丢失
4. `.tmp` 文件在下次启动时被清理（main.go:1140-1155）
5. `config.ServerPathCounts` 和 `config.LocalPathCounts` 持久化，但仅用于 UI 显示

## STAGING_ATOMIC

**Python (solid.py)**:
- **数据库层面有原子提交**：使用 `.tempfiles.db` 作为临时数据库，purge 完成后 `os.rename(tempdb, localdb)` (solid.py:755)
- **文件层面无原子写入**：`aiofiles.open(file_path, "wb")` 直接写入目标文件，下载中途崩溃会留下残缺文件
- **无暂存区**：文件直接写入最终位置

**Go (main.go)**:
- **文件层面有原子写入**：`os.Create(localPath + ".tmp")` → `io.Copy` → `os.Rename(localPath+".tmp", localPath)` (main.go:970-980)
- **.tmp 清理**：每次同步开始时 `filepath.Walk` 清理所有 `.tmp` 文件 (main.go:1140-1155)
- **时间戳设置**：rename 后 `os.Chtimes(localPath, modTime, modTime)` 恢复服务器时间戳 (main.go:985)
- **无暂存区**：无全局 staging dir，每个文件独立 .tmp

## GAP_ANALYSIS

对照 Kernel 数据安全要求，两个实现的缺口：

| 蓝图概念 | Python | Go | 缺口 |
|----------|--------|-----|------|
| **Complete Gate** | 有（gap<10） | **无** | Go 版若 .scan.list.gz 不完整或解析错误，会把大量本地文件移入回收站。Python 的阈值 10 也偏小，大库场景可能误判 |
| **Removal Guard** | **无**（硬删除） | 有（recycle_bin） | Python 版一旦通过 gap 检查就硬删除，无法恢复。Go 版回收站无自动清理策略，需手动清空 |
| **Unexpected Shrink Guard** | 部分（local_amount 差距>1000 重建） | **无** | 两者都没有"删除数量超过阈值时暂停"的保护。Go 版若服务器返回空列表，会把全部本地文件移入回收站 |
| **Provider Error Guard** | 部分（全不可用退出） | 有（等 5 分钟重试） | Python 在服务器全不可用时直接退出，不删除；Go 在核心同步失败时不更新 ScanListTime，但已删除的文件不会回滚 |
| **Checkpoint/Resume** | **无** | **无** | 两者都无断点续传，大库同步中途崩溃需从头开始 |
| **事务性/回滚** | **无** | **无** | 两者都无事务性保证：删除和下载是独立步骤，无法回滚已完成的删除 |
| **下载校验** | **无**（不校验大小/哈希） | **无**（不校验大小/哈希） | 两者下载后都不校验文件完整性（无 checksum、无 size 验证） |
| **并发安全** | asyncio 单线程安全 | goroutine + Mutex | Go 的 Map 并发读写有锁保护，但回收站移动操作无锁（依赖 os.Rename 原子性） |

**关键缺口**：
1. Go 版缺少 Complete Gate — 服务器数据异常时会大规模误删（虽有回收站兜底）
2. Python 版缺少 Removal Guard — 硬删除不可恢复
3. 两者都缺少下载后校验 — 网络错误可能产生损坏文件
4. 两者都缺少事务性 — 删除和下载不是原子的

## VERIFIED_FACTS

1. **Python purge safety 在 solid.py:465**: `if gap < 10 and total_amount > 0:` 执行 purge，否则 `return []` 跳过
2. **Python 硬删除在 solid.py:488**: `os.remove(media + file)` 无回收站
3. **Python 本地 DB 完整性检查在 solid.py:721-729**: `abs(total_amount - local_amount) > 1000` 时重建 localdb
4. **Python 原子 DB 提交在 solid.py:755**: `os.rename(tempdb, localdb)`
5. **Python 并发模型在 solid.py:700**: `semaphore = asyncio.Semaphore(args.count)`，默认 100
6. **Python 服务器选择在 solid.py:140-159**: `pick_a_pool_member` 随机打乱逐个尝试
7. **Python 服务器池获取在 solid.py:66-126**: `fetch_pool_from_url` 远程获取，失败用 16 个硬编码 fallback
8. **Go recycle_bin 在 main.go:1001-1036**: `deleteLocalFile` 移动到 `recycle_bin/`，同名加时间戳后缀
9. **Go 原子写入在 main.go:970-980**: `.tmp` + `os.Rename`
10. **Go .tmp 清理在 main.go:1140-1155**: 每次同步开始时清理
11. **Go 服务器选择在 main.go:604-721**: `pickBestServers` 并行探测，选最新时间戳+最快响应前 3 个
12. **Go Diff 在 main.go:857-925**: `compareAndPrepareSync`，update 条件 `serverTS-localTS > 600`，delete 条件 `!exists in serverInfo.Files`
13. **Go 并发模型在 main.go:1410**: `semaphore := make(chan struct{}, maxConcurrency)`
14. **Go 无 gap 检查**: `compareAndPrepareSync` 中无任何数量阈值检查（已全文搜索确认）
15. **Go ScanListTime 持久化在 main.go:1296**: `config.ScanListTime = serverTime` + `saveConfig()`
16. **Go 30 分钟跳过在 main.go:1551**: `serverTime.Sub(compareTime) <= 30*time.Minute` 时跳过同步
17. **Go LICENSE 是 GPL v3**: /tmp/worker-d/xiaoya_emd_go/LICENSE 第 1 行 `GNU GENERAL PUBLIC LICENSE`
18. **Python 无 LICENSE 文件**: `ls /tmp/worker-d/xiaoya_db/` 确认无 LICENSE/COPYING

## EVIDENCE

| 编号 | 文件 | 行号 | 函数/语句 | 说明 |
|------|------|------|-----------|------|
| E01 | xiaoya_db/solid.py | 465 | `if gap < 10 and total_amount > 0:` | Python Complete Gate |
| E02 | xiaoya_db/solid.py | 476-481 | `logger.error(...); return []` | Python gap 过大跳过 purge |
| E03 | xiaoya_db/solid.py | 488 | `os.remove(media + file)` | Python 硬删除 |
| E04 | xiaoya_db/solid.py | 721-729 | `abs(total_amount - local_amount) > 1000` | Python 本地 DB 完整性检查 |
| E05 | xiaoya_db/solid.py | 755 | `os.rename(tempdb, localdb)` | Python 原子 DB 提交 |
| E06 | xiaoya_db/solid.py | 700 | `asyncio.Semaphore(args.count)` | Python 并发控制 |
| E07 | xiaoya_db/solid.py | 140-159 | `pick_a_pool_member` | Python 服务器选择 |
| E08 | xiaoya_db/solid.py | 66-126 | `fetch_pool_from_url` | Python 服务器池获取 |
| E09 | xiaoya_db/solid.py | 265-287 | `need_download` | Python add/update 判定 |
| E10 | xiaoya_db/solid.py | 290-313 | `download` | Python 下载（无 .tmp） |
| E11 | xiaoya_emd_go/main.go | 1001-1036 | `deleteLocalFile` | Go 回收站机制 |
| E12 | xiaoya_emd_go/main.go | 1020-1026 | `finalRecyclePath = ..._20060102_150405.ext` | Go 同名冲突处理 |
| E13 | xiaoya_emd_go/main.go | 970-980 | `.tmp` + `os.Rename` | Go 原子写入 |
| E14 | xiaoya_emd_go/main.go | 1140-1155 | `strings.HasSuffix(path, ".tmp")` | Go .tmp 清理 |
| E15 | xiaoya_emd_go/main.go | 604-721 | `pickBestServers` | Go 服务器选择 |
| E16 | xiaoya_emd_go/main.go | 857-925 | `compareAndPrepareSync` | Go Diff 逻辑 |
| E17 | xiaoya_emd_go/main.go | 878 | `serverTS-localTS > 600` | Go update 阈值 10 分钟 |
| E18 | xiaoya_emd_go/main.go | 1410 | `make(chan struct{}, maxConcurrency)` | Go 并发控制 |
| E19 | xiaoya_emd_go/main.go | 1551 | `<= 30*time.Minute` | Go 跳过同步阈值 |
| E20 | xiaoya_emd_go/main.go | 957-960 | `StatusNotFound` 提前退出 | Go 404 处理 |
| E21 | xiaoya_emd_go/LICENSE | 1 | `GNU GENERAL PUBLIC LICENSE` | Go GPL v3 |

## UNVERIFIED

1. Python 仓库 (xiaoya_db) 的许可证未明确声明——无 LICENSE 文件，无 setup.py license 字段，无 pyproject.toml。GitHub 仓库页面可能显示 "No license"，意味着默认 All Rights Reserved。但原仓库可能是 Rik-F5/xiaoya_db 的 fork，原仓库可能有许可证（未验证）。
2. Go 版的 `maxConcurrency` 默认值未找到（从 config.json 读取，config.json 中有值但未在代码中看到默认值）。
3. Go 版是否有内存限制导致扫描不完整的保护——`scanLocalFilesToMap` 中有内存检查 (main.go:816-823)，但内存超限时返回 error，syncFiles 会 continue 跳过本轮，不会删除。这算是一种隐式的 Complete Gate（扫描失败就不删除）。
4. Python 版的 `.scan.list.gz` 是否包含全量文件列表——从代码看 `current_amount` 仅用于获取总数，不用于 Diff（Diff 靠爬取 HTML 目录）。

## RISKS

1. **Go 版大规模误删风险**：如果 `.scan.list.gz` 下载不完整或解析错误，`generateServerMap` 返回的 Map 会缺少文件，`compareAndPrepareSync` 会把这些文件全部加入 `toDelete`，移入回收站。虽然回收站可恢复，但大量文件移动会消耗 IO 且影响可用性。
2. **Python 版不可恢复风险**：gap < 10 时执行硬删除，如果恰好有少量文件因网络问题未爬取到，这些文件会被永久删除。
3. **Go 版回收站无自动清理**：回收站会无限增长，需手动清空。如果用户不清空，磁盘可能被占满。
4. **两者都无下载校验**：网络错误可能产生截断/损坏的文件，下次同步时时间戳可能被设为服务器时间（Go 的 `os.Chtimes`），导致损坏文件不被重新下载。
5. **Python 版无 .tmp 保护**：下载中途崩溃会留下残缺文件，`need_download` 可能因大小/时间戳匹配而跳过重新下载。

## LICENSE

| 仓库 | LICENSE 文件 | 许可证 | 能否复用代码 |
|------|-------------|--------|-------------|
| xiaoyaDev/xiaoya_db (Python) | **无** | 未声明（默认 All Rights Reserved） | **不能**复用代码，只能借思想 |
| xiaoyaDev/xiaoya_emd_go (Go) | 有 (GPL v3) | GNU General Public License v3 | **不能**直接复制到非 GPL 项目。复制代码会触发 GPL 传染性，要求整个项目以 GPL 开源 |

**结论**：两个仓库的代码都不能直接复制到 Kernel 产品代码中。Python 无许可证意味着保留所有权利；Go 是 GPL v3，传染性太强。只能借鉴思想/算法，独立实现。

## COPY_CANDIDATE

**无**。两个仓库的许可证都不允许直接复制代码：
- Python 版无 LICENSE（All Rights Reserved）
- Go 版 GPL v3（传染性）

## REWRITE_CANDIDATE

以下逻辑值得参考并独立重写（不复制代码，只借思想）：

1. **Go 的 `.tmp` + `os.Rename` 原子写入模式** (main.go:970-980)
   - 思想：先写入临时文件，完成后原子 rename
   - 重写：用 Python 的 `tempfile.NamedTemporaryFile` + `os.replace`

2. **Go 的 `recycle_bin` 回收站模式** (main.go:1001-1036)
   - 思想：删除时移动到回收站而非硬删除，同名加时间戳
   - 重写：用 Python 的 `shutil.move` + 时间戳后缀

3. **Go 的 `pickBestServers` 服务器选择** (main.go:604-721)
   - 思想：并行探测所有服务器，选时间戳最新+响应最快的前 N 个
   - 重写：用 `asyncio.gather` 并行探测，按 (时间戳, 响应时间) 排序

4. **Python 的 `compare_databases` gap 检查** (solid.py:465)
   - 思想：远端扫描数量与声明总数差距小于阈值才执行删除
   - 重写：在删除前检查 `abs(len(remote) - len(declared_total)) < threshold`

5. **Go 的 30 分钟跳过检测** (main.go:1551)
   - 思想：服务器数据包时间与本地上次同步时间差小于阈值则跳过
   - 重写：持久化 `last_sync_time`，比较服务器 `Last-Modified` 头

6. **Go 的 `.tmp` 残留清理** (main.go:1140-1155)
   - 思想：每次同步开始时扫描清理 `.tmp` 文件
   - 重写：启动时 `glob("**/*.tmp")` 清理

## IDEA_ONLY

以下思想只借鉴不重写（太简单或太特定）：

1. **Python 的 `fetch_pool_from_url`**：从远程 URL 动态获取服务器列表，失败用硬编码 fallback。思想：服务器池可动态更新。
2. **Python 的 `need_download` 三条件**：不存在 / 大小不同 / 时间戳更新。思想：基于文件属性的最小下载判定。
3. **Go 的 `serverTS - localTS > 600`**：10 分钟误差容忍。思想：避免时钟漂移导致的不必要更新。
4. **Go 的内存限制检查** (main.go:816-823)：扫描时检查内存使用，超限中止。思想：大库扫描的资源保护。
5. **Go 的 `pathCountChanges` 增量计数**：同步后增量更新路径文件计数，避免全量重扫。思想：增量状态维护。

## IGNORE

以下部分不适合复用或参考：

1. **Python 的 HTML 目录爬取** (solid.py:209-262, `parse` 函数)：用 BeautifulSoup 解析 nginx autoindex HTML。Kernel 应使用结构化 API 或索引文件，不应爬取 HTML。
2. **Python 的 `bulk_crawl_and_write` 递归爬取** (solid.py:431-450)：递归 HTTP 爬取目录树。太特定于小雅的 nginx 目录结构。
3. **Go 的 DOH/DOT DNS 解析** (main.go:124-285)：自定义 DNS 解析器。与 Diff/安全删除无关。
4. **Go 的 Web UI** (main.go:1645+, static/)：HTTP API 服务器和前端。与核心同步逻辑无关。
5. **Go 的 `cleanFileName`** (main.go:595-601)：文件名非法字符替换。太简单且特定。
6. **两者的 Docker 配置**：与同步逻辑无关。

## UNRESOLVED

1. Python 仓库的原作者（Rik-F5/xiaoya_db）是否曾有 LICENSE？xiaoyaDev/xiaoya_db 可能是 fork，原仓库的许可证状态未验证。
2. Go 版的 `config.json` 中 `maxConcurrency` 的默认值是什么？（代码中从配置读取，未找到硬编码默认值）
3. Go 版的 `checkAndUpdateScanList` 下载 `.scan.list.gz` 时是否校验完整性？（代码中只读 `Last-Modified` 头，不校验内容）
4. Python 版的 `.scan.list.gz` 格式与 Go 版是否完全相同？（Python 用 `current_amount` 解析，Go 用 `generateServerMap` 解析，格式看起来一致但未交叉验证）
5. Go 版的回收站是否有最大容量限制？（代码中无，但可能有外部 cron 清理）
6. 两者是否处理了符号链接？（Python 的 `os.walk` 默认不跟随符号链接；Go 的 `filepath.Walk` 也不跟随）
7. Go 版的 `syncFilesCore` 中删除先于下载执行——如果删除成功但下载失败，本地会缺少文件且本轮不会重试。这是否是预期行为？