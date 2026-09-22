# W-B — AList / OpenList External Provider API Research (D02)

> Question: If an external IndexCore Collector never touches the AList/OpenList
> internal database and only calls the public HTTP API, how many reliable
> resource facts can it obtain?

---

## 0. Source version record (hard rule)

| Item | AList | OpenList |
|---|---|---|
| Repository URL | https://github.com/alist-org/alist | https://github.com/OpenListTeam/OpenList |
| Branch | `main` | `main` |
| Commit SHA | `fb0731a6953012e7b72b89bf5473817caa4625f9` | `3a31b438a94af2532608499b74251c630ddf0f6f` |
| Commit date | 2026-09-19 | 2026-09-21 |
| Checked date | 2026-09-23 | 2026-09-23 |
| License | GNU AGPL v3 (`LICENSE`, "GNU AFFERO GENERAL PUBLIC LICENSE Version 3") | GNU AGPL v3 (`LICENSE`, identical header) |
| Language | Go (module `github.com/alist-org/alist/v3`) | Go (module `github.com/OpenListTeam/OpenList/v4`) |

Both are AGPL-3.0. 当前工程策略：不复制其源码、不链接其源码进入 Kernel、优先独立进程 /
public API 边界、实际采用前单独进行许可证审查。本报告不替代法务结论。

OpenList is a fork of AList (v4 module path vs v3). The FS read API shape
diverged: OpenList removed `id` / `path` / `virtual_path` from the public
object response (see Q1/Q2/Q5/Q19).

---

## 1. HTTP API Contract (verified from `server/router.go` + `server/handles/*.go`)

All FS read endpoints live under `{URL.Path}/api/fs` and require the standard
`Authorization` header (JWT token from `/api/auth/login`), except where noted.
Request binding is `c.ShouldBind(&req)` which accepts BOTH JSON body and
form/query params (`json:"..." form:"..."` tags).

### AList (`server/router.go:111` calls `_fs(auth.Group("/fs"))`)

| Endpoint | Method | Handler | Auth | Purpose |
|---|---|---|---|---|
| `/api/fs/list` | ANY | `FsList` | user | List directory |
| `/api/fs/get` | ANY | `FsGet` | user | Get single object + raw_url |
| `/api/fs/search` | ANY | `Search` | user + SearchIndex middleware | Search built index |
| `/api/fs/dirs` | ANY | `FsDirs` | user | List only subdirs (for tree UI) |
| `/api/fs/other` | ANY | `FsOther` | user | Driver-specific op |
| `/api/fs/link` | POST | `Link` | **admin** | Get direct link |
| `/api/fs/mkdir` `/rename` `/move` `/copy` `/remove` ... | POST | `fsmanage.go` | user | Write ops (out of scope for read-only collector) |
| `/d/*path` | GET/HEAD | `Down` | sign check | 302 redirect to raw link |
| `/p/*path` | GET/HEAD | `Proxy` | sign check | Proxy download |

### OpenList (`server/router.go:108-109`)

| Endpoint | Method | Handler | Auth | Notes |
|---|---|---|---|---|
| `/api/fs/list` | ANY | `FsListSplit` | Auth(true) (guest allowed if not disabled) | Dispatches to `FsList`; `/@s` prefix routes to `SharingList` |
| `/api/fs/get` | ANY | `FsGetSplit` | Auth(true) | Dispatches to `FsGet`; `/@s` prefix routes to `SharingGet` |
| `/api/fs/search` `/dirs` `/other` ... | ANY/POST | same as AList `_fs` | user | Inside `_fs(auth.Group("/fs"))` |
| `/api/fs/link` | POST | `Link` | admin | same |
| `/api/fs/get_direct_upload_info` | POST | `FsGetDirectUploadInfo` | user | OpenList-only |

### Request / Response field tables

#### `ListReq` (AList `fsread.go:22`, OpenList `fsread.go:22` — identical)

