# Worker C — AList 搜索导入链路调查

## STATUS
PASS

## SUMMARY
xiaoya-alist-search 是一个 Bash+内嵌 Python 的单脚本工具（Apache-2.0），它通过 docker-compose 启动一个全新的 `xhofe/alist:latest` 容器，用 `docker cp` 从原小雅 AList 容器的 `/index/` 目录拷贝出全部 `*.txt` 索引文件，然后直接用 Python sqlite3 模块写入新 AList 的 `data.db`：先向 `x_storages` 表插入 19 条挂载配置（WebDav 或 AList V3 驱动，指向原小雅 AList 的对应分类目录），再把 TXT 逐行解析为 `(parent, name, is_dir=1, size=0)` 批量插入 `x_search_nodes` 表，最后把 `x_setting_items.search_index` 设为 `database_non_full_text`。搜索时 AList 的 `db_non_full_text` searcher 对 sqlite3 一律走 `name LIKE %keyword%` 的子串匹配（非全文），结果返回 `(parent, name)` 供前端拼接路径访问。点击资源后，AList 通过 `fs.Link` 向上游驱动（WebDav/AList V3）取直链，再根据 `webdav_policy` 决定 `302_redirect`（直接重定向到上游 URL，用于 AList V3 驱动）或 `native_proxy`（AList 本地流式代理流量，用于 WebDav 驱动，因 WebDav 直链含认证不能外泄）。更新机制是全量重建：先 `DELETE FROM x_search_nodes` 清空全表，再批量插入，再 `DELETE ... WHERE rowid NOT IN (MIN(rowid) GROUP BY parent, name)` 去重——更新期间搜索会短暂返回空结果，存在一致性风险。

## CALL_CHAIN
```
原小雅 AList 容器 (/index/*.txt)
        │
        │  docker cp ${DOCKER_NAME}:/index/. → $INSTALL_PATH/alist-temp/
        ▼
xiaoya-alist-search.sh (Bash)
        │
        │  1. docker-compose up xhofe/alist:latest (新 AList 容器)
        │  2. docker stop 新容器
        │  3. cp data.db → data.db.bak
        │  4. 内嵌 Python 操作 SQLite (data.db)
        ▼
Python sqlite3 模块
        │
        ├─► UPDATE x_setting_items SET value='database_non_full_text' WHERE key='search_index'
        ├─► INSERT INTO x_storages (19 条挂载: /体育 /动漫 /电影 ... → 原小雅 AList)
        ├─► CREATE TABLE x_search_nodes (parent, name, is_dir, size)
        ├─► DELETE FROM x_search_nodes              (清空)
        ├─► 逐行解析 TXT: line.split('#')[0].replace('./','') → rfind('/') → (parent, name)
        ├─► INSERT INTO x_search_nodes 批量 1000 条/批
        ├─► DELETE 去重 (保留 MIN(rowid) GROUP BY parent, name)
        └─► CREATE INDEX idx_x_search_nodes_parent ON x_search_nodes (parent, name)
        │
        ▼
docker start 新容器 (AList 启动, 读取 data.db)
        │
        ▼
用户搜索 (GET /api/fs/search?keywords=xxx)
        │
        ▼
AList server/handles/search.go → search.Search()
        │
        ▼
internal/search/db_non_full_text/search.go → db.SearchNode(req, useFullText=false)
        │
        ▼
internal/db/searchnode.go: SearchNode()
        │  sqlite3 总是走 LIKE 分支:
        │  SELECT * FROM x_search_nodes
        │  WHERE (parent LIKE '/parent/%' OR parent = '/parent')
        │    AND name LIKE '%keyword1%' AND name LIKE '%keyword2%'
        │  ORDER BY name ASC LIMIT ? OFFSET ?
        ▼
返回 []SearchNode{Parent, Name, IsDir, Size}
        │
        ▼
前端拼接路径 → GET /d/{parent}/{name} 或 /p/{parent}/{name}
        │
        ▼
AList server/handles/down.go: Down()
        │
        ├─ fs.GetStorage(rawPath) → 找到挂载 (如 /电影 → AList V3 驱动)
        ├─ fs.Link(rawPath) → 调用上游驱动 Link()
        │     ├─ AList V3: POST 上游 /api/fs/get → 返回 RawURL
        │     └─ WebDav:   PROPFIND 上游 → 返回带认证的 URL
        ▼
根据 webdav_policy:
        ├─ 302_redirect: c.Redirect(302, link.URL)  → 客户端直连上游
        └─ native_proxy: common.Proxy(w, r, link)    → AList 流式转发
```

