# 小雅索引架构调查报告 — Discovery 01

> 阶段：Discovery 01 — 小雅索引链路调查
> 状态：REWORK → READY_FOR_ARCH_REVIEW（v2）
> 日期：2026-09-22
> 产出：Foreman 统一整理四份 Worker 报告
> 约束：本报告只总结调查事实与候选方案，不做最终架构决定
> 修订：v2 修正 3 处表述（生成器未证实 / 许可证不下法律定论 / COPY 降级）

---

## 一、调查概述

本报告由 Foreman 基于 Worker A-D 四份独立调查报告交叉核验后统一整理。

| Worker | 任务 | 报告 | 结论 |
|--------|------|------|------|
| A | 小雅整体索引链路 | `d01/W-A-XIAOYA-SYSTEM-CHAIN.md` | PASS |
| B | index.zip 数据格式 | `d01/W-B-XIAOYA-INDEX-FORMAT.md` | PASS |
| C | AList 搜索导入链路 | `d01/W-C-ALIST-SEARCH-IMPORT.md` | PASS |
| D | Diff / 安全删除 | `d01/W-D-XIAOYA-DIFF-SAFETY.md` | PASS |

---

## 二、系统链路总结（W-A）

小雅是一套"预生成索引分发 + 客户端 AList 统一挂载 + Emby 可视化"的家庭影视方案。

> **已证实**：客户端下载预生成的 index.zip/update.zip/strm.zip，不自行扫描 Provider。
> **未证实**：索引生成器源码和生成位置未找到（推测在闭源镜像或未公开仓库中）。

### 数据流

```
真实资源（阿里云盘/115/夸克/PikPak 公开分享）
    │
    │  [推测] 服务端预整理 → 3 生成器源码未找到
    ▼
index.zip（搜索索引）  update.zip（AList 挂载 SQL）  strm.zip（strm 列表）  tvbox.zip（TVBox 配置）
    │
    │  GitHub data 仓库分发 → 客户端容器拉取
    ▼
AList 容器（xiaoyaliu/alist:hostmode）
    ├── update.sql → 注册网盘分享为 AList storage
    ├── index/*.txt → 搜索索引
    ├── /d/路径 → 302 直链访问
    ├── /dav → WebDAV 访问
    └── /tvbox → TVBox 订阅
    │
    ▼
Emby 容器 → 影视库 UI → 用户访问
```

### 组件分类

| 组件 | 类型 | 职责 |
|------|------|------|
| docker-xiaoya | 部署壳 | Docker Compose 一键部署 |
| xiaoya-alist | 安装脚本 | 菜单式安装/更新/卸载 |
| xiaoyaDev/data | 数据包 | 索引/配置/strm 分发 |
| xiaoyaliu/alist 镜像 | Docker 镜像 | 定制版 AList（含数据下载/搜索/TVBox） |
| xiaoya_db (solid.py) | Python 源码 | Emby 元数据爬虫 |

### 关键事实

- 客户端**无需自行扫描 Provider**，下载预生成的 index.zip（已证实）
- 索引生成器源码和生成位置**未找到**（推测在闭源镜像或未公开仓库中）
- 代码引用 `xiaoyaliu00/data` 而非任务指定的 `xiaoyaDev/data`（两者关系未证实）
- WebDAV 默认凭据 guest/guest_Api789

---

## 三、索引数据格式总结（W-B）

### index.zip 结构

- **41 个 UTF-8 TXT 文件**，压缩 17MB / 解压 100MB / **562,298 行**
- 按媒体类型分文件：`index.movie.txt`、`index.115.txt`、`index.tv.txt` 等

### TXT 行格式

两种形态，`#` 分隔：

```
形态 A（纯路径）：./电子书/5000本/VOL01-05
形态 B（带元数据）：./路径#标题#豆瓣ID#评分#海报URL[#年份#国家#类型]
```

字段表（8 字段变体）：

| 位置 | 字段 | 语义 |
|------|------|------|
| 1 | path | 相对路径（`./` 开头） |
| 2 | title | 中文标题（豆瓣标题） |
| 3 | douban_id | 豆瓣影视 ID |
| 4 | rating | 豆瓣评分 |
| 5 | poster_url | 豆瓣海报 URL |
| 6 | year | 上映年份（可选） |
| 7 | country | 国家/地区（可选） |
| 8 | genres | 类型/流派（可选） |