| Field | Type | Source | Semantics |
|---|---|---|---|
| `page` | int | `PageReq` | 1-based page; <1 normalized to 1 (AList) / 1 (OpenList Validate) |
| `per_page` | int | `PageReq` | AList: 0→200, <0→-1(all), >500→500. OpenList: <1→MaxInt(all) |
| `path` | string | form/json | Virtual mount path to list |
| `password` | string | form/json | Meta password for access |
| `refresh` | bool | form/json | **Bypass list cache** (requires write perm in AList) |

#### AList `FsListResp` (`fsread.go:52`)

| Field | Type | Semantics |
|---|---|---|
| `content` | []ObjLabelResp | Page slice of objects |
| `total` | int64 | Count after role filtering |
| `filtered_total` | int64 | Same as total in current code (line 147-148) |
| `page` | int | Effective page |
| `per_page` | int | Effective per_page |
| `has_more` | bool | `page*per_page < total` (false when per_page=-1) |
| `pages_total` | int | ceil(total/per_page) |
| `readme` `header` | string | Meta readme/header |
| `write` | bool | Can user write here |
| `provider` | string | `storage.GetStorage().Driver` (driver name, e.g. "Aliyundrive") |

#### OpenList `FsListResp` (`fsread.go:49`)

| Field | Type | Semantics |
|---|---|---|
| `content` | []ObjResp | **No id/path/virtual_path fields** |
| `total` | int64 | Count |
| `readme` `header` `write` `provider` | same | |
| `write_content_bypass` | bool | OpenList-only |
| `direct_upload_tools` | []string | OpenList-only |
| **Missing vs AList** | — | `filtered_total`, `page`, `per_page`, `has_more`, `pages_total` all removed |

#### AList object fields (`ObjLabelResp` `fsread.go:66`)

| Field | Type | Populated by | Stability |
|---|---|---|---|
| `id` | string | `obj.GetID()` | **Driver-dependent** (see Q5) |
| `path` | string | `obj.GetPath()` | **Driver-dependent**; local=abs fs path, webdav/s3="" |
| `virtual_path` | string | `Join(parent, name)` | Always present; the canonical virtual path |
| `name` | string | `obj.GetName()` | Always present |
| `size` | int64 | `obj.GetSize()` | Always present; 0 for dirs |
| `is_dir` | bool | `obj.IsDir()` | Always present |
| `modified` | time.Time | `obj.ModTime()` | Always present |
| `created` | time.Time | `obj.CreateTime()` | Falls back to ModTime if zero (`object.go:64`) |
| `sign` | string | `common.Sign(obj,parent,encrypt)` | Empty for dirs; HMAC of path for files when encrypted or SignAll |
| `thumb` | string | `model.GetThumb(obj)` | Only if obj implements `Thumb` |
| `type` | int | `utils.GetObjType(name,isDir)` | Derived from extension |
| `hashinfo` | string | `obj.GetHash().String()` | Only if driver sets hash |
| `hash_info` | map | `obj.GetHash().Export()` | Same |
| `label_list` | []Label | `op.GetLabelsByFileNamesPublic` | AList-only (labels) |
| `storage_class` | string | `model.GetStorageClass(obj)` | S3 etc. (e.g. STANDARD/GLACIER) |

#### OpenList object fields (`ObjResp` `fsread.go:35`)

Same as AList **minus** `id`, `path`, `virtual_path`, `label_list`, `storage_class`,
**plus** `mount_details` (StorageDetails, for direct upload). So OpenList exposes
strictly less identity information than AList.

#### `FsGetResp` (AList `fsread.go:346`, OpenList `fsread.go:255`)

Adds to ObjResp: `raw_url` (string, only for files), `readme`, `header`,
`provider`, `related` ([]Obj of same-level files sharing name prefix).
AList also has `web_proxy` (bool); OpenList does not.

---

## 2. Answers to the 20 questions