## KEY_SQL

### 建表（xiaoya-alist-search.sh:233-240，由 Python 脚本执行）
```sql
CREATE TABLE IF NOT EXISTS x_search_nodes (
    parent TEXT,
    name TEXT,
    is_dir NUMERIC,
    size INTEGER
)
```

### 建索引（xiaoya-alist-search.sh:247）
```sql
CREATE INDEX IF NOT EXISTS idx_x_search_nodes_parent ON x_search_nodes (parent, name)
```

### 清空（全量重建第一步，xiaoya-alist-search.sh:224）
```sql
DELETE FROM x_search_nodes
```

### 批量插入（xiaoya-alist-search.sh:255-258）
```sql
INSERT INTO x_search_nodes (parent, name, is_dir, size) VALUES (?, ?, ?, ?)
-- executemany, 1000 条/批
```

### 去重（xiaoya-alist-search.sh:307-314）
```sql
DELETE FROM x_search_nodes
WHERE rowid NOT IN (
    SELECT MIN(rowid) FROM x_search_nodes GROUP BY parent, name
)
```

### 设置搜索引擎（xiaoya-alist-search.sh:323-327）
```sql
UPDATE x_setting_items SET value = 'database_non_full_text' WHERE key = 'search_index'
```

### 挂载存储插入（xiaoya-alist-search.sh:207-213，19 条）
```sql
INSERT INTO x_storages (id, mount_path, [order], driver, cache_expiration, status,
    addition, remark, modified, disabled, enable_sign, order_by, order_direction,
    extract_folder, web_proxy, webdav_policy, proxy_range, down_proxy_url)
VALUES (1, '/体育', 1, 'WebDav', 30, 'work', "{...json...}", '',
    '2024-01-01 00:00:00', 0, 0, 'name', 'asc', 'front', 0, 'native_proxy', 0, '')
-- webdav_policy = 'native_proxy' (WebDav) 或 '302_redirect' (AList V3)
```

### 序列插入（xiaoya-alist-search.sh:131）
```sql
INSERT OR REPLACE INTO sqlite_sequence (name, seq) VALUES ('x_storages', 19)
```

### 搜索查询（AList 源码 internal/db/searchnode.go:57-90，sqlite3 分支）
```sql
-- useFullText=false 或 Database.Type=='sqlite3' 时走 LIKE
SELECT * FROM x_search_nodes
WHERE (parent LIKE '/parent/%' OR parent = '/parent')   -- whereInParent()
  AND name LIKE '%keyword1%' AND name LIKE '%keyword2%' -- 多关键词 AND
ORDER BY name ASC
LIMIT ? OFFSET ?
-- 注: keywords 用 strings.Fields() 按空格分词，每个词一个 LIKE
```

## X_SEARCH_NODES_SCHEMA

| 字段 | 类型 | 说明 | 来源 |
|------|------|------|------|
| parent | TEXT | 父目录路径，如 `/电影/2024` | TXT 解析 rfind('/') 前部分，前补 '/' |
| name | TEXT | 文件/目录名 | TXT 解析 rfind('/') 后部分 |
| is_dir | NUMERIC | 是否目录，固定为 1 | xiaoya 脚本硬编码 |
| size | INTEGER | 大小，固定为 0 | xiaoya 脚本硬编码 |

**索引**: `idx_x_search_nodes_parent ON x_search_nodes (parent, name)` — 复合索引，服务于 `whereInParent` 的 parent 前缀匹配 + name 排序。

**AList GORM 模型** (internal/model/search.go:23-28):
```go
type SearchNode struct {
    Parent string `json:"parent" gorm:"index"`
    Name   string `json:"name"`
    IsDir  bool   `json:"is_dir"`
    Size   int64  `json:"size"`
}
```
注：AList 自身 BuildIndex 会填真实 is_dir 和 size；xiaoya 脚本只填 is_dir=1, size=0（只索引目录/文件名，不关心类型和大小）。

## ALIST_DEPENDENCY

xiaoya-alist-search 直接操作 AList 内部 SQLite 表，强耦合点：