### update.zip

**不是索引增量**，而是 AList 存储挂载配置（`x_storages` 表 SQL + opentoken 刷新地址）。

### Snapshot 字段映射

| SnapshotEntry 字段 | 小雅对应 | 有/缺 |
|---------------------|----------|-------|
| provider_object_id | 无 | **缺** |
| parent_ref | parent（parser 推导） | 有 |
| path | 字段1 | 有 |
| name | 路径末段 | 有 |
| is_dir | 无（恒置 1） | **缺** |
| size | 无（恒置 0） | **缺** |
| mtime | 无 | **缺** |
| hash | 无 | **缺** |
| metadata | 字段2-8 | 有但被官方 parser 丢弃 |

**结论**：小雅索引不足以直接生成通用 Snapshot，缺 5 个关键字段。

---

## 四、搜索导入链路总结（W-C）

### 完整调用链

```
原小雅 AList (/index/*.txt)
    │ docker cp
    ▼
xiaoya-alist-search.sh (Bash + 内嵌 Python)
    │ 逐行解析: line.split('#')[0] → rfind('/') → (parent, name)
    ▼
SQLite x_search_nodes(parent, name, is_dir=1, size=0)
    │
    ▼
AList Search (database_non_full_text)
    │ SELECT * WHERE parent LIKE '/parent/%' AND name LIKE '%keyword%'
    ▼
搜索结果 (parent, name) → 前端拼接路径 → /d/{parent}/{name}
    │
    ├─ AList V3 驱动 → 302_redirect（客户端直连上游）
    └─ WebDav 驱动 → native_proxy（AList 流式代理）
```

### 关键 SQL

```sql
-- 建表
CREATE TABLE x_search_nodes (parent TEXT, name TEXT, is_dir NUMERIC, size INTEGER)

-- 搜索（sqlite3 总是走 LIKE）
SELECT * FROM x_search_nodes
WHERE (parent LIKE '/parent/%' OR parent = '/parent')
  AND name LIKE '%keyword1%' AND name LIKE '%keyword2%'
ORDER BY name ASC LIMIT ? OFFSET ?
```

### 更新机制

**全量重建**（非增量）：DELETE 全表 → 批量插入（1000 条/批）→ 去重。更新期间容器停止，搜索不可用。

### AList 依赖点

xiaoya-alist-search 直接操作 AList 内部 SQLite 表（x_search_nodes / x_storages / x_setting_items），与 AList GORM 模型和 DB schema 强耦合。

---

## 五、Diff / 安全删除总结（W-D）

### 两个实现对比

| 维度 | Python (xiaoya_db) | Go (xiaoya_emd_go) |
|------|--------------------|--------------------|
| 远端列表 | HTTP 爬取 HTML 目录 | `.scan.list.gz` 索引文件 |
| 并发模型 | asyncio + Semaphore(100) | goroutine + channel |
| Complete Gate | **有**（gap < 10） | **无** |
| 回收站 | **无**（os.remove 硬删除） | **有**（→ recycle_bin/） |
| 原子写入 | DB 层面有（tempdb → rename） | 文件层面有（.tmp + rename） |
| checkpoint/resume | **无** | **无** |
| 下载校验 | **无** | **无** |

### 安全机制对照蓝图

| 蓝图概念 | Python | Go | 缺口 |
|----------|--------|-----|------|
| Complete Gate | 有（gap<10） | **无** | Go 版可能大规模误删 |
| Removal Guard | **无** | 有（recycle_bin） | Python 版不可恢复 |
| Unexpected Shrink Guard | 部分 | **无** | 两者都无删除数量阈值 |
| Provider Error Guard | 部分 | 有 | Python 全不可用时退出 |
| Staging/Atomic Commit | DB 层有 | 文件层有 | Python 文件层无 |

### 关键缺口

1. Go 版缺少 Complete Gate — 服务器数据异常时会大规模误删（虽有回收站兜底）
2. Python 版缺少 Removal Guard — 硬删除不可恢复
3. 两者都缺少下载后校验 — 网络错误可能产生损坏文件
4. 两者都缺少事务性 — 删除和下载不是原子的