### Q1. list request/response complete fields
**FACT** (AList `fsread.go:22-64`): request = `{page, per_page, path, password, refresh}`;
response = `{content[], total, filtered_total, page, per_page, has_more, pages_total, readme, header, write, provider}`.
OpenList (`fsread.go:22-58`): same request; response drops `filtered_total/page/per_page/has_more/pages_total`.

### Q2. get request/response complete fields
**FACT** (AList `fsread.go:341-354`): request = `{path, password}`; response = ObjResp fields + `{raw_url, readme, header, provider, web_proxy, related[]}`.
OpenList (`fsread.go:250-262`): same request; response has no `web_proxy`, and ObjResp has no `id/path/virtual_path`.

### Q3. file vs directory distinction
**FACT** (`ObjLabelResp.IsDir` `fsread.go:72`, `obj.IsDir()`): the `is_dir` boolean is the sole discriminator. `type` int is derived from extension via `utils.GetObjType` and is NOT authoritative for dir detection. Size is 0 for dirs but 0 is also valid for empty files, so `is_dir` must be used.

### Q4. name/path/size/modified/created/hash/sign/raw_url/thumb semantics
**FACT** (sourced above):
- `name`: basename, always present.
- `path` (AList only): driver-internal path; **not** the virtual mount path. For local driver it is the **server-local absolute filesystem path** (`drivers/local/driver.go:193` `Path: filePath`), which leaks host paths. For webdav/s3 it is empty. **Do not treat `path` as a stable virtual identity.**
- `virtual_path` (AList only): `Join(parent, name)` — the canonical virtual path; stable across calls for the same mount.
- `size`: bytes; 0 for dirs.
- `modified`: mtime; always present.
- `created`: ctime/birthtime; **falls back to modified when zero** (`object.go:64-67`), so `created==modified` does not imply real creation time.
- `hashinfo`/`hash_info`: only when driver populates `HashInfo` (e.g. GoogleDrive md5 `types.go:51`, 115 sha1 `types.go:23`, Aliyundrive). Local/webdav/s3 = empty.
- `sign`: HMAC-SHA256 of the virtual path using the server `Token` as key (`sign/sign.go:40` `NewHMACSign`, `common/sign.go:16`). Empty for dirs; for files only when the path is encrypted (meta password) or `SignAll` setting is on. Expires per `LinkExpiration` setting. **It is a download capability token, not a content hash or identity.**
- `raw_url` (get only, files): either the driver's direct link (`model.GetUrl`), a `/d/` or `/p/` local endpoint, or a `down_proxy_url`. May be a per-request minted URL (e.g. BaiduYouth forces `/d/` `fsread.go:401-411`). **Not stable across calls for most cloud drivers.**
- `thumb`: only when obj implements `Thumb` (image/video drivers).

### Q5. Does the API expose the underlying provider object/file ID?
**FACT**: The AList `id` field is populated from `obj.GetID()` (`fsread.go:321`). Whether it is non-empty is **per-driver**:
- GoogleDrive: `ID: f.Id` (`drivers/google_drive/types.go:45`) — exposes Google file id.
- Aliyundrive: `ID: f.FileId` (`drivers/aliyundrive/types.go:37`) — exposes aliyun file_id.
- 115: `FileObj` embeds `driver.File` whose `GetID()` returns 115 file id (`drivers/115/driver.go:57` uses `dir.GetID()`).
- Local: **ID not set** (`drivers/local/driver.go:191-204` sets only Path) → `id` is `""`.
- WebDAV: **ID not set** (`drivers/webdav/driver.go:56-62`) → `id` is `""`.
- S3: **ID explicitly commented out** (`drivers/s3/util.go:111` `//Id: *object.Key`) → `id` is `""`.

**FACT**: OpenList **removed the `id` field entirely** from `ObjResp` (`fsread.go:35-47`). So on OpenList the public API never exposes the provider file id, regardless of driver.

The `model.Obj.GetID()` doc itself warns (`model/obj.go:36-37`): "The internal information of the driver. If you want to use it, please understand what it means."

