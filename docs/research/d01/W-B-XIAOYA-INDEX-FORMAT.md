# Worker B — 小雅索引资产数据格式调查

## STATUS
PASS

## SUMMARY
小雅预生成索引资产以 `index.zip` 为核心，内含 41 个 UTF-8 编码的 TXT 文件（解压后 100MB / 562298 行），按媒体类型分文件存放（book/movie/tv/comics/music/video/115/pikpak/quark/docu/daily 等）。每行有两种形态：纯路径（`./a/b/c`，代表目录或无元数据文件）和带元数据路径（`./a/b/c#name#douban_id#rating#poster[#year#country#genres]`，用 `#` 分隔，5 或 8 字段）。官方 parser（xiaoya-alist-search.sh）只取每行 `#` 前的路径部分拆成 (parent, name) 存入 SQLite 的 `x_search_nodes(parent, name, is_dir, size)` 表，**丢弃所有元数据字段**，且 **is_dir 恒为 1、size 恒为 0**，不区分目录与文件，不记录 mtime/hash/object_id。`update.zip` 不是索引增量，而是 alist 容器的存储挂载配置（`x_storages` 表 SQL + opentoken_url）。索引中**不存在任何 stable object id / hash / provider id**，仅有豆瓣 ID 作为外部元数据。无法仅靠 parser 输出还原完整目录树（中间目录不入库），但可从路径前缀推导。

## INDEX_ZIP_STRUCTURE
`index.zip` 解压后为 **41 个扁平 TXT 文件**（无子目录），命名模式 `index.<category>[.sub].txt`：

| 文件 | 行数 | 说明 |
|------|------|------|
| index.115.txt | 220510 | 115 网盘分享，最大，含 8 字段元数据 |
| index.book.txt | 110168 | 电子书，纯路径 |
| index.music.txt | 105912 | 音乐，纯路径（含 .mp3 文件） |
| index.video.txt | 54582 | 每日更新视频，5/8 字段混合 |
| index.non.video.txt | 13697 | 非视频资源，纯路径 |
| index.pikpak.txt | 13216 | PikPak 分享，5 字段 |
| index.quark.txt | 7386 | 夸克分享，纯路径 |
| index.movie.txt | 6897 | 电影，5 字段 |
| index.tv.txt | 6061 | 电视剧，5 字段 |
| index.daily.txt | 4909 | 每日更新，8 字段为主 |
| index.comics.txt | 3041 | 动漫，5 字段 |
| index.docu.txt | 2460 | 纪录片 |
| 其余 29 个子分类 | 0~2228 | 如 index.movie.4kremux.txt / index.tv.china.txt / index.docu.bbc.txt 等 |
| index.docu.bilibili.txt | 0 | 空文件 |

**zip 体积**：压缩后 17MB，解压后 100MB（41 文件合计）。zip 内文件保留各自修改时间（最早 2023-08-30，最新 2026-09-22，说明是滚动更新）。

**其他 zip**：
- `strm.zip`（11MB）→ 单个 `strm.txt`（192MB 解压，706774 行），格式 `<相对路径>/<strm文件名>#<alist直链URL>`，用于 Emby/Jellyfin 的 strm 播放。
- `tvbox.zip`（1.6MB）→ tvbox 配置（json/js/jar），与索引无关。
- `update.zip`（20KB）→ `update.sql`（292 行）+ `opentoken_url.txt`，见 UPDATE_ZIP_RELATION。

## TXT_FORMAT
每行两种形态，编码 **UTF-8**（已用 python 逐行验证全部样本文件无解码错误），换行符 `\n`。

### 形态 A：纯路径（目录或无元数据文件）
```
./电子书/5000本（纯电子版非扫描）/VOL01-05
./🏷️我的115分享/4KRemux/Wild.Pacific.2015.DOCU.2160p.BluRay.REMUX.HEVC.DTS-HD.MA.5.1-FGT
```
- 以 `./` 开头，无 `#`。
- 可能是目录，也可能是文件（如 `.../Aaron Neville - Ave Maria.mp3`）——**索引本身不区分**。

