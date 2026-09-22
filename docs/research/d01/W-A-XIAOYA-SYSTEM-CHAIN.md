# Worker A — 小雅整体索引链路调查

## STATUS

PASS

## SUMMARY

小雅是一套"预生成索引分发 + AList 统一挂载 + Emby 可视化"的家庭影视方案。真实资源来自阿里云盘/115/夸克/PikPak 的公开分享，由某套未公开的工具链预先整理成四类数据包（index.zip 搜索索引、update.zip AList 挂载配置 SQL、strm.zip strm 文件列表、tvbox.zip TVBox 配置）分发到 GitHub data 仓库。**已证实**：客户端 AList 容器拉取这些数据包并消费，不自行扫描 Provider。**未证实**：索引生成器的源码和生成位置未找到（推测在 xiaoyaliu/alist 闭源镜像内部或另一未公开仓库中）。客户端的 AList 容器（基于 xiaoyaliu/alist:hostmode 镜像）启动时从 data 仓库拉取这些数据包，用 update.sql 注册所有网盘分享挂载，用 index 文件提供搜索，对外暴露 HTTP/WebDAV/TVBox 三种访问方式。Emby 容器消费 AList 提供的媒体（通过 strm 或直链），metadata 容器负责下载 Emby 元数据包（config.mp4/all.mp4 等，从 AList 的 /d/元数据/ 路径拉取）并运行 solid.py 爬虫每日刷新。docker-xiaoya 是 Docker Compose 部署壳，xiaoya-alist 是菜单式安装脚本集合（all_in_one.sh），两者都不生成索引。