---

## 六、交叉核验结论

| 核验项 | 结果 |
|--------|------|
| W-A 数据流 vs W-C 搜索链路 | 一致：W-A 描述原小雅容器内索引，W-C 描述新 AList 从原小雅拷贝索引，两阶段衔接 |
| W-B 数据格式 vs W-C Parser | 完全一致：`split('#')[0]` → `rfind('/')` → (parent, name), is_dir=1, size=0 |
| W-D 数据源 vs W-A/B 结论 | 一致：W-D 研究的是 Emby 元数据同步器（非搜索索引），与 W-A 确认的 solid.py 爬虫一致 |
| 许可证判断一致性 | 一致：data 无 LICENSE（A+B），xiaoya-alist-search Apache 2.0（B+C） |

**无冲突。**

---

## 七、许可证综合判断

> 本节只陈述许可证事实与我们的工程隔离策略，不下法律定论。具体法律影响需咨询律师。

| 仓库/组件 | 许可证事实 | 工程隔离策略 |
|------------|-----------|--------------|
| xiaoya-alist-search | Apache-2.0 | 允许复用，需保留版权声明 |
| AList (alist-org/alist) | AGPLv3 | 不链接其源码、不修改其二进制；通过外部操作 SQLite 文件交互（xiaoya-alist-search 即用此策略） |
| docker-xiaoya | CC BY-NC 4.0 | 不作为代码底座，仅研究部署模式 |
| xiaoya-alist | GPL-3.0 | 不复制代码，独立重写所需逻辑 |
| xiaoyaDev/data | 无 LICENSE | 不复制数据到产品仓库，仅研究格式 |
| xiaoya_db (Python) | 无 LICENSE | 不复制代码，借思想独立实现 |
| xiaoya_emd_go (Go) | GPL-3.0 | 不复制代码，借思想独立实现 |

**工程策略**：当前工程策略是不复制、不链接相关源码，优先通过公开 API 或独立进程边界交互；具体许可证义务在实际采用相关组件前另行审查。

---

## 八、复用候选分类

### REFERENCE / OPTIONAL_COPY（Apache-2.0，但逻辑小且耦合 AList，以后再决定是否真搬）

| 候选 | 来源 | 价值 | 降级原因 |
|------|------|------|----------|
| TXT 解析逻辑 | xiaoya-alist-search.sh:268-282 | 逐行读 → `split('#')[0]` → `rfind('/')` → (parent, name) | 逻辑仅 15 行，重写成本极低 |
| 批量插入 + 去重模式 | xiaoya-alist-search.sh:251-316 | 1000 条/批 executemany + GROUP BY 去重 | 模式通用，但耦合 AList 的 x_search_nodes 表结构 |
| 全量重建流程 | xiaoya-alist-search.sh:377-456 | stop → copy → DELETE → INSERT → dedup → start | 耦合 docker stop/start，非通用方案 |

> Architect 决定：先从 COPY 降为 REFERENCE / OPTIONAL_COPY，以后再决定是否真搬。

### REWRITE（值得重写，不复制代码）

| 候选 | 来源 | 重写方向 |
|------|------|----------|
| 挂载配置生成 | xiaoya-alist-search.sh:152-194 | 从硬编码 19 条改为动态发现分类目录 |
| 更新调度 | xiaoya-alist-search.sh:357-459 | 加入定时器 + 增量 diff + 失败回滚 |
| 一致性保障 | xiaoya-alist-search.sh:99-113 | 用 SQLite 事务包裹 DELETE+INSERT |
| is_dir/size 填充 | xiaoya-alist-search.sh:282 | 从 AList API 获取真实类型和大小 |
| `.tmp` + rename 原子写入 | xiaoya_emd_go main.go:970-980 | Python: tempfile.NamedTemporaryFile + os.replace |
| recycle_bin 软删除 | xiaoya_emd_go main.go:1001-1036 | Python: shutil.move + 时间戳后缀 |
| 服务器并行探测选择 | xiaoya_emd_go main.go:604-721 | asyncio.gather 并行探测，按时间戳+响应时间排序 |
| Complete Gate (gap 检查) | xiaoya_db solid.py:465 | 删除前检查 abs(len(remote) - len(declared)) < threshold |
| 30 分钟跳过检测 | xiaoya_emd_go main.go:1551 | 持久化 last_sync_time，比较 Last-Modified |
| `.tmp` 拘留清理 | xiaoya_emd_go main.go:1140-1155 | 启动时 glob("**/*.tmp") 清理 |