### 形态 B：带元数据路径（5 或 8 字段，`#` 分隔）
```
./🏷️我的115分享/4KRemux/Alita.Battle.Angel.2019...FGT#阿丽塔：战斗天使#1652592#7.5#https://img9.doubanio.com/.../p2535922184.jpg#2019#美国/日本/加拿大#动作/科幻/冒险
```

#### 字段表（8 字段变体，5 字段变体只有前 5 列）

| 字段位置 | 字段名 | 类型 | 语义 | 样本值 |
|----------|--------|------|------|--------|
| 1 | path | string | 相对路径，以 `./` 开头，`/` 分隔层级 | `./🏷️我的115分享/4KRemux/Alita.Battle.Angel.2019...FGT` |
| 2 | title | string | 中文标题（豆瓣标题） | `阿丽塔：战斗天使` |
| 3 | douban_id | int(string) | 豆瓣影视 ID（非网盘 object id） | `1652592` |
| 4 | rating | float(string) | 豆瓣评分，可能为空 | `7.5` 或 `` |
| 5 | poster_url | string | 豆瓣海报 URL | `https://img9.doubanio.com/view/photo/s_ratio_poster/public/p2535922184.jpg` |
| 6 | year | int(string) | 上映年份（仅 8 字段变体有） | `2019` |
| 7 | country | string | 国家/地区，`/` 分隔多个 | `美国/日本/加拿大` |
| 8 | genres | string | 类型/流派，`/` 或 ` / ` 分隔多个 | `动作/科幻/冒险` 或 `喜剧 / 动作 / 动画 / 家庭` |

**字段数分布实测**：
- index.115.txt: 1 字段 63835 行（纯路径），8 字段 152719 行，少量 6/7/9/10
- index.movie.txt: 5 字段 6800 行（主流），少量 2/3/4/6
- index.tv.txt: 5 字段 6045 行
- index.daily.txt: 8 字段 4513 行，5 字段 278 行
- index.music.txt: 1 字段 105906 行（几乎全纯路径）

**注意**：`#` 分隔意味着路径中不能含 `#` 字符（否则 parser 会误切）。实测样本未发现路径含 `#` 的情况，但这是格式脆弱点。

## UPDATE_ZIP_RELATION
`update.zip` 解压含 2 个文件，**与 index.zip 是完全不同的东西**：

1. **update.sql**（292 行）：alist 容器 `x_storages` 表的 CREATE + INSERT 语句，定义网盘分享的挂载配置。每条记录含 `mount_path`、`driver`（AliyundriveShare / PikPakShare / 115Share 等）、`addition`（JSON，含 `refresh_token` / `share_id` / `share_pwd` / `root_folder_id`）。部分行以 `#` 开头表示注释（已禁用的挂载）。
   - 这是 **alist 存储配置**，不是索引数据的增量更新。
   - 其中 `root_folder_id` 是网盘分享的根文件夹 ID（如 `62b1abc24e83a8a32ec14bc28feeba60b5dce5b8`），但这是挂载点配置，不随索引条目下发。

2. **opentoken_url.txt**（44 字节）：`http://auth.xiaoya.pro/api/ali_open/refresh`，阿里云盘 open token 刷新地址。

**结论**：update.zip ≠ index.zip 的增量。index.zip 是全量索引快照，每次整体替换；update.zip 是 alist 容器初始化配置。二者无增量关系。

## DATA_SCALE
- **index.zip**：压缩后 17MB，解压后 100MB，41 个 TXT，**562298 行**（条目数）。
- **strm.zip**：压缩后 11MB，解压后 192MB，706774 行（strm 文件数，与索引条目非一一对应）。
- **update.zip**：20KB，292 行 SQL。
- **tvbox.zip**：1.6MB（配置，与索引无关）。
- **version.txt**：8 字节，内容 `0.57.27`。

### 100k / 1M 条目规模粗略估算
- 当前 562k 条目 → 解压后 100MB（平均 ~178 字节/行）。
- **100k 条目**：约 18MB 解压，3MB 压缩 zip。SQLite `x_search_nodes` 表约 5-8MB（含索引）。
- **1M 条目**：约 178MB 解压，30MB 压缩 zip。SQLite 约 50-80MB。
- parser 用 `BATCH_SIZE=1000` 批量插入，去重用 `DELETE ... WHERE rowid NOT IN (SELECT MIN(rowid) GROUP BY parent, name)`，1M 规模下去重可能较慢（全表扫描 + 子查询）。