### Q6. If no ID, is there another public field usable as stable identity?
**FACT**: The most stable public identity available on BOTH projects is the **virtual path** (`virtual_path` in AList; reconstructable as `Join(parent, name)` in OpenList). It is unique within a running instance for a given mount configuration.
- `name` alone is not unique (collisions across dirs).
- `size`+`name`+`modified` is a weak composite key but not guaranteed unique and `modified` can change.
- `hashinfo` when present is a strong content identity but is **unavailable for local/webdav/s3** and is content-addressed (changes on content change, not on rename).
**INFERENCE**: For a collector, the best available **matching key** is `(mount_path, virtual_path)` or, for OpenList, `(provider, parent, name)` reconstructed per call. This is a **path-based matching key**, not a stable resource identity — rename changes name, move changes parent. There is no guaranteed stable opaque id across all drivers via the public API.

### Q7. After rename/move on the same path, which fields stay invariant?
**FACT** (`op/fs.go:407-439` Rename, `364-405` Move): rename/move operate by `srcObj` resolved from current path, then call driver. Cache is updated by name (`updateCacheObj`/`delCacheObj` match by `GetName()`).
- `name` changes on rename (by definition).
- `virtual_path` changes (it is `Join(parent,name)`).
- `id` (when present): for cloud drivers the underlying file_id is **typically invariant** under rename/move (GoogleDrive patch changes name only `google_drive/driver.go:93-101`; Aliyundrive Move changes parent only). But this is provider behavior, not an API contract, and the API does not return the old+new id in one response.
- `size`, `hashinfo` (content) are invariant under rename/move.
- `modified`: may or may not change (provider-dependent; local `os.Rename` preserves mtime).
**INFERENCE**: No public API field is guaranteed invariant under rename/move across all drivers except content hash (when available). The virtual path is a **matching key** that the API itself mutates on rename/move; it is **not a stable resource identity**.

### Q8. Is the API paginated? How to judge pagination completeness?
**FACT** (AList `fsread.go:84-88, 253-299`): yes. `DefaultPerPage=200`, `MaxPerPage=500`, `AllPerPage=-1`. `has_more = page*per_page < total`. `pages_total = ceil(total/per_page)`. Response includes `total`, `page`, `per_page`, `has_more`, `pages_total` — sufficient to detect completeness.
**FACT** (OpenList `fsread.go:214-226` + `model/req.go:13-19`): `PerPage<1` → `MaxInt` (return all). Response has only `total`; **no `has_more`/`page`/`per_page`**. Completeness = `len(content) == total` or you received all in one call (default behavior).
**FACT**: Pagination is applied **after** role filtering (`fsread.go:139` `pagination(filtered, ...)`), so `total` is the filtered count, not the raw provider count. A collector cannot learn the unfiltered count.

### Q9. Can list return cache instead of live Provider?
**FACT** (`op/fs.go:111-170` `List`): yes, by default. If `!args.Refresh` and cache hit → return cached `[]model.Obj` (line 118-123). Cache TTL = `storage.CacheExpiration` minutes (default 30, `op/driver.go:78-82`). Cache key = `Join(MountPath, path)` (line 106-108). Empty result is **not cached** (line 162-165 del cache on empty).
So a normal `list` call returns cached data unless `refresh=true` is sent (and the caller has write permission, AList `fsread.go:118-121`).

### Q10. refresh / cache expiration vs API
**FACT**: `refresh=true` in `ListReq` skips the cache read and forces `storage.List` (provider call), then refreshes the cache (`op/fs.go:118` guard). In AList, refresh requires write permission (`fsread.go:118-121`); a read-only external collector account **cannot force refresh**. Cache expiration is a storage config field (`Storage.CacheExpiration`, `model/storage.go:12`), not exposed via the FS API response. There is no API field telling the client how stale the data is.