### IDEA_ONLY（只借思想）

| 候选 | 来源 | 思想 |
|------|------|------|
| database_non_full_text 命名 | AList | 区分全文索引和子串匹配 |
| webdav_policy 三策略 | AList | 302_redirect / native_proxy / use_proxy_url |
| AList 增量更新 diff | AList build.go:224-267 | old.Difference(now) + now.Difference(old) |
| searcher 插件注册 | AList internal/search/searcher/ | 多搜索引擎可插拔 |
| 服务器池动态获取 | xiaoya_db solid.py:66-126 | 远程获取 + 硬编码 fallback |
| need_download 三条件 | xiaoya_db solid.py:265-287 | 不存在 / 大小不同 / 时间戳更新 |
| 10 分钟误差容忍 | xiaoya_emd_go main.go:878 | 避免时钟漂移导致不必要更新 |
| 内存限制检查 | xiaoya_emd_go main.go:816-823 | 大库扫描的资源保护 |
| 增量路径计数 | xiaoya_emd_go | 同步后增量更新计数，避免全量重扫 |

### IGNORE（不适合复用）

| 候选 | 原因 |
|------|------|
| docker-compose 容器编排 | IndexCore 是独立服务，不应 Docker 套娃 |
| docker cp 文件拷贝 | 依赖容器内部路径，非通用方案 |
| sqlite_sequence 手动操作 | SQLite 内部表，不应直接操作 |
| AList GORM 模型和 DB 操作 | AGPLv3 + schema 强绑定 |
| AList BuildIndex/WalkFS | AGPLv3 + 依赖 AList 内部包 |
| 硬编码 19 个分类目录名 | 小雅生态特有，不可通用 |
| Python HTML 目录爬取 | 不应爬取 HTML，应用结构化 API |
| Go DOH/DOT DNS 解析 | 与 Diff/安全删除无关 |
| Go Web UI / Docker 配置 | 与核心同步逻辑无关 |

---

## 九、关键缺口与风险

### 数据层面

1. **无 stable object id / hash**：索引条目只能靠路径标识，路径变更无法做稳定 diff
2. **is_dir 恒为 1**：不区分目录与文件，依赖 is_dir 的逻辑都会出错
3. **size / mtime / hash 全缺**：无法做变更检测、内容级去重、完整性校验
4. **元数据被官方 parser 丢弃**：title/douban_id/rating/poster 等需自行解析

### 安全层面

5. **Go 版无 Complete Gate**：扫描不完整时会大规模误删（回收站可恢复但影响可用性）
6. **Python 版无 Removal Guard**：硬删除不可恢复
7. **两者都无下载校验**：网络错误可能产生损坏文件
8. **两者都无事务性**：删除和下载不是原子的

### 许可证层面

9. **data 仓库无 LICENSE**：使用该数据资产存在法律风险
10. **AList AGPLv3**：不链接源码，通过外部操作 SQLite 交互（工程隔离策略，是否足够需法律顾问确认）
11. **xiaoya_db 无 LICENSE**：默认 All Rights Reserved

### 运维层面

12. **全量重建无增量**：562k 条目每次整体替换，大库耗时
13. **更新期间搜索不可用**：容器停止后操作 DB
14. **AList 表结构版本耦合**：AList 升级可能破坏 xiaoya 脚本

---

## 十、未决问题（UNRESOLVED）