## SNAPSHOT_FIELD_MAPPING
蓝图 SnapshotEntry 定义字段：`provider_object_id, parent_ref, path, name, is_dir, size, mtime, hash, metadata`

| SnapshotEntry 字段 | 小雅对应字段 | 有/缺 | 备注 |
|---------------------|--------------|-------|------|
| provider_object_id | 无 | 缺 | 索引中无网盘 object id；update.sql 的 root_folder_id 是挂载配置非条目级 |
| parent_ref | parent（parser 推导） | 有 | parser 用 `rfind('/')` 从路径拆出 parent，存为 `/前缀` |
| path | 字段1（去掉 `./`） | 有 | 完整相对路径，`/` 分隔 |
| name | parser 从路径末段拆出 | 有 | `path[last_slash+1:]`；注意：这是路径末段，**不是**字段2的中文标题 |
| is_dir | 无（parser 恒置 1） | 缺 | 索引不区分目录/文件；只能靠扩展名启发式猜测，不可靠 |
| size | 无（parser 恒置 0） | 缺 | 索引无文件大小 |
| mtime | 无 | 缺 | 索引无修改时间；zip 内文件有 mtime 但那是打包时间 |
| hash | 无 | 缺 | 索引无任何 hash（content hash / etag 均无） |
| metadata | 字段2-8（title/douban_id/rating/poster/year/country/genres） | 有但被 parser 丢弃 | TXT 中存在，但官方 parser 完全忽略；需自行解析 |

**结论**：小雅索引**不足以直接生成通用 Snapshot**。缺 `provider_object_id`、`is_dir`、`size`、`mtime`、`hash` 五个关键字段。`metadata` 在 TXT 中存在但需自行解析（官方 parser 丢弃）。`parent_ref`/`path`/`name` 可从路径推导。

## SAMPLE_DATA
### 样本 1：index.115.txt 8 字段行（带完整元数据）
```
./🏷️我的115分享/4KRemux/Alita.Battle.Angel.2019.2160p.BluRay.REMUX.HEVC.DTS-HD.MA.TrueHD.7.1.Atmos-FGT#阿丽塔：战斗天使#1652592#7.5#https://img9.doubanio.com/view/photo/s_ratio_poster/public/p2535922184.jpg#2019#美国/日本/加拿大#动作/科幻/冒险
```
拆解：path=`./🏷️我的115分享/4KRemux/Alita...FGT`, title=`阿丽塔：战斗天使`, douban_id=`1652592`, rating=`7.5`, poster=`https://...p2535922184.jpg`, year=`2019`, country=`美国/日本/加拿大`, genres=`动作/科幻/冒险`

### 样本 2：index.115.txt 纯路径行（目录或文件，无元数据）
```
./🏷️我的115分享/4KRemux/Wild.Pacific.2015.DOCU.2160p.BluRay.REMUX.HEVC.DTS-HD.MA.5.1-FGT
```

### 样本 3：index.movie.txt 5 字段行（无 year/country/genres）
```
./动漫/A-Z/D/哆啦A梦/电影#哆啦A梦#1782004#9.6#https://img9.doubanio.com/view/photo/s_ratio_poster/public/p2510688261.webp
```

### 样本 4：index.book.txt 纯路径行（目录）
```
./电子书/5000本（纯电子版非扫描）/VOL01-05/两京十五日
```

### 样本 5：index.music.txt 纯路径行（含文件扩展名）
```
./音乐/Classical Crossover/Ave Maria/Aaron Neville - Ave Maria.mp3
```

### 样本 6：strm.txt 行（strm 文件列表，非索引）
```
115/IMAX/Top Gun - Maverick - IMAX (2022)/{tmdb-361743}/Top Gun: Maverick.2022.strm#http://xiaoya.host:5678/d/🏷️我的115分享/4KRemux/Top.Gun.Maverick...mkv?sign=SIGN_STR
```
注意：strm.txt 路径中有 `{tmdb-361743}` 这样的 TMDB ID 标记，但这是 strm 文件名的一部分，不在 index.zip 索引中。