### Q11. How are Provider errors surfaced in the API?
**FACT** (`fsread.go:128-131`): `fs.List` error → `common.ErrorResp(c, err, 500)`. The error is wrapped (`errors.WithMessage(err, "failed get objs")`) and returned as `{code:500, message:"...", ...}`. Specific sentinel errors exist (`errs/errors.go`): `ObjectNotFound`, `StorageNotFound`, `NotFolder`, `NotFile`, `NotImplement`, `NotSupport`. `op/fs.go:112-113` returns "storage not init" if status != WORK.
**FACT**: The HTTP status code is coarse: 400 (bind), 403 (perm/password), 500 (most provider errors). Provider-specific error text is in the `message` field but is not structured for reliable programmatic distinction beyond the sentinel strings.

### Q12. partial failure / permission failure / non-existent dir distinction
**FACT**:
- Permission/password failure → HTTP 403 with "password is incorrect or you have no permission" (`fsread.go:114`, `377`).
- Non-existent storage (path not under any mount) → `op.GetStorageAndActualPath` error → 500 "failed get storage" (`fs/list.go:21`).
- Non-existent object → `errs.ObjectNotFound` → 500 "object not found" (`op/fs.go:237`). `FsGet` does not special-case 404; it returns 500.
- Not a folder (listing a file path) → `errs.NotFolder` → 500 (`op/fs.go:130`).
- Partial failure (some children unreadable): **not distinguishable** — `storage.List` returns a single error for the whole directory; there is no per-child error field. If the driver returns partial results with nil error, the API returns them as if complete.
**INFERENCE**: A collector cannot reliably distinguish "dir does not exist" from "provider error" from "path is a file" using HTTP status alone; it must parse the `message` string for sentinels like "object not found" / "not a folder", which is fragile.

### Q13. multi storage / root unique identification
**FACT** (`op/path.go` / `op.GetStorageAndActualPath`): the virtual path prefix maps 1:1 to a mounted storage (`Storage.MountPath` is `gorm:"unique"` `model/storage.go:9`). The `provider` field in list/get response is `storage.GetStorage().Driver` (the driver name, e.g. "Aliyundrive"). Two storages with the same driver but different `MountPath` are distinguished by the path prefix, not by any field in the object response. The `Storage.ID` (uint primary key) is **not** exposed in FS read responses.

### Q14. Can two storages of the same type be reliably distinguished?
**FACT**: Only by their mount path prefix (the `path`/`virtual_path` you queried). The `provider` field is the driver name and is identical for same-type storages. There is no `storage_id` in `ObjResp`. A collector must track `(mount_path_prefix → provider)` itself by observing which prefix each list call used.

### Q15. Can an external program fully recursively traverse via API alone?
**FACT**: Yes, in principle: start at `/`, call `list`, for each entry with `is_dir=true` recurse into `Join(parent, name)`. This is exactly what `FsDirs` (`fsread.go:159`) supports for tree UI.
**RISKS**:
- Cache staleness (Q9/Q10): traversal may see a snapshot, not live state.
- Pagination (Q8): must follow `has_more`/`total` (AList) or fetch all (OpenList default).
- Permission: a child dir may be listable but its children filtered by role (`fsread.go:132-138`); the collector sees a subset with no marker that filtering occurred.
- No bulk/parallel primitive: one HTTP call per directory. Deep trees are slow.
- `total` is post-filter, so the collector cannot detect that children were hidden.
**INFERENCE**: Full traversal is possible but the result is "everything the current token is allowed to see at this instant per storage cache state", not a guaranteed-complete provider view.

### Q16. Is there a public delta / change API?
**FACT**: No. Searched `server/router.go` and `server/handles/` for `delta`/`changes`/`watch`/`notify` (excluding task notify) — no matches. The only change-adjacent surface is the admin-only `/api/admin/index/build|update|stop|clear|progress` (`router.go:197-202`) which rebuilds the **local search index**, not a provider delta stream. `/api/fs/search` queries that local index (`SearchNode` has only Parent/Name/IsDir/Size, `model/search.go:23-28`), not live changes.