1. **`x_search_nodes` 表** — AList 搜索索引表，schema 由 AList GORM 模型 `model.SearchNode` 定义
2. **`x_storages` 表** — AList 存储挂载表，19 条记录的 17 个字段必须完全匹配 AList Storage 模型
3. **`x_setting_items` 表** — AList 设置表，`search_index` 键控制搜索引擎选择
4. **`sqlite_sequence` 表** — SQLite 自增序列表，手动设置 `x_storages` 序列为 19
5. **`database_non_full_text` searcher** — AList 内置搜索引擎之一 (internal/search/db_non_full_text/)
6. **`webdav_policy` 字段** — AList Storage.Proxy 结构体的字段，控制下载策略
7. **WebDav 驱动 `OnlyProxy: true`** — AList WebDav 驱动强制 native_proxy（不能 302）
8. **AList V3 驱动 `Link()` → `/api/fs/get`** — 上游 AList 的 API 契约
9. **`data.db` 文件路径** — AList Docker 镜像固定数据库路径 `/opt/alist/data/data.db`
10. **`xhofe/alist:latest` Docker 镜像** — 依赖特定 AList 版本的表结构

## SEARCH_TO_RESOURCE

搜索结果到实际资源访问的映射机制：

1. **搜索返回**：`SearchNode{Parent, Name, IsDir, Size}` — parent 是完整父路径（如 `/电影/2024`），name 是文件名
2. **前端拼接**：`path = parent + "/" + name`（如 `/电影/2024/xxx.mkv`）
3. **路径路由**：前端请求 `/d/电影/2024/xxx.mkv`（下载）或 `/p/电影/2024/xxx.mkv`（预览）
4. **AList 路由**：`server/router.go` 将 `/d/*` 和 `/p/*` 路由到 `handles.Down` 或 `handles.Proxy`
5. **存储查找**：`fs.GetStorage(rawPath)` 按挂载路径前缀匹配（如 `/电影` → 找到 id=10 的 AList V3 存储）
6. **获取直链**：`fs.Link(rawPath)` 调用对应驱动的 `Link()` 方法：
   - **AList V3 驱动**：`POST {上游地址}/api/fs/get` → 返回 `RawURL`（上游 AList 的直链）
   - **WebDav 驱动**：`PROPFIND {上游地址}/dav/电影/2024/xxx.mkv` → 返回带认证 header 的 URL
7. **下载策略**（由 `webdav_policy` 决定）：
   - `302_redirect`：`c.Redirect(302, link.URL)` — 客户端直接访问上游 URL（AList V3 用此）
   - `native_proxy`：`common.Proxy(w, r, link, file)` — AList 流式转发，客户端只看到 AList URL（WebDav 用此，因 WebDav 直链含认证）
   - `use_proxy_url`：重定向到 `down_proxy_url`（xiaoya 未用）

**关键**：搜索结果的 parent 字段就是 AList 挂载路径下的实际路径，无需路径重写。xiaoya 脚本插入的 parent 直接来自 TXT 文件中的路径（如 `./电影/2024` → parent=`/电影/2024`），与挂载路径 `/电影` 自然匹配。

## UPDATE_MECHANISM

### 全量重建（非增量）

xiaoya-alist-search 的更新脚本（xiaoya-alist-search.sh:357-459）流程：
1. `docker stop` 新 AList 容器
2. `docker cp` 从原小雅拷贝最新 TXT
3. Python: `DELETE FROM x_search_nodes`（清空全表）
4. Python: 逐文件批量插入（1000 条/批）
5. Python: 去重（保留 MIN(rowid)）
6. `docker start` 新容器

**注意**：更新脚本不重建 `x_storages`（挂载配置不变），不重建索引（索引已存在），不清空后再建表（用 IF NOT EXISTS）。

### AList 自身的增量更新（对比）

AList 内置的 `search.Update()` (internal/search/build.go:202-268) 是增量：
- 对比 `instance.Get(parent)` 旧节点 vs `objs` 新节点
- `old.Difference(now)` → 删除不存在的
- `now.Difference(old)` → 添加新增的
- 由 `op.HandleObjsUpdateHook` 在 List 操作后异步触发

但 xiaoya-alist-search **不使用** AList 的增量更新，而是绕过 AList 直接操作 SQLite。

### 一致性风险