### 样本 7：update.sql 行（alist 存储挂载配置，非索引）
```sql
INSERT INTO x_storages VALUES(2,'/曲艺/戏曲（京，豫，吕，黄梅戏）剧',0,'AliyundriveShare',10,'work','{"refresh_token":"3f46710d...","share_id":"oxXY1wCyXE9","share_pwd":"","root_folder_id":"62b1abc2..."}',...);
```

## VERIFIED_FACTS
1. **index.zip 含 41 个 TXT 文件**。证据：`unzip -l index.zip` 输出 `41 files`。
2. **编码为 UTF-8**。证据：`file` 命令报 `UTF-8 text`；python 逐行读取全部样本文件无 UnicodeDecodeError。
3. **TXT 每行用 `#` 分隔字段，路径以 `./` 开头**。证据：index.115.txt/index.movie.txt 首行实测；parser 代码 `xiaoya-alist-search.sh:271` `parts = line.split('#')`。
4. **字段语义：path#title#douban_id#rating#poster[#year#country#genres]**。证据：index.115.txt 首行 8 字段拆解，douban_id=1652592 对应豆瓣电影《阿丽塔：战斗天使》；poster URL 域名 `doubanio.com`。
5. **parser 丢弃所有 `#` 后元数据，只取路径**。证据：`xiaoya-alist-search.sh:271-272` `parts = line.split('#'); path_with_file = parts[0].replace('./', '')`。
6. **parser 恒置 is_dir=1, size=0**。证据：`xiaoya-alist-search.sh:282` `data_batch.append((parent, name, 1, 0))`。
7. **SQLite schema 为 `x_search_nodes(parent TEXT, name TEXT, is_dir NUMERIC, size INTEGER)`**。证据：`xiaoya-alist-search.sh:233-240` CREATE TABLE 语句。
8. **去重按 (parent, name)**。证据：`xiaoya-alist-search.sh:307-314` `GROUP BY parent, name`。
9. **update.zip 是 alist 存储配置，非索引增量**。证据：`unzip -l update.zip` 含 `update.sql` + `opentoken_url.txt`；update.sql 首行 `CREATE TABLE x_storages`，全部是 `INSERT INTO x_storages`。
10. **索引中无 stable object id / hash**。证据：grep `[0-9a-f]{32,}` 无命中；第 3 字段为豆瓣 ID（短数字），非网盘 file_id；quark.txt 全纯路径无任何 ID。
11. **version.txt 内容为 0.57.27**。证据：`cat version.txt`。
12. **各 TXT 之间有数据重叠**。证据：index.daily.txt 第 1 行在 index.video.txt 中出现 1 次。
13. **parser 从 docker `/index/` 目录复制 TXT**。证据：`xiaoya-alist-search.sh:116` `docker cp "${DOCKER_NAME}:/index/."`。
14. **总条目数 562298**。证据：`cat index.*.txt | wc -l` = 562298。
15. **xiaoya-alist-search 许可证为 Apache 2.0**。证据：LICENSE 文件首行 `Apache License Version 2.0`。
16. **data 仓库无 LICENSE 文件**。证据：`ls /tmp/worker-b/data/` 无 LICENSE 相关文件。

## EVIDENCE
- **parser 源码**：`/tmp/worker-b/xiaoya-alist-search/xiaoya-alist-search.sh`
  - `process_file` 函数：行 261-293（主脚本）、行 395-427（更新脚本）
  - `create_table`（schema 定义）：行 228-241
  - `deduplicate_records`：行 302-316
  - `docker cp /index/.`：行 116、行 360
- **数据样本**：`/tmp/worker-b/extracted/index.*.txt`（从 index.zip 解压）
- **update.sql**：`/tmp/worker-b/extracted/update.sql`（从 update.zip 解压）
- **strm.txt**：`/tmp/worker-b/extracted/strm.txt`（从 strm.zip 解压）
- **version.txt**：`/tmp/worker-b/data/version.txt`
- **share_list 样本**：`/tmp/worker-b/data/115share_list.txt`（115 分享根挂载配置，非索引）
- **LICENSE**：`/tmp/worker-b/xiaoya-alist-search/LICENSE`（Apache 2.0）