### Q17. Is there a webhook / change notification?
**FACT**: No webhook registration endpoint exists in `router.go`. There is an internal `HandleObjsUpdateHook` (`op/fs.go:147-150`) fired after a fresh `storage.List`, but it is an in-process Go hook for index update, not an external HTTP callback. No SSE/WebSocket for FS changes (the SSE channel is for task events only).

### Q18. Are raw URL / 302 decoupled from index identity?
**FACT**: Yes. `raw_url` is computed per-request in `FsGet` (`fsread.go:385-443`): it may be a driver-minted expiring URL (S3 presign `s3/driver.go:133`, cloud download links), a `/d/` or `/p/` local proxy, or a `down_proxy_url`. The `/d/*path` 302 (`down.go:99-123`) redirects to `link.URL` which is re-fetched via `fs.Link` with its own `linkCache` (`op/fs.go:248-288`) keyed by path with per-link expiration.
**INFERENCE**: `raw_url` is a download capability, not an identity. The same file at the same virtual path can return different `raw_url` across calls (URL expiry, IP-pinned links `op/fs.go:274`). A collector must not use `raw_url` as a stable key; it must use the virtual path (and `id` when present and known stable for that driver).

### Q19. Are AList and OpenList APIs compatible?
**FACT**: Not fully. Divergences verified by `diff`:
- OpenList `ObjResp` **removed** `id`, `path`, `virtual_path` (and `label_list`, `storage_class`).
- OpenList `FsListResp` **removed** `filtered_total`, `page`, `per_page`, `has_more`, `pages_total`; added `write_content_bypass`, `direct_upload_tools`.
- OpenList `FsGetResp` removed `web_proxy`; added `mount_details`.
- OpenList default pagination returns ALL (`PerPage<1→MaxInt`); AList defaults 200.
- OpenList `/api/fs/list` and `/get` are split handlers (`FsListSplit`/`FsGetSplit`) that intercept `/@s` for shares; AList uses direct handlers.
- OpenList added `middlewares.PathParse` on `/d` `/p` `/ad` routes; AList does not.
- OpenList module path is `v4`, AList `v3`.
- Core read field names that remain (`name`,`size`,`is_dir`,`modified`,`created`,`sign`,`thumb`,`type`,`hashinfo`,`hash_info`,`raw_url`,`provider`) are compatible.
**INFERENCE**: A collector targeting both must avoid relying on `id`/`path`/`virtual_path`/`has_more` and use `name`+reconstructed path + `total` only. Treat them as a common subset API.

### Q20. SnapshotEntry field mapping (DIRECT / DERIVABLE / DRIVER_DEPENDENT / UNAVAILABLE)

Assume a SnapshotEntry needs: `id`, `path`, `name`, `size`, `is_dir`, `modified`, `created`, `hash`, `thumb`, `raw_url`, `storage_id`, `provider`.