1. **更新期间搜索不可用**：脚本先 `docker stop` 再 `DELETE FROM x_search_nodes`，期间搜索返回空。但脚本在容器停止时操作 DB，所以用户无法搜索（容器没运行）。`docker start` 后数据已就绪。**风险较低**。
2. **无事务包裹**：Python 脚本的 `clear_table()` 和 `insert_data_batch()` 是独立连接独立提交，若中途崩溃会留下部分数据。但下次更新会先 DELETE 全表，可自愈。
3. **去重前有重复**：批量插入后、去重前，表中有重复记录（同一 parent+name 多行）。搜索可能返回重复结果，但去重后正常。
4. **docker cp 原子性**：`docker cp` 是逐文件拷贝，非原子。若原小雅正在更新 TXT，可能拷贝到部分更新的文件。但 xiaoya 假设原小雅 TXT 静态。
5. **无锁**：直接操作 SQLite 文件无文件锁。若 AList 容器未完全停止就写 DB，可能损坏。脚本有 `sleep 3` 缓冲但非可靠。

## VERIFIED_FACTS

1. **xiaoya-alist-search 是单 Bash 脚本 + 内嵌 Python**，480 行，无独立 Python 文件
   - 证据：`xiaoya-alist-search.sh:119-344` 内嵌 Python via heredoc

2. **新 AList 通过 docker-compose 创建**，镜像 `xhofe/alist:latest`
   - 证据：`xiaoya-alist-search.sh:67-85`，docker-compose.yml 内联生成

3. **TXT 通过 docker cp 从原小雅容器拷贝**
   - 证据：`xiaoya-alist-search.sh:116` `docker cp "${DOCKER_NAME}:/index/." "$INSTALL_PATH/alist-temp/"`

4. **TXT 解析逻辑**：逐行读，`#` 分割取路径，`rfind('/')` 分割 parent/name
   - 证据：`xiaoya-alist-search.sh:268-282`（首次安装）和 `395-416`（更新脚本）

5. **x_search_nodes 表结构**：parent TEXT, name TEXT, is_dir NUMERIC, size INTEGER
   - 证据：`xiaoya-alist-search.sh:233-240` CREATE TABLE 语句

6. **索引**：`idx_x_search_nodes_parent ON x_search_nodes (parent, name)`
   - 证据：`xiaoya-alist-search.sh:247`

7. **database_non_full_text 设置**
   - 证据：`xiaoya-alist-search.sh:323-327` UPDATE x_setting_items
   - AList 源码：`internal/search/db_non_full_text/init.go:8` Name: "database_non_full_text"

8. **sqlite3 总是走 LIKE 查询**（无论 useFullText）
   - 证据：`internal/db/searchnode.go:59` `if !useFullText || conf.Conf.Database.Type == "sqlite3"`

9. **搜索查询用 LIKE + parent 前缀过滤**
   - 证据：`internal/db/searchnode.go:60-64`，`whereInParent` 函数 line 15-22

10. **webdav_policy: WebDav→native_proxy, AList V3→302_redirect**
    - 证据：`xiaoya-alist-search.sh:202` `webdav_policy = 'native_proxy' if driver == 'WebDav' else '302_redirect'`

11. **WebDav 驱动 OnlyProxy=true，强制 native_proxy**
    - 证据：`drivers/webdav/meta.go:19` `OnlyProxy: true`
    - `internal/op/driver.go:107-114` OnlyProxy 时默认 native_proxy

12. **302_redirect 实现**：`c.Redirect(302, link.URL)`
    - 证据：`server/handles/down.go:122`

13. **native_proxy 实现**：`common.Proxy(w, r, link, file)` 流式转发
    - 证据：`server/webdav/webdav.go:250` 和 `server/handles/down.go:169`

14. **AList V3 Link 调用上游 /api/fs/get 获取 RawURL**
    - 证据：`drivers/alist_v3/driver.go:121-132`

15. **更新机制是全量重建**：DELETE 全表 + 批量插入 + 去重
    - 证据：`xiaoya-alist-search.sh:377-383` clear_table(), `385-393` insert_data_batch(), `436-450` deduplicate_records()

16. **19 个挂载路径**：体育/动漫/教育/整理中/曲艺/有声书/每日更新/游戏/电子书/电影/电视剧/纪录片/综艺/资料/音乐/夸克分享/115分享/画质演示/PikPak分享
    - 证据：`xiaoya-alist-search.sh:152-194` data 列表