1. xiaoyaliu00/data 与 xiaoyaDev/data 的确切关系（fork？镜像？）
2. xiaoyaliu/alist:hostmode 镜像内部如何消费 index.zip/update.zip/strm.zip
3. /index/*.txt 的生成逻辑源码位置（推测在闭源镜像内）
4. index.zip 中 5 字段 vs 8 字段的选择规则
5. strm.txt 中 `{tmdb-XXXX}` 标记与 index.zip 中豆瓣 ID 的关联关系
6. data 仓库的更新频率与机制（CI？手动？）
7. version.txt 0.57.27 与 alist 镜像版本的兼容性矩阵
8. OpenList 是否兼容 AList 的 x_search_nodes schema
9. Python xiaoya_db 原作者（Rik-F5）是否曾有 LICENSE
10. Go 版 maxConcurrency 默认值
11. Go 版 .scan.list.gz 下载是否校验完整性
12. Go 版回收站是否有最大容量限制

---

## 十一、候选方案（供 Architect 决策）

> 以下为候选方案，不做最终架构决定。

### 候选 A：参考 xiaoya-alist-search 的 TXT 解析 + 自建 Snapshot 生成

- 参考 Apache-2.0 的 TXT 路径解析逻辑（REFERENCE/OPTIONAL_COPY，以后再决定是否真搬）
- 自行解析被丢弃的元数据字段（title/douban_id/rating/poster）
- 生成含完整字段的 Snapshot（补充 is_dir/size/mtime 需从其他来源获取）
- 风险：缺 provider_object_id，稳定身份需自行设计

### 候选 B：借鉴 .scan.list.gz 思想，设计 index.snapshot 格式

- 从 xiaoya 的 index.zip 学到"预生成索引 + 分发 + 导入"思想
- 设计更完整的 index.snapshot 格式（含 is_dir/size/mtime/hash/metadata）
- 不复制任何代码，独立实现
- 优势：Snapshot 成为 Kernel 一等公民，符合蓝图第九节

### 候选 C：借鉴 Complete Gate + recycle_bin，设计安全删除模型

- 结合 Python 的 gap 检查 + Go 的回收站
- 设计：Missing → Removal Candidate → Validation（Complete Gate + Shrink Guard）→ Confirmed → recycle_bin
- 不复制代码，独立实现
- 符合蓝图第八节"删除必须极度保守"

### 候选 D：AList 搜索链路作为 Consumer，不进入 Kernel

- AList 的搜索/302/代理逻辑只借思想
- IndexCore 提供 Query API，AList 作为 Consumer 读取
- 不嵌入 AList 二进制（不链接其源码）
- 符合蓝图第四节"Layer 4: Consumers"

---

## 十二、调查覆盖的蓝图 12 个问题

| 蓝图问题 | 调查结论 |
|----------|----------|
| 1. index.zip 包含什么？ | 41 个 TXT，562k 行，路径#元数据格式，按媒体分类 |
| 2. 谁生成？ | 未找到生成器源码；已证实客户端消费预生成索引，不自行扫描 |
| 3. 完整索引如何更新？ | 全量替换（version.txt 判断是否下载新 zip） |
| 4. 客户端是否需要遍历 Provider？ | 不需要，索引完全预生成 |
| 5. x_search_nodes 真实语义？ | (parent, name) 路径索引，LIKE 子串搜索 |
| 6. OpenList 数据库索引如何维护？ | 未调查（Discovery 02 范围） |
| 7. rclone Provider metadata？ | 未调查（Discovery 02+ 范围） |
| 8. 哪些 Provider 有稳定 object ID？ | 小雅索引中无 stable object ID |
| 9. Snapshot 最少字段？ | 小雅缺 provider_object_id/is_dir/size/mtime/hash |
| 10. Stable Identity 第一版？ | 仅有豆瓣 ID 作为外部元数据，无资源级稳定 ID |
| 11. Canonical Inventory 持久化？ | 未调查（架构阶段决定） |
| 12. 还剩哪些需自己开发？ | 见候选方案 A-D |

**注意**：问题 6/7/11 超出 Discovery 01 范围，留给后续 Discovery 阶段。

---

## 十三、结论

Discovery 01 调查完成。四份 Worker 报告全部 PASS，交叉核验无冲突。

从客户端可观察到的模式是"预生成索引资产 + 客户端拉取导入"，值得借鉴。但数据格式缺关键字段（is_dir/size/mtime/hash/object_id），安全机制有缺口（Go 无 Complete Gate、Python 无回收站），且大部分组件许可证不允许直接复制代码。

xiaoya-alist-search（Apache-2.0）的 TXT 解析和 SQLite 批量导入逻辑已降级为 REFERENCE/OPTIONAL_COPY，以后再决定是否真搬。其余安全机制（原子写入、回收站、Complete Gate）值得借鉴但需独立实现。

最终架构决策由 Architect 决定。