| SnapshotEntry field | AList | OpenList | Classification |
|---|---|---|---|
| `name` | `content[].name` | `content[].name` | **DIRECT** |
| `size` | `content[].size` | `content[].size` | **DIRECT** |
| `is_dir` | `content[].is_dir` | `content[].is_dir` | **DIRECT** |
| `modified` | `content[].modified` | `content[].modified` | **DIRECT** |
| `created` | `content[].created` (may equal modified) | same | **DIRECT** (but semantics weak — fallback to modified) |
| `type` | `content[].type` | `content[].type` | **DERIVABLE** (from name extension) |
| `provider` | `FsListResp.provider` | same | **DIRECT** (driver name, not storage instance id) |
| `path` (virtual) | `content[].virtual_path` | reconstruct `Join(req.path, name)` | **DIRECT** (AList) / **DERIVABLE** (OpenList) |
| `path` (driver-internal) | `content[].path` | absent | **DRIVER_DEPENDENT** (AList only; local leaks host path, others empty) |
| `id` (provider file id) | `content[].id` | absent | **DRIVER_DEPENDENT** (AList only; non-empty only for cloud drivers) |
| `hash` | `content[].hashinfo` | same | **DRIVER_DEPENDENT** (only some drivers populate) |
| `thumb` | `content[].thumb` | same | **DRIVER_DEPENDENT** (only Thumb-implementing drivers) |
| `storage_class` | `content[].storage_class` | absent | **DRIVER_DEPENDENT** (AList only; S3 etc.) |
| `raw_url` | `FsGet.raw_url` only | same | **DERIVABLE** via extra `get` call; **not stable** (per-request, expiring) |
| `sign` | `content[].sign` | same | **DERIVABLE** but is a download token, not identity; empty for dirs |
| `storage_id` (instance) | not in FS response | not in FS response | **UNAVAILABLE** via FS API (only admin `/api/admin/storage/list`) |
| `created` (real birthtime) | may fallback to modified | same | **DRIVER_DEPENDENT** (local has it; many cloud drivers don't) |

---

## 3. Driver capability diff (verified from `drivers/*/driver.go` + `types.go`)

| Driver | Sets `ID`? | Sets `Path`? | `Hash` | `Thumb` | `StorageClass` | Identity model |
|---|---|---|---|---|---|---|
| **Local** (`drivers/local/driver.go:191`) | No | Yes (abs fs path) | No | Yes (image/video, generated) | No | path-based |
| **WebDAV** (`drivers/webdav/driver.go:56`) | No | No | No | No | No | path-based |
| **S3** (`drivers/s3/util.go:110,126`) | No (commented out) | No | No | No | Yes (STANDARD/GLACIER) | path-based (key) |
| **GoogleDrive** (`types.go:45`) | Yes (`f.Id`) | No | Yes (md5) | Yes (ThumbnailLink) | No | id-based |
| **Aliyundrive** (`types.go:37`) | Yes (`f.FileId`) | No | Yes (sha1) | Yes | No | id-based |
| **115** (`types.go:13`, `driver.go:57`) | Yes (via `driver.File`) | No | Yes (sha1) | Yes (ThumbURL) | No | id-based |

**Key insight**: drivers split into **path-based** (local/webdav/s3) where the provider has no stable file id and AList exposes none, and **id-based** (cloud drives) where the provider has a file_id and AList exposes it via `id` (but OpenList hides it). A collector cannot assume `id` is present.

---

## 4. cache / error / pagination risks

### Cache
- Default-served data is **cache, not live** (Q9). TTL default 30 min, configurable per storage, **not exposed in response**.
- A read-only token **cannot force refresh** in AList (needs write perm, `fsread.go:118-121`).
- Empty directories are **not cached** (`op/fs.go:162-165`), so an empty result might be a cache miss or a genuinely empty dir — indistinguishable.
- `linkCache` separately caches download links with per-link expiry (`op/fs.go:248-288`); `raw_url` from `get` may thus be a cached link near expiry.

### Error
- HTTP 500 covers provider errors, not-found, not-a-folder, not-implemented (Q11/Q12). Sentinels are in `message` text only.
- No per-child error in `list`; partial provider failure is invisible (Q12).
- `storage not init` (status != WORK) returns 500 (`op/fs.go:112`).

### Pagination
- AList: bounded (max 500/page); use `has_more`/`pages_total` to complete.
- OpenList: defaults to returning all in one call (`PerPage<1→MaxInt`); large dirs → large single response, no server-side cap observed in `fsread.go:214-226`.
- `total` is post-role-filter in both; hidden children are not countable (Q15).

---

## 5. VERIFIED_FACTS vs UNVERIFIED

### VERIFIED_FACTS (with source evidence)
1. **F1**: AList `FsListResp` includes `id`/`path`/`virtual_path`; OpenList `ObjResp` does not. — `alist/server/handles/fsread.go:66-82` vs `openlist/server/handles/fsread.go:35-47`.
2. **F2**: `id` is `obj.GetID()` and is empty for local/webdav/s3 (S3 explicitly commented out). — `drivers/local/driver.go:191`, `drivers/webdav/driver.go:56`, `drivers/s3/util.go:111,127,166,182`.
3. **F3**: `id` is populated for GoogleDrive/Aliyundrive/115. — `drivers/google_drive/types.go:45`, `drivers/aliyundrive/types.go:37`, `drivers/115/driver.go:57`.
4. **F4**: `list` returns cache unless `refresh=true`; refresh requires write perm in AList. — `op/fs.go:118-123`, `fsread.go:118-121`.
5. **F5**: Cache TTL = `CacheExpiration` min (default 30), not in response. — `op/driver.go:78-82`, `op/fs.go:161`.
6. **F6**: No delta/webhook/change API exists. — `server/router.go` exhaustive route list; grep found no `delta`/`webhook`/`watch` routes.
7. **F7**: `sign` is HMAC of virtual path with server Token, empty for dirs, for files only when encrypted/SignAll. — `server/common/sign.go:12-17`, `internal/sign/sign.go:15-41`.
8. **F8**: `created` falls back to `modified` when zero. — `internal/model/object.go:64-67`.
9. **F9**: `total` is post-role-filter count. — `fsread.go:132-148` (filter then paginate).
10. **F10**: OpenList default pagination returns all (`PerPage<1→MaxInt`); AList defaults 200, max 500. — `openlist/internal/model/req.go:13-19`, `alist/server/handles/fsread.go:84-88`.
11. **F11**: Write ops (`MoveCopyReq`) identify objects by name, not id. — `fsmanage.go:66-74`.
12. **F12**: `FsGet` returns 500 (not 404) for not-found. — `fsread.go:380-384` calls `fs.Get` then `common.ErrorResp(c, err, 500)`.
13. **F13**: `search` queries a local built index (`SearchNode` = Parent/Name/IsDir/Size only), not live provider. — `internal/model/search.go:23-28`, `server/handles/search.go:50`.
14. **F14**: Both repos are AGPL-3.0. — `LICENSE` headers.

### UNVERIFIED / INFERENCE (clearly labeled, not to be treated as fact)
- **I1**: Provider file_id is invariant under rename/move for cloud drivers. Supported by GoogleDrive/Aliyundrive driver code (rename patches name only) but **not an API contract** and not testable via API alone without a live instance.
- **I2**: No upper bound on OpenList single-call response size beyond process memory. Inferred from `MaxInt` default; not load-tested.
- **I3**: `raw_url` for a given path changes across calls. Strongly supported by per-request presigning code but not exhaustively verified for every driver.
- **I4**: `path` field for local driver leaks server host absolute path. Verified in source (`drivers/local/driver.go:193`) but whether a given deployment exposes local driver to external collectors is a deployment config, not a code fact.

---

## 6. Bottom line for an external Collector

An external IndexCore Collector using only the public HTTP API can reliably obtain:
- **DIRECT (always, both projects)**: `name`, `size`, `is_dir`, `modified`, `provider` (driver name), and the virtual path (explicit in AList, reconstructed in OpenList).
- **DIRECT but weak**: `created` (may equal modified).
- **DRIVER_DEPENDENT (must probe per storage)**: `id` (AList only, cloud drivers only), `hash`, `thumb`, `storage_class`, real `created`.
- **DERIVABLE with extra call**: `raw_url` (via `get`, but unstable/expiring — not an identity).
- **UNAVAILABLE**: `storage_id` (instance), live delta/changes, webhook notifications, cache staleness indicator, unfiltered child count, per-child error.

The collector **cannot** assume `id` is present or stable across all storages, **cannot** detect cache staleness, **cannot** get a change stream, and **cannot** distinguish not-found from provider-error via HTTP status alone. The strongest cross-project **matching key** is the **virtual path** (plus `hash` when present as a content check), but this is a path-based matching key, not a stable resource identity (rename/move changes path). OpenList exposes strictly less than AList (no `id`/`path`/`virtual_path`), so a portable collector should target the OpenList subset.