17. **xiaoya-alist-search 许可证：Apache-2.0**
    - 证据：`/tmp/worker-c/xiaoya-alist-search/LICENSE` Apache License 2.0

18. **AList 许可证：AGPLv3**
    - 证据：`/tmp/worker-c/alist/LICENSE` GNU AFFERO GENERAL PUBLIC LICENSE Version 3

19. **AList 搜索 API 路由**：`GET /api/fs/search`
    - 证据：`server/router.go:224` `g.Any("/search", middlewares.SearchIndex, handles.Search)`

20. **搜索结果权限过滤**：按 user.BasePath 和 meta 密码校验
    - 证据：`server/handles/search.go:59-68`

## EVIDENCE

| 编号 | 文件 | 行号 | 说明 |
|------|------|------|------|
| E1 | xiaoya-alist-search.sh | 67-85 | docker-compose 创建新 AList 容器 |
| E2 | xiaoya-alist-search.sh | 116 | docker cp 拷贝 TXT |
| E3 | xiaoya-alist-search.sh | 119-344 | 内嵌 Python 脚本 |
| E4 | xiaoya-alist-search.sh | 131 | sqlite_sequence 插入 |
| E5 | xiaoya-alist-search.sh | 152-194 | 19 条挂载配置 |
| E6 | xiaoya-alist-search.sh | 202 | webdav_policy 三元表达式 |
| E7 | xiaoya-alist-search.sh | 207-213 | INSERT INTO x_storages |
| E8 | xiaoya-alist-search.sh | 224 | DELETE FROM x_search_nodes |
| E9 | xiaoya-alist-search.sh | 233-240 | CREATE TABLE x_search_nodes |
| E10 | xiaoya-alist-search.sh | 247 | CREATE INDEX |
| E11 | xiaoya-alist-search.sh | 255-258 | 批量 INSERT |
| E12 | xiaoya-alist-search.sh | 268-282 | TXT 解析逻辑 |
| E13 | xiaoya-alist-search.sh | 307-314 | 去重 SQL |
| E14 | xiaoya-alist-search.sh | 323-327 | 设置 database_non_full_text |
| E15 | xiaoya-alist-search.sh | 357-459 | 更新脚本生成 |
| E16 | alist/internal/model/search.go | 23-28 | SearchNode GORM 模型 |
| E17 | alist/internal/db/searchnode.go | 15-22 | whereInParent 实现 |
| E18 | alist/internal/db/searchnode.go | 57-90 | SearchNode 查询实现 |
| E19 | alist/internal/search/db_non_full_text/init.go | 7-10 | searcher 配置 |
| E20 | alist/internal/search/db_non_full_text/search.go | 17-19 | Search 调用 useFullText=false |
| E21 | alist/server/handles/search.go | 27-80 | 搜索 API handler |
| E22 | alist/server/handles/down.go | 99-123 | down() 302 重定向 |
| E23 | alist/server/handles/down.go | 125-179 | localProxy() 流式代理 |
| E24 | alist/server/webdav/webdav.go | 242-264 | WebDAV 下载策略 |
| E25 | alist/internal/model/storage.go | 50-60 | Webdav302/Native/Proxy 判定 |
| E26 | alist/drivers/webdav/meta.go | 19 | OnlyProxy: true |
| E27 | alist/drivers/alist_v3/driver.go | 111-133 | AList V3 Link 实现 |
| E28 | alist/internal/op/driver.go | 84-114 | webdav_policy 选项生成 |
| E29 | alist/internal/search/build.go | 202-268 | AList 自身增量更新 |
| E30 | alist/internal/bootstrap/data/setting.go | 209 | search_index 选项 |

## UNVERIFIED