## SYSTEM_CHAIN

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ 服务端（推测：小雅官方工具链，源码未找到）                                  │
│                                                                             │
│  阿里云盘分享 + 115分享 + 夸克分享 + PikPak分享                            │
│         │                                                                   │
│         ▼                                                                   │
│  [预整理] → 生成 index/*.txt（搜索索引，路径#标题#豆瓣ID#评分#海报）        │
│           → 生成 update.sql（AList x_storages 表，含 share_id/folder_id）   │
│           → 生成 strm.txt（strm 路径#AList 直链 URL）                       │
│           → 生成 tvbox 配置（json/js/libs）                                 │
│         │                                                                   │
│         ▼                                                                   │
│  打包: index.zip | update.zip | strm.zip | tvbox.zip | version.txt          │
│         │                                                                   │
│         ▼                                                                   │
│  GitHub: xiaoyaliu00/data （代码引用源）                                    │
│          xiaoyaDev/data    （任务指定仓库，内容相同，无 LICENSE）           │
└─────────────────────────┬───────────────────────────────────────────────────┘
                          │ curl 拉取（service.sh，按 version.txt 判断更新）
                          ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 客户端 Docker 部署                                                          │
│                                                                             │
│  ┌───────────────────────────────────────────────────────────────────┐     │
│  │ alist 容器 (xiaoyaliu/alist:hostmode)                             │     │
│  │  端口: 5678(HTTP/AList) / 2345 / 2346                             │     │
│  │  - 启动时 download index/update/strm/tvbox zip → /www/data        │     │
│  │  - 加载 update.sql → 注册所有网盘分享为 AList storage             │     │
│  │  - 对外: /d/路径(直链302) /dav(WebDAV) /tvbox(TVBox订阅)          │     │
│  │  - 搜索: 基于 index.*.txt 预生成索引                              │     │
│  │  - 内置 nginx (emby.js 代理) + 5233 数据服务                      │     │
│  └──────────────┬────────────────────────────────────────────┬─────────┘     │
│                 │ 元数据: /d/元数据/*.mp4                    │ WebDAV/HTTP   │
│                 ▼                                        ▼                 │
│  ┌──────────────────────────┐              ┌───────────────────────────┐     │
│  │ metadata 容器             │              │ emby 容器                  │     │
│  │ (xiaoya-metadata)         │              │ (xiaoya-embyserver)        │     │
│  │ - 下载 config.mp4/all.mp4 │──媒体──────▶│ - 消费 /media/xiaoya       │     │
│  │   pikpak.mp4 (从 alist)   │  解压到      │ - 影视库 UI (端口6908)     │     │
│  │ - 解压到 /media/xiaoya    │  /media      │ - 通过 alist 访问真实文件  │     │
│  │ - solid.py 爬虫每日更新   │              └───────────────────────────┘     │
│  └──────────────────────────┘                                              │
│                                                                             │
│  部署壳: docker-xiaoya (docker-compose.yml + install.sh)                    │
│  安装脚本: xiaoya-alist (all_in_one.sh 菜单式安装)                          │
└─────────────────────────────────────────────────────────────────────────────┘
                          │
                          ▼
┌─────────────────────────────────────────────────────────────────────────────┐
│ 用户访问                                                                    │
│  - Emby 客户端 → emby:6908 → alist:5678/d/ → 网盘直链(302)                 │
│  - Infuse/WebDAV → alist:5678/dav → 网盘直链                               │
│  - TVBox → alist:5678/tvbox/my_ext.json                                    │
│  - 搜索 → alist search API (基于 index 预生成索引)                          │
└─────────────────────────────────────────────────────────────────────────────┘
```

## COMPONENT_TABLE

| 组件 | 仓库 | 类型 | 职责 | 许可证 |
|------|------|------|------|--------|
| docker-xiaoya | monlor/docker-xiaoya | 部署壳 | Docker Compose 一键部署 alist+metadata+emby，提供 install.sh/Dockerfile/docker-compose.yml | CC BY-NC 4.0 |
| xiaoya-alist 镜像 | monlor/docker-xiaoya (alist/) | Dockerfile+脚本 | 构建 AList 容器（基于 xiaoyaliu/alist:hostmode），生成配置、下载数据包、定时清理阿里云盘 | CC BY-NC 4.0 |
| xiaoya-metadata 镜像 | monlor/docker-xiaoya (metadata/) | Dockerfile+脚本 | 下载解压 Emby 元数据包(config.mp4/all.mp4)，运行 solid.py 爬虫 | CC BY-NC 4.0 |
| xiaoya-embyserver 镜像 | monlor/docker-xiaoya (emby/) | Dockerfile+脚本 | Emby 影视库服务，消费 AList 媒体 | CC BY-NC 4.0 |
| all_in_one.sh | xiaoyaDev/xiaoya-alist | 安装脚本 | 菜单式安装/更新/卸载所有小雅组件 | GPL-3.0 |
| main.sh | xiaoyaDev/xiaoya-alist | 引导脚本 | 拉取并执行 all_in_one.sh | GPL-3.0 |
| xiaoya_data_downloader.sh | xiaoyaDev/xiaoya-alist | 数据下载脚本(已弃用) | 从 xiaoyaliu00/data 拉取 index/update/strm zip | GPL-3.0 |
| glue_python | xiaoyaDev/xiaoya-alist | Python 工具集 | token 刷新/cookie 获取/封面/strm 助手等 | GPL-3.0 |
| cron 容器 | xiaoyaDev/xiaoya-alist (cron/) | Docker 镜像 | s6-overlay 定时任务容器 | GPL-3.0 |
| base 工具脚本 | xiaoyaDev/xiaoya-alist (base/) | Shell 脚本 | auto_symlink/casaos/onelist/portainer 等周边工具 | GPL-3.0 |
| xiaoyaliu/alist 镜像 | Docker Hub (上游) | Docker 镜像 | 小雅定制版 AList，内置 nginx/数据下载/搜索/TVBox | 未知(非本仓库) |
| ddsderek/xiaoya-emd 镜像 | Docker Hub (上游) | Docker 镜像 | 元数据爬虫容器(solid.py) | 未知(非本仓库) |
| ddsderek/xiaoya-glue 镜像 | Docker Hub (上游) | Docker 镜像 | Python glue 工具容器 | 未知(非本仓库) |
| ddsderek/xiaoya-proxy 镜像 | Docker Hub (上游) | Docker 镜像 | 代理容器 | 未知(非本仓库) |
| ddsderek/xiaoya-115cleaner 镜像 | Docker Hub (上游) | Docker 镜像 | 115 网盘清理容器 | 未知(非本仓库) |
| data: index.zip | xiaoyaDev/data | 数据包 | 搜索索引(按电影/动漫/纪录片等分类，路径#标题#豆瓣ID#评分#海报) | 无 LICENSE |
| data: update.zip | xiaoyaDev/data | 数据包 | AList x_storages 表 SQL dump + opentoken 刷新地址 | 无 LICENSE |
| data: strm.zip | xiaoyaDev/data | 数据包 | strm 文件列表(相对路径#AList直链URL)，192MB 解压 | 无 LICENSE |
| data: tvbox.zip | xiaoyaDev/data | 数据包 | TVBox 配置(json/js/libs，含 xiaoya_proxy.jar) | 无 LICENSE |
| data: *_share_list.txt | xiaoyaDev/data | 数据文件 | 115/pikpak/quark 分享列表(名称 分享ID 文件夹ID 提取码) | 无 LICENSE |
| auth.xiaoya.pro | 上游服务 | HTTP 服务 | 阿里云盘 open token 刷新服务 | 未知(非本仓库) |
| xiaoya_db (solid.py) | xiaoyaDev/xiaoya_db (上游) | Python 源码 | Emby 元数据爬虫 | 未知(非本仓库) |

## VERIFIED_FACTS

1. **真实资源来源为阿里云盘/115/夸克/PikPak 分享**：update.sql 中 x_storages 表使用 `AliyundriveShare`、`PikPakShare` driver（`/tmp/worker-a/data/update.zip` 解压 update.sql 第2-10行）；env 文件含 `ALIYUN_TOKEN`/`QUARK_COOKIE`/`PAN115_COOKIE`/`PIKPAK_USER`（`/tmp/worker-a/docker-xiaoya/env:3-21`）；data 仓库含 115share_list.txt/pikpakshare_list.txt/quarkshare_list.txt。

2. **docker-xiaoya 是部署壳**：README.md 第4-5行"小雅全家桶部署，使用 Docker Compose 一键部署 Alist + Emby"；提供 docker-compose.yml、install.sh、uninstall.sh、Dockerfile（`/tmp/worker-a/docker-xiaoya/README.md:4-5`）。

3. **xiaoya-alist 是菜单式安装脚本集合**：README.md 第24行"整合安装脚本，内置所有相关软件的安装"；main.sh 拉取 all_in_one.sh 执行（`/tmp/worker-a/xiaoya-alist/main.sh:50-68`）；all_in_one.sh 6567 行，包含安装/更新/卸载菜单（`/tmp/worker-a/xiaoya-alist/README.md:24`）。

4. **xiaoyaDev/data 是数据包分发仓库**：仓库内容为 index.zip/update.zip/strm.zip/tvbox.zip/version.txt + 三个 share_list.txt，无源码（`/tmp/worker-a/data/` 目录列表）。

5. **index.zip 是预生成搜索索引**：解压含 index.movie.txt/index.book.txt/index.daily.txt 等，格式为 `./路径#标题#豆瓣ID#评分#海报URL`（`/tmp/worker-a/data/index.zip` 解压 index.movie.txt 前10行）。

6. **update.zip 是 AList 挂载配置 SQL**：解压含 update.sql（x_storages 表 INSERT 语句，含 share_id/folder_id/refresh_token）和 opentoken_url.txt（`http://auth.xiaoya.pro/api/ali_open/refresh`）（`/tmp/worker-a/data/update.zip` 解压）。

7. **strm.zip 是 strm 文件列表**：解压含 strm.txt（192MB），格式为 `相对路径#http://xiaoya.host:5678/d/...?sign=SIGN_STR`（`/tmp/worker-a/data/strm.zip` 解压 strm.txt 前10行）。

8. **数据包由 alist 容器客户端拉取**：service.sh 中 base_urls 指向 `xiaoyaliu00/data`，下载 tvbox.zip/update.zip/index.zip/version.txt 到 `/www/data`（`/tmp/worker-a/docker-xiaoya/alist/service.sh:5-18,35-61`）。

9. **AList 镜像来自 xiaoyaliu/alist:hostmode**：Dockerfile FROM `xiaoyaliu/alist:hostmode`（`/tmp/worker-a/docker-xiaoya/alist/Dockerfile:5`）；all_in_one.sh 第1373行同（`/tmp/worker-a/xiaoya-alist/all_in_one.sh:1373`）。

10. **客户端无需自行全量扫描 Provider**：搜索索引由 index.zip 预生成，客户端只需下载解压（`/tmp/worker-a/docker-xiaoya/alist/service.sh:13-18`）。注意：index.zip 的**生成器源码未找到**，仅证实客户端消费预生成索引。

11. **Emby 元数据从 AList 的 /d/元数据/ 路径下载**：metadata/entrypoint.sh 中 `aria2c ... ${ALIST_ADDR}/d/元数据/${path}${file}`（`/tmp/worker-a/docker-xiaoya/metadata/entrypoint.sh:69`）。

12. **solid.py 爬虫来自 xiaoyaDev/xiaoya_db**：metadata/Dockerfile 安装 solid.py（`curl ... github.com/Rik-F5/xiaoya_db`）（`/tmp/worker-a/docker-xiaoya/metadata/Dockerfile:23-27`）；all_in_one.sh 第4345行引用 `https://github.com/xiaoyaDev/xiaoya_db`（`/tmp/worker-a/xiaoya-alist/all_in_one.sh:4345`）。

13. **docker-xiaoya 许可证为 CC BY-NC 4.0**：LICENSE 文件第1行"Creative Commons Attribution-NonCommercial 4.0 International"（`/tmp/worker-a/docker-xiaoya/LICENSE:1`）。

14. **xiaoya-alist 许可证为 GPL-3.0**：LICENSE 文件第1-2行"GNU GENERAL PUBLIC LICENSE, Version 3"（`/tmp/worker-a/xiaoya-alist/LICENSE:1-2`）。

15. **xiaoyaDev/data 无 LICENSE**：仓库根目录无 LICENSE 文件，仅一次 commit "add files"（`/tmp/worker-a/data/` 目录列表 + git log）。

16. **代码引用 xiaoyaliu00/data 而非 xiaoyaDev/data**：service.sh 和 xiaoya_data_downloader.sh 所有 base_url 均指向 `xiaoyaliu00/data`（`/tmp/worker-a/docker-xiaoya/alist/service.sh:6-10`）。

17. **WebDAV 默认凭据 guest/guest_Api789**：README.md 第63行"webdav http://ip:5678/dav guest/guest_Api789"（`/tmp/worker-a/docker-xiaoya/README.md:63`）。

18. **阿里云盘自动清理**：clear.sh 通过阿里云 API 删除转存文件，默认每10分钟（`/tmp/worker-a/docker-xiaoya/alist/clear.sh:151-183` + `start.sh:192-208`）。

## EVIDENCE

### E1: 数据包内容结构（data 仓库）
- `index.zip`（16.9MB）：28+ 个 index.*.txt 分类索引文件，如 index.movie.txt/index.book.txt/index.daily.txt/index.comics.txt 等
- `update.zip`（20KB）：update.sql（102KB，AList x_storages 表 dump）+ opentoken_url.txt（44字节，指向 auth.xiaoya.pro）
- `strm.zip`（11.1MB）：strm.txt（解压后192MB，每行一个 strm 文件及其 AList 直链）
- `tvbox.zip`（1.6MB）：tvbox/json/*.json + tvbox/js/*.js + tvbox/libs/*.js + xiaoya_proxy.jar
- `version.txt`：`0.57.27`
- 来源：`/tmp/worker-a/data/` + `unzip -l` 输出

### E2: 数据包下载链路（service.sh）
- 文件：`/tmp/worker-a/docker-xiaoya/alist/service.sh`
- base_urls（第5-11行）：5个镜像源，均指向 `xiaoyaliu00/data/main`
- files（第13-18行）：tvbox.zip, update.zip, index.zip, version.txt
- download_files（第35-61行）：对比 version.txt 决定是否下载
- 下载到 `$DATA_DIR=/www/data`

### E3: AList 挂载配置（update.sql）
- 文件：`/tmp/worker-a/data/update.zip` 解压 update.sql
- 内容：`CREATE TABLE x_storages ...` + 大量 `INSERT INTO x_storages VALUES(...)`
- driver 类型：`AliyundriveShare`、`PikPakShare`
- 每条记录含：mount_path, driver, refresh_token/share_id/share_pwd/root_folder_id
- 示例：`INSERT INTO x_storages VALUES(1,'/每日更新/PikPak',0,'PikPakShare',...)`

### E4: 搜索索引格式（index.movie.txt）
- 文件：`/tmp/worker-a/data/index.zip` 解压 index.movie.txt
- 格式：`./相对路径#标题#豆瓣ID#评分#豆瓣海报URL`
- 示例：`./动漫/A-Z/D/哆啦A梦/电影#哆啦A梦#1782004#9.6#https://img9.doubanio.com/...`

### E5: strm 文件格式（strm.txt）
- 文件：`/tmp/worker-a/data/strm.zip` 解压 strm.txt
- 格式：`相对路径#http://xiaoya.host:5678/d/网盘路径?sign=SIGN_STR`
- xiaoya.host 是 alist 容器的 hosts 别名（all_in_one.sh 第3211-3234行配置）

### E6: 客户端 alist 容器启动流程（start.sh）
- 文件：`/tmp/worker-a/docker-xiaoya/alist/start.sh`
- 生成配置文件（mytoken.txt/myopentoken.txt/temp_transfer_folder_id.txt 等）
- 设置 cron：自动清理阿里云盘 + 进程守护
- 设置 download_url.txt 指向 `http://127.0.0.1:5233/data`（内置数据服务）
- 启动 alist server

### E7: metadata 容器流程（entrypoint.sh）
- 文件：`/tmp/worker-a/docker-xiaoya/metadata/entrypoint.sh`
- 等待 alist 启动 → 下载 config.mp4/all.mp4/pikpak.mp4（从 `${ALIST_ADDR}/d/元数据/`）→ 7z 解压 → 可选 solid.py 定时爬虫

### E8: 许可证文件
- `/tmp/worker-a/docker-xiaoya/LICENSE`：CC BY-NC 4.0 全文（15行）
- `/tmp/worker-a/xiaoya-alist/LICENSE`：GPL-3.0 全文（标准 FSF 文本）
- `/tmp/worker-a/data/`：无 LICENSE 文件

### E9: all_in_one.sh 镜像引用
- alist：`xiaoyaliu/alist:hostmode` 或 `xiaoyaliu/alist:latest`（第1373/1376行）
- emd 爬虫：`ddsderek/xiaoya-emd`（第4236行）
- glue：`ddsderek/xiaoya-glue:python`（第472行）
- proxy：`ddsderek/xiaoya-proxy`（第5458行）
- 115cleaner：`ddsderek/xiaoya-115cleaner`（第5327行）
- 来源：`/tmp/worker-a/xiaoya-alist/all_in_one.sh` grep 结果

### E10: opentoken 刷新服务
- `opentoken_url.txt` 内容：`http://auth.xiaoya.pro/api/ali_open/refresh`
- all_in_one.sh 第421行：`curl -s "http://auth.xiaoya.pro/api/ali_open/refresh" ...`
- 说明：小雅官方提供阿里云盘 open token 刷新服务

## UNVERIFIED

1. **推测**：xiaoyaDev/data 与 xiaoyaliu00/data 内容完全相同。证据：两者都含 index.zip/update.zip/strm.zip/tvbox.zip/version.txt，但代码仅引用 xiaoyaliu00/data，未找到 xiaoyaDev/data 的引用。xiaoyaDev/data 可能是镜像/fork，但未证实。

2. **推测**：xiaoyaliu/alist:hostmode 镜像内置了 index.zip 解压和 AList search 索引加载逻辑。证据：service.sh 下载到 /www/data，但解压和加载逻辑不在本仓库脚本中，应在 xiaoyaliu/alist 镜像内部（本仓库不含该镜像源码）。

3. **推测**：index.zip 中的索引文件被 AList 的 search 功能直接使用。证据：文件名以 index. 开头且按分类组织，但未找到明确的加载代码。

4. **推测**：strm.zip 中的 strm.txt 用于生成本地 strm 文件供 Emby 挂载。证据：strm.txt 格式为"路径#URL"，但生成 strm 文件的具体脚本未在本仓库中找到（可能在 xiaoyaliu/alist 镜像内或 xiaoyahelper 工具中）。

5. **推测**：update.sql 在 alist 容器启动时自动导入 AList 数据库。证据：update.sql 是 AList x_storages 表 dump，但导入逻辑不在本仓库脚本中。

6. **推测**：xiaoyaliu/alist:hostmode 的 hostmode 指使用 host 网络模式。证据：all_in_one.sh 第1372-1374行 hostmode 与 --network=host 关联，但未从镜像源码证实。

## RISKS

1. **许可证风险（高）**：docker-xiaoya 为 CC BY-NC 4.0（禁止商业用途），xiaoyaDev/data 无 LICENSE（版权状态不明）。任何借鉴/复制需谨慎评估。

2. **上游依赖风险**：核心组件 xiaoyaliu/alist 镜像、auth.xiaoya.pro 服务、xiaoyaliu00/data 仓库均不在研究范围内，其可用性/稳定性不受控。

3. **凭证嵌入风险**：update.sql 中硬编码了 `refresh_token: "3f46710d73424aaaa18db8ce2e521fff"` 等凭证，share_list 文件含公开分享 ID。这些是小雅官方分享的公开凭证，但若上游失效则所有挂载失效。

4. **数据包版本耦合**：version.txt 0.57.27，数据包与 alist 镜像版本可能存在隐式耦合，未找到兼容性说明。

5. **非本仓库组件未知**：xiaoyaliu/alist、ddsderek/xiaoya-emd、xiaoya_db 等上游组件的许可证和行为未在本研究中证实。

## LICENSE

| 仓库 | 许可证 | 证据 |
|------|--------|------|
| monlor/docker-xiaoya | CC BY-NC 4.0（署名-非商业） | `/tmp/worker-a/docker-xiaoya/LICENSE:1` + README.md:275 |
| xiaoyaDev/xiaoya-alist | GPL-3.0（GNU 通用公共许可证 v3） | `/tmp/worker-a/xiaoya-alist/LICENSE:1-2` + README.md 许可证章节 |
| xiaoyaDev/data | 无 LICENSE | 仓库根目录无 LICENSE 文件，git log 仅一次 "add files" commit |

**使用限制说明**：
- docker-xiaoya 的 CC BY-NC 4.0 明确禁止商业用途，仅可分享和改编用于非商业目的
- xiaoya-alist 的 GPL-3.0 要求衍生作品同样以 GPL-3.0 开源
- xiaoyaDev/data 无许可证，默认版权保留（all rights reserved），技术上不可自由使用/复制

## UNRESOLVED

1. xiaoyaliu00/data 与 xiaoyaDev/data 的确切关系（fork？镜像？同一作者不同账号？）——未证实，记录待查。

2. xiaoyaliu/alist:hostmode 镜像内部如何消费 index.zip/update.zip/strm.zip——镜像源码不在研究范围，无法证实索引加载和 SQL 导入机制。

3. all_in_one.sh 中 base64 硬编码的 pan115share_list_base64/quarkshare_list_base64（第93-94行）与 data 仓库 share_list 文件的关系——是否同一份数据的两种形式？未证实。

4. data 仓库中 tvbox.zip 的 xiaoya_proxy.jar（181KB）来源与功能——未分析。

5. xiaoya-alist 仓库中 emby_lovechen/、aliyuntvtoken_connector/、115_cleaner/ 等子目录的具体作用——未深入分析（与核心索引链路关系较小）。

6. data 仓库 version.txt 0.57.27 与 alist 镜像版本的兼容性矩阵——未找到文档说明。

7. update.sql 中 refresh_token `3f46710d73424aaaa18db8ce2e521fff` 是小雅官方公开 token 还是个人凭证——推测为官方公开分享 token，但未证实。