## UNVERIFIED
1. **`/index/*.txt`（容器内）与 index.zip（GitHub）是否完全等价**：脚本从容器 `/index/` 复制，GitHub data 仓库应是其镜像，但未实际对比容器内文件（无运行中的小雅容器）。推测等价，但未证实。
2. **5 字段 vs 8 字段的选择规则**：index.movie/tv/comics 用 5 字段，index.115/daily/video 用 8 字段（混合）。推测是不同生成时间或不同网盘来源的差异，但未找到生成逻辑源码（data 仓库只有成品，无生成脚本）。
3. **index.docu.bilibili.txt 为何是空文件**：推测该来源已下线，但未证实。
4. **strm.txt 中的 `{tmdb-XXXX}` 标记是否可用于关联**：strm 路径含 TMDB ID，但 index.zip 中无 TMDB ID 字段（只有豆瓣 ID），二者关联关系未证实。
5. **data 仓库的更新频率**：zip 内文件 mtime 跨 2023-2026，version.txt=0.57.27，但更新机制（CI?手动?）未证实。

## RISKS
1. **无 stable object id 是最大风险**：索引条目只能靠路径标识，路径变更（重命名/移动）会导致条目"消失"和"出现"，无法做稳定 diff/增量。任何基于此索引的 Snapshot 方案必须自行生成 synthetic id（如 hash(path)）或依赖外部网盘 API 补充 object id。
2. **is_dir 恒为 1 误导**：官方 parser 把文件也标记为目录，任何依赖 is_dir 的逻辑（如目录树展开、size 聚合）都会出错。若要区分，需自行用扩展名启发式判断，但不可靠（如 `VOL01-05` 无扩展名但是目录，`Ave Maria.mp3` 有扩展名是文件）。
3. **size/mtime/hash 全缺**：无法做变更检测、去重（内容级）、完整性校验。只能做路径级 diff。
4. **`#` 分隔符脆弱**：若路径中含 `#`（虽然样本未发现），parser 会误切。自建 parser 应只 split 第一个 `#` 前为路径、其后为元数据，或用更robust的分隔策略。
5. **各 TXT 有数据重叠**：index.daily.txt 与 index.video.txt 有重复行。若合并所有 TXT 入库需去重（官方 parser 已做 `GROUP BY parent, name` 去重）。
6. **data 仓库无 LICENSE**：使用该数据资产存在法律风险，需自行确认数据来源的授权。xiaoya-alist-search 代码为 Apache 2.0，但 data 仓库本身无许可证声明。
7. **元数据字段被官方 parser 丢弃**：若要利用 title/douban_id/rating/poster/year/country/genres，需自行解析 TXT，不能依赖官方 parser 输出。
8. **更新机制不明**：index.zip 是全量快照替换，无增量机制。562k 条目全量重建的开销需评估。

## LICENSE
- **xiaoya-alist-search**：Apache License 2.0（LICENSE 文件明确声明）。
- **xiaoyaDev/data**：**无 LICENSE 文件**。仓库内只有数据资产（zip/txt）和 version.txt，无任何许可证声明。使用该数据资产的法律授权状态未知，需自行向仓库所有者确认。

## UNRESOLVED
1. 小雅容器内部如何生成 `/index/*.txt`？生成脚本/逻辑不在 data 仓库也不在 xiaoya-alist-search 仓库中（后者只消费索引，不生成索引）。生成逻辑可能在 alist 容器镜像内部（闭源）或另一未公开仓库。
2. index.pikpak.txt 的豆瓣 ID（如 35087769）如何与 PikPak 网盘 file_id 关联？索引中无 PikPak file_id，关联可能在容器运行时通过 alist API 动态建立。
3. strm.txt 中的 `?sign=SIGN_STR` 占位符如何替换为真实签名？推测容器运行时动态生成，但机制未证实。
4. 115share_list.txt / pikpakshare_list.txt / quarkshare_list.txt 的格式（`<目录> <share_id> <folder_id> <share_pwd>`）与 update.sql 中 x_storages 的关系——推测 share_list 是 update.sql 的简化文本版，但未证实。
5. index.zip 中 41 个 TXT 的命名分类体系是否有正式文档？目前只能从文件名推断分类（book/movie/tv/comics/music/docu/video/daily/non.video/115/pikpak/quark/reality），未找到分类标准文档。
6. version.txt 的版本号（0.57.27）与 alist 容器版本、索引格式的对应关系未证实。