1. **原小雅 AList 的 /index/*.txt 格式来源**：未找到原小雅 AList 生成 TXT 的源码，推测由小雅生态的另一个工具生成。TXT 每行格式为 `./分类/子目录/文件名#...`（# 后内容被丢弃）。
2. **原小雅 AList 是否就是 xiaoya-alist-search 创建的上游**：脚本假设用户已部署原小雅 AList（输入其地址），但原小雅 AList 的部署工具不在本仓库。
3. **pass_ua_to_upsteam 字段作用**：AList V3 Addition 中有此字段，推测是透传客户端 User-Agent 给上游（用于防盗链），但未深入验证。
4. **database_non_full_text vs database 在 sqlite3 下的实际差异**：从源码看 sqlite3 下两者都走 LIKE，似乎无差异。推测 database_non_full_text 只是语义标记，实际差异仅在 mysql/postgres（database 用全文索引，database_non_full_text 用 LIKE）。
5. **更新脚本是否被自动调度**：脚本提示用户手动添加到任务计划（line 463），无内置 cron。

## RISKS

1. **AGPLv3 传染性风险**：AList 是 AGPLv3，若 IndexCore 直接链接 AList 源码或修改 AList 并网络提供服务，IndexCore 必须开源。xiaoya-alist-search 通过外部操作 SQLite 文件规避了链接，但若 IndexCore 嵌入 AList 二进制则需注意。
2. **AList 表结构版本耦合**：xiaoya-alist-search 直接写 `x_storages` 的 17 个字段，若 AList 升级增删字段，脚本会失效。当前脚本针对特定 AList 版本。
3. **SQLite 并发写风险**：更新脚本在容器停止时写 DB，但若用户误操作在容器运行时执行，可能损坏 DB。
4. **docker cp 非原子**：拷贝期间原小雅 TXT 若在更新，可能得到不一致的索引快照。
5. **去重性能**：`DELETE WHERE rowid NOT IN (SELECT MIN(rowid) ... GROUP BY)` 在大表上性能差（子查询全表扫描）。对于百万级索引可能很慢。
6. **无增量更新**：每次全量重建，对于大索引（如百万文件）耗时可能数分钟，期间搜索不可用。
7. **is_dir 和 size 不准确**：xiaoya 脚本硬编码 is_dir=1, size=0，搜索结果的类型和大小信息不可靠。AList 前端可能显示异常。
8. **parent 路径前补 '/' 可能错误**：`parent = '/' + path_with_file[:last_slash_index]`，若 TXT 路径以 '/' 开头会变成 '//'。但小雅 TXT 用 './' 开头，replace('./','') 后无前导 '/'，所以实际无问题。

## LICENSE

| 组件 | 许可证 | 可复用性 |
|------|--------|----------|
| xiaoya-alist-search | Apache-2.0 | 可复用（需保留版权声明） |
| AList (alist-org/alist) | AGPLv3 | 传染性：若链接/修改/网络服务需开源；若仅外部操作其 DB 不传染 |
| OpenList | 未检查（任务未要求深入） | 待定 |

**关键判断**：xiaoya-alist-search 的 Apache-2.0 允许复用其 TXT 解析逻辑和 SQLite 操作逻辑。AList 的 AGPLv3 不传染外部操作其 SQLite 文件的项目（xiaoya-alist-search 正是如此规避）。

## COPY_CANDIDATE

### 1. TXT 解析逻辑（Apache-2.0，可直接复用）
- **来源**：xiaoya-alist-search.sh:268-282 和 395-416
- **逻辑**：逐行读 TXT → `line.split('#')[0].replace('./','')` → `rfind('/')` 分割 parent/name
- **复用价值**：简单高效的路径解析，无外部依赖
- **许可证确认**：Apache-2.0 允许

### 2. 批量插入 + 去重模式（Apache-2.0，可复用模式）
- **来源**：xiaoya-alist-search.sh:251-259, 302-316
- **逻辑**：1000 条/批 executemany + `DELETE WHERE rowid NOT IN (MIN(rowid) GROUP BY)` 去重
- **复用价值**：大批量导入 SQLite 的通用模式
- **许可证确认**：Apache-2.0 允许

### 3. 全量重建流程（Apache-2.0，可复用流程）
- **来源**：xiaoya-alist-search.sh:377-456
- **逻辑**：stop → copy TXT → DELETE 全表 → 批量插入 → 去重 → start
- **复用价值**：简单可靠的索引重建流程
- **许可证确认**：Apache-2.0 允许

## REWRITE_CANDIDATE

### 1. 挂载配置生成（值得重写）
- **来源**：xiaoya-alist-search.sh:152-194（19 条硬编码挂载）
- **原因**：分类目录硬编码，无法适配不同小雅配置
- **重写方向**：从原小雅 AList 的 `/api/fs/list` 动态发现分类目录，生成挂载配置

### 2. 更新调度（值得重写）
- **来源**：xiaoya-alist-search.sh:357-459（手动 cron）
- **原因**：无内置调度，无增量更新，无失败重试
- **重写方向**：内置定时器 + 增量 diff（对比新旧 TXT）+ 失败回滚

### 3. 一致性保障（值得重写）
- **来源**：xiaoya-alist-search.sh:99-113（stop/sleep/cp）
- **原因**：无事务、无锁、无原子性保障
- **重写方向**：用 SQLite 事务包裹 DELETE+INSERT，或写入临时表后原子 RENAME

### 4. is_dir/size 填充（值得重写）
- **来源**：xiaoya-alist-search.sh:282（硬编码 is_dir=1, size=0）
- **原因**：信息丢失，搜索结果无法区分文件/目录
- **重写方向**：从原小雅 AList API 获取真实类型和大小，或在 TXT 格式中扩展

## IDEA_ONLY

### 1. database_non_full_text 的命名思想
- **思想**：区分"全文索引"（MATCH/AGAINST）和"子串匹配"（LIKE），对 sqlite3 后者更实用
- **借鉴**：IndexCore 可设计类似搜索引擎选择机制，但无需照搬实现

### 2. webdav_policy 三策略
- **思想**：302_redirect（直连上游）/ native_proxy（本地代理）/ use_proxy_url（自定义代理）
- **借鉴**：IndexCore 的资源访问层可设计类似策略，根据上游特性选择

### 3. AList 的增量更新 diff 算法
- **思想**：`old.Difference(now)` 删除 + `now.Difference(old)` 添加（internal/search/build.go:224-267）
- **借鉴**：IndexCore 增量更新可借鉴集合 diff 思路，但需自己实现（AList 是 AGPLv3）

### 4. searcher 插件注册模式
- **思想**：`searcher.RegisterSearcher(config, factory)` 注册多种搜索引擎（internal/search/searcher/）
- **借鉴**：IndexCore 可设计类似插件接口，支持多种索引后端

## IGNORE

### 1. docker-compose 容器编排
- **原因**：IndexCore 是独立服务，不应依赖 Docker 套娃部署模式

### 2. docker cp 文件拷贝
- **原因**：依赖 Docker 容器内部路径，非通用方案。IndexCore 应通过 API 或挂载卷获取索引数据

### 3. sqlite_sequence 手动操作
- **原因**：`INSERT OR REPLACE INTO sqlite_sequence` 是 SQLite 内部表操作，不应直接操作

### 4. AList GORM 模型和 DB 操作代码
- **原因**：AGPLv3 传染性，且与 AList 内部 schema 强绑定

### 5. AList 的 BuildIndex/WalkFS 实现
- **原因**：AGPLv3，且依赖 AList 内部 fs/op 包

### 6. 硬编码的 19 个分类目录名
- **原因**：小雅生态特有，不可通用

## UNRESOLVED

1. **原小雅 AList 的 /index/*.txt 由谁生成**：未找到生成端源码。推测由小雅工具链的另一个组件（如 xiaoya-alist 或 xiaoya-pro）生成，格式为每行 `./分类/路径/文件名#可选注释`。
2. **OpenList 是否兼容 AList 的 x_search_nodes schema**：未检查 OpenList 源码。若 OpenList 是 AList fork，schema 可能相同。
3. **AList V3 驱动的 pass_ua_to_upsteam 字段在 302_redirect 下的实际效果**：302 后客户端直连上游，UA 透传是否生效取决于上游是否检查 UA。未深入验证。
4. **database_non_full_text 在 mysql/postgres 下的行为**：源码显示 useFullText=false 时走 LIKE，但 xiaoya 只用 sqlite3，未验证其他 DB。
5. **AList 搜索结果的 parent 字段是否包含挂载路径前缀**：从 xiaoya 脚本看，parent 是 `/电影/2024`（含挂载路径 `/电影`），但 AList 自身 BuildIndex 生成的 parent 是否也含挂载路径前缀，未完全验证（从 build.go:172 `Parent: path.Dir(indexPath)` 看，indexPath 是完整路径含挂载前缀，所以一致）。
6. **多用户搜索权限隔离**：search.go:59 检查 `strings.HasPrefix(node.Parent, user.BasePath)`，但 x_search_nodes 中 parent 是全局路径，若多用户共享同一 AList 实例，权限隔离依赖此检查。未验证复杂场景。
7. **AList 升级时 x_storages schema 变更的兼容性**：xiaoya 脚本硬编码 17 个字段，AList 版本升级若增删字段会失效。未检查 AList 版本历史中的 schema 变更。