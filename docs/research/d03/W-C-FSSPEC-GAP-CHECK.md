# D03 — fsspec Gap Check (Worker C)

> Research report. No architecture decision is made here. Every key
> conclusion is tagged `FACT` (backed by cited source) or `INFERENCE`
> (reasoned from facts but not directly asserted by source). Single
> backend capabilities are never generalized to all backends.

## Source Version Record (hard rule)

| Field | Value |
| --- | --- |
| Repository URL | https://github.com/fsspec/filesystem_spec |
| Branch | `main` |
| Commit SHA | `751721f96c04fb53aa3b945ab1b0ff53781e79c0` |
| Commit subject | `Make whole-file cache writes atomic (#2075)` |
| Commit date | 2026-09-22T12:14:56-04:00 |
| Checked date | 2026-09-23 |
| License file | `LICENSE` (BSD 3-Clause, Copyright (c) 2018, Martin Durant) |
| License | BSD-3-Clause |
| Core spec file | `fsspec/spec.py` (2353 lines) |
| Async base | `fsspec/asyn.py` (1277 lines) |

All file references below use the form `path:line` against the
commit SHA above. Paths are relative to the repository root
(`/tmp/d03/fsspec` at investigation time).

### Scope note on "implementations"

**FACT** fsspec registers cloud backends by import path only; their
source lives in separate packages, not in this repository
(`fsspec/registry.py:62-241`):

| Protocol | External class | Package |
| --- | --- | --- |
| `s3`, `s3a` | `s3fs.S3FileSystem` | s3fs |
| `gcs`, `gs` | `gcsfs.GCSFileSystem` | gcsfs |
| `abfs`, `az` | `adlfs.AzureBlobFileSystem` | adlfs |
| `gdrive` | `gdrive_fsspec.GoogleDriveFileSystem` | gdrive_fsspec |
| `dropbox` | `dropboxdrivefs.DropboxDriveFileSystem` | dropboxdrivefs |
| `webdav` | `webdav4.fsspec.WebdavFileSystem` | webdav4 |
| `oss` | `ossfs.OSSFileSystem` | ossfs |
| `tos`, `tosfs` | `tosfs.TosFileSystem` | tosfs |
| `oci`, `ocilake` | `ocifs.OCIFileSystem` | ocifs |

**FACT** The following implementations are bundled inside this
repository and could be read directly: `local`, `memory`, `http`,
`http_sync`, `ftp`, `sftp`, `webhdfs`, `smb`, `dbfs`, `arrow`
(HadoopFileSystem), `git`, `github`, `gist`, `jupyter`, `zip`, `tar`,
`libarchive`, `data`, `reference`, `cached`, `dirfs`, `dask`,
`asyn_wrapper`, `generic`.

Any statement below about s3/gcs/azure/gdrive/dropbox/webdav
`info()` fields is explicitly labeled `INFERENCE` because their source
was not inspected in this task.

---

## 1. AbstractFileSystem core interface (`ls`/`info`/`walk`/`find`)

**FACT** The abstract base is `AbstractFileSystem` at
`fsspec/spec.py:151`, using metaclass `_Cached` (`fsspec/spec.py:52`).

**FACT** `ls` signature and contract (`fsspec/spec.py:377-416`):
```
def ls(self, path, detail=True, **kwargs)
```
- `detail=True`  -> list of dicts, "each is the same as the result of
  `info(path)`" (`spec.py:405-407`).
- `detail=False` -> list of path strings (`spec.py:413-414`).
- The docstring mandates only three keys: full path (without protocol),
  `size` in bytes (or `None`), and `type` ("file"/"directory"/other)
  (`spec.py:386-391`). "Additional information may be present,
  appropriate to the file-system, e.g., generation, checksum, etc."
  (`spec.py:393-395`).
- The base method `raise NotImplementedError` (`spec.py:416`); every
  backend must override.

**FACT** `info` signature and contract (`fsspec/spec.py:732-764`):
```
def info(self, path, **kwargs)
```
- Returns a single dict "with exactly the same information as `ls`
  would with `detail=True`" (`spec.py:735-736`).
- Documented keys: `name` (full path in the FS), `size` (bytes, may be
  `None`), `type` ("file"/"directory"/other), "and other FS-specific
  keys" (`spec.py:746-747`).
- Default implementation calls `ls(parent, detail=True)` and filters
  (`spec.py:749-764`); may be overridden by a shortcut.

**FACT** `walk` signature (`fsspec/spec.py:443-475`):
```
def walk(self, path, maxdepth=None, topdown=True, on_error="omit", **kwargs)
```
- Iterator-style, yields `(path, dirs, files)` like `os.walk()`
  (`spec.py:446-447`).
- `on_error` accepts `"omit"` (default), `"raise"`, or a callable
  receiving an `OSError` (`spec.py:470-473`).
- `detail` is popped from kwargs (default `False`); when `False`,
  `dirs`/`files` are lists of names; when `True`, they are dicts
  name->info (`spec.py:484`, `509-511`).

**FACT** `find` signature (`fsspec/spec.py:537-573`):
```
def find(self, path, maxdepth=None, withdirs=False, detail=False, **kwargs)
```
- `detail=False` -> sorted list of path strings (`spec.py:569-571`).
- `detail=True`  -> `{name: info_dict}` (`spec.py:572-573`).
- Implemented on top of `walk(..., detail=True)` (`spec.py:561-564`).

**FACT** `glob` (`spec.py:609`), `exists` (`spec.py:718`), `isdir`
(`spec.py:787`), `isfile` (`spec.py:794`) are all derived from
`info`/`ls`.

---

## 2. Unified `id` / `etag` / `version` in `info()`?

**FACT** The `info()` contract names only `name`, `size`, `type` as
required keys; everything else is "FS-specific keys"
(`fsspec/spec.py:746-747`). There is no abstract field named `id`,
`etag`, `version`, or `generation` defined on `AbstractFileSystem`.

**FACT** `AbstractFileSystem.fsid` is a property that `raise
NotImplementedError` by default (`fsspec/spec.py:222-227`); it is a
*filesystem-level* id ("Persistent filesystem id that can be used to
compare filesystems across sessions"), not a per-object id.

**FACT** Per-backend `fsid` values observed are constant strings, not
per-object: `LocalFileSystem.fsid == "local"` (`implementations/local.py:40-41`),
`HTTPFileSystem.fsid == "http"` (`implementations/http.py:118-119`),
`WebHDFS.fsid == self._fsid` (`implementations/webhdfs.py:148-149`).

**FACT** The `info()` dicts produced by bundled backends carry
different, non-uniform extra keys (see Section 3). No key is present
in all of them besides `name`/`size`/`type`.

**INFERENCE** fsspec does not provide a cross-backend stable per-object
identity field. `name` (the path) is the only universally present
identifier, and it is location-bound, not content-bound. `fsid`
identifies the filesystem class/instance, not objects.

---

## 3. `info()` fields per implementation (bundled backends)

### local (`implementations/local.py:79-128`)

**FACT** `LocalFileSystem.info` returns (`local.py:117-128`):
`name`, `size`, `type`, `created`, `islink`, plus `mode`, `uid`,
`gid`, `mtime`, `ino`, `nlink` (`local.py:124-125`), and `destination`
for symlinks (`local.py:126-127`). `ino` (inode) is a host-local
native id, not portable across machines.

### memory (`implementations/memory.py:262-282`)

**FACT** For a directory: `{name, size:0, type:"directory"}`
(`memory.py:268-272`). For a file: `{name, size, type:"file",
created}` (`memory.py:275-280`). No id/etag/version.

### http (`implementations/http.py:431-477`, `_file_info` at `:844-892`)

**FACT** `_file_info` populates `size` from `Content-Length`/
`Content-Range` (`http.py:867-876`), `mimetype` (`:878-879`),
`partial` (`:881-884`), `url` (`:886`), and copies any of the headers
`ETag`, `Content-MD5`, `Digest`, `Last-Modified` into the info dict
verbatim as top-level keys (`http.py:888-890`).

**FACT** `HTTPFileSystem.ls` (derived from `_ls_real`) returns only
`{name, size:None, type}` (`http.py:206-213`) — it does not call
`_file_info`, so ETag/Last-Modified are available via `info()` but
not via `ls(detail=True)` for HTTP.

**FACT** `HTTPFileSystem.ukey` returns `tokenize(url, self.kwargs,
self.protocol)` — it assumes HTTP files are static and keys on the URL,
not on server-returned ETag (`http.py:427-429`).

### ftp (`implementations/ftp.py:147-195`)

**FACT** `ls` uses `MLSD` and passes through the server-returned
`details` dict, normalizing `type` ("dir"->"directory") and `size`
to int (`ftp.py:153-170`). Fields are therefore server-dependent
(MLST/MLSD facts: `modify`, `perm`, `size`, `type`, `unique` per
RFC 3659, but fsspec does not enumerate or rename them beyond
`type`/`size`/`name`).

### sftp (`implementations/sftp.py:145-201`)

**FACT** `_decode_stat` returns `name`, `size`, `type`, `uid`, `gid`,
`time` (atime), `mtime` (`sftp.py:176-188`). No etag/version.

### smb (`implementations/smb.py:247-265`)

**FACT** `info` returns `name`, `size`, `type`, `uid`, `gid`, `time`
(atime), `mtime` (`smb.py:256-264`). No etag/version.

### webhdfs (`implementations/webhdfs.py:266-298`)

**FACT** `info` returns the raw HDFS `FileStatus` JSON plus `name`
(`webhdfs.py:266-270`). `ls` returns `FileStatuses` entries
(`webhdfs.py:289-298`). HDFS `FileStatus` fields include
`pathSuffix`, `type`, `length`, `owner`, `group`, `permission`,
`modificationTime`, `accessTime`, `childrenNum`, `fileId`,
`storagePolicy` (Hadoop-dependent), passed through unchanged.

**FACT** `WebHDFS.ukey` calls `GETFILECHECKSUM` and returns the
`FileChecksum` JSON (`webhdfs.py:305-315`) — a server-computed
content hash (MD5-of-MD5 per HDFS). This is backend-specific, not
abstract.

### dbfs (`implementations/dbfs.py:80-125`)

**FACT** `ls` returns only `{name, type, size}` (`dbfs.py:113-120`).
No etag/version/id. `info` is inherited from `AbstractFileSystem`
(default `ls`-based implementation).

### arrow / HadoopFileSystem (`implementations/arrow.py:84-131`)

**FACT** `_make_entry` returns `{name, size, type, mtime}`
(`arrow.py:126-131`). No etag/version.

### git (`implementations/git.py:74-101`)

**FACT** `_object_to_info` returns `{type, name, hex, mode, size}`
(`git.py:77-85`). `hex` is the pygit2 object id (SHA-1/SHA-256 of the
Git object). `GitFileSystem.ukey` returns `info(path)["hex"]`
(`git.py:100-101`) — a content-addressed native id, but only on this
backend.

### reference / dirfs / cached / dask (wrappers)

**FACT** `DirFileSystem.info` delegates to the wrapped fs and rewrites
`name` to a relative path (`dirfs.py:311-315`); `ukey`/`checksum`
delegate (`dirfs.py:293-297`). No new identity fields are added.

**FACT** `ReferenceFileSystem.ls` synthesizes `{name, type, size}`
where `size` is `len(json.dumps(...))` of the reference metadata, not
the referenced object's real size (`reference.py:258-299`). This size
is a metadata artifact, not a backend-reported byte size.

### Summary table of extra keys (bundled backends)

**FACT**

| Backend | Extra keys beyond name/size/type | Native id field |
| --- | --- | --- |
| local | created, islink, mode, uid, gid, mtime, ino, nlink, destination | `ino` (host-local) |
| memory | created | none |
| http | mimetype, partial, url, ETag?, Content-MD5?, Digest?, Last-Modified? | ETag/Content-MD5/Digest (server-dependent, not guaranteed) |
| ftp | (MLSD pass-through: modify, perm, unique?, ...) | `unique` (RFC 3659, server-dependent) |
| sftp | uid, gid, time, mtime | none |
| smb | uid, gid, time, mtime | none |
| webhdfs | (full FileStatus: owner, group, permission, modificationTime, accessTime, fileId, ...) | `fileId` (HDFS-native) |
| dbfs | (none) | none |
| arrow | mtime | none |
| git | hex, mode | `hex` (Git object SHA) |

**INFERENCE** The only backends in this repo exposing a content-bound
native id are `git` (`hex`) and `webhdfs` (`fileId`, plus `ukey`
checksum). `http` exposes ETag/Content-MD5 *when the server sends
them*, which is not guaranteed. There is no common key name across
backends for such an id.

---

## 4. `walk()` / `find()` error behavior

**FACT** Sync `walk` with `on_error="omit"` (default): on
`FileNotFoundError`/`OSError` from `ls`, the method `return`s without
yielding that path at all (`spec.py:485-492`). The errored subtree is
silently dropped from the iteration.

**FACT** Sync `walk` with `on_error="raise"`: re-raises the first
exception (`spec.py:488-489`).

**FACT** Sync `walk` with `on_error=<callable>`: calls
`on_error(e)` then `return`s without yielding (`spec.py:490-492`).

**FACT** Async `_walk` with `on_error="omit"` or callable: yields
`(path, {}, {})` (detail) or `(path, [], [])` (no detail) for the
errored path, then returns (`asyn.py:866-875`).

**INFERENCE** There is a behavioral inconsistency between sync and
async `walk` on error: sync `walk` skips the errored path entirely
(no yield), while async `_walk` yields an empty entry for it. A
consumer that switches between sync and async cannot rely on a
uniform "errored paths appear as empty" contract.

**FACT** `find` inherits `walk`'s error behavior because it is
implemented on top of `walk(..., detail=True)` (`spec.py:561-564`).
`find` itself has no `on_error` parameter; the only kwarg controlling
output shape is `detail` (`spec.py:537`).

**FACT** Neither `walk` nor `find` returns a structured "partial
failure" object. There is no aggregate error, no per-path error map,
and no continuation token. The caller learns about failures only
through the `on_error` policy on `walk` (raise / omit / callable).

---

## 5. `details=True` mechanism

**FACT** There are two distinct `detail`/`details` mechanisms:

1. `ls(path, detail=True)` and `find(path, detail=True)` control
   whether the return is a list of names vs. list/dict of info dicts
   (`spec.py:404-407`, `537`, `569-573`).
2. `AbstractBufferedFile.details` is a *property* on file objects that
   lazily calls `self.fs.info(self.path)` and caches it
   (`spec.py:2000-2004`). It is unrelated to listing; it is the per
   open-file metadata accessor.

**FACT** `AbstractBufferedFile.__hash__` in read mode returns
`int(tokenize(self.details), 16)` (`spec.py:2025-2029`), and
`__eq__` compares hashes (`spec.py:2031-2040`). So two open read-mode
files are "equal" iff their `info()` dicts tokenize identically.

**INFERENCE** The `details` property gives a per-file identity based
on the full `info()` dict, but because `info()` fields are
backend-specific and may include volatile fields (e.g. `mtime`,
`accessTime`), this hash is not a stable content identity across
backends or across time.

---

## 6. Unified checksum / hash interface

**FACT** `AbstractFileSystem.checksum(path)` exists
(`spec.py:766-777`):
```
def checksum(self, path):
    return int(tokenize(self.info(path)), 16)
```
The docstring says: "If the checksum is the same from one moment to
another, the contents are guaranteed to be the same. If the checksum
changes, the contents *might* have changed." Default "will probably
capture creation/modification timestamp ... or maybe access timestamp
(which would be bad)" (`spec.py:773-775`).

**FACT** `AbstractFileSystem.ukey(path)` ("Hash of file properties, to
tell if it has changed") returns
`sha256(str(self.info(path)).encode()).hexdigest()`
(`spec.py:1453-1455`).

**FACT** Only two bundled backends override `ukey`/`checksum` with a
content-derived value: `WebHDFS.ukey` (HDFS GETFILECHECKSUM,
`webhdfs.py:305-315`) and `GitFileSystem.ukey` (returns `hex`,
`git.py:100-101`). `HTTPFileSystem.ukey` returns
`tokenize(url, kwargs, protocol)` — a URL-based token, not a content
hash (`http.py:427-429`).

**FACT** `DirFileSystem.ukey`/`checksum` delegate to the wrapped fs
(`dirfs.py:293-297`); no other bundled backend overrides them, so they
inherit the `info()`-dict hash default.

**INFERENCE** fsspec has a *named* unified checksum interface
(`checksum`, `ukey`), but the default implementations are
property-dict hashes, not content hashes. Content-hash semantics exist
only where a backend overrides them (webhdfs, git). There is no
mechanism to request a specific hash algorithm (md5/sha256/crc32c)
through the abstract interface.

---

## 7. `ls` completeness / pagination / continuation

**FACT** The `ls` contract (`spec.py:377-416`) says nothing about
completeness, pagination, or continuation tokens. It returns a list
synchronously. There is no `next_token`, `marker`, `is_truncated`, or
`start_after` parameter on `AbstractFileSystem.ls`.

**FACT** A repository-wide search for `ContinuationToken`, `NextToken`,
`page_token`, `start_after`, `marker` (as a pagination field),
`is_truncated` found no matches in fsspec core or bundled
implementations (only `root_marker`, which is a path-prefix constant,
unrelated to pagination — `spec.py:169`, `implementations/local.py:31`,
etc.).

**FACT** `ls` may consult `self.dircache` (an in-memory
`DirCache`, `dircache.py:6`) via `_ls_from_cache` (`spec.py:418-441`),
but the cache is a full-listing cache keyed by path, not a resumable
cursor.

**INFERENCE** fsspec's abstract `ls` assumes a backend can return a
complete directory listing in one call. Any pagination internal to a
cloud backend (e.g. S3 `ListObjectsV2` continuation tokens) must be
looped through *inside* the backend's `ls` implementation and is not
exposed to the caller. Therefore fsspec provides no way for a caller
to resume an interrupted listing across processes or to bound the
page size through the abstract interface.

**INFERENCE** "Provider-complete traversal" cannot be *proven* from
the fsspec abstract contract alone: the contract neither documents a
completeness guarantee nor exposes a partial-result/continuation
channel. Completeness is an obligation pushed entirely into each
backend's `ls` override, which is out of scope of this repository for
s3/gcs/azure/gdrive/dropbox/webdav (external packages, see Scope
note). For bundled backends, `ls` calls a single native listing
primitive (`os.scandir` for local, `MLSD` for ftp, `smbclient.readdir`
for smb, `get_file_info(FileSelector)` for arrow, `LISTSTATUS` for
webhdfs) and returns whatever that primitive returns in one shot.

---

## 8. Change notification / delta / webhook

**FACT** A repository-wide search for `webhook`, `notification`,
`delta`, `watch` (as a filesystem change-watch), `event` (as a
filesystem change event) found no such API on `AbstractFileSystem`
or any bundled backend. Matches for `event` are exclusively:
asyncio `Event` primitives (`prefetcher.py:125,154,187,200,242,260`,
`tests/test_prefetcher.py`), GUI signal-slot events in `gui.py:19-107`
(a Panel/widget helper, unrelated to FS change notifications), and
callback accounting in tests (`tests/test_spec.py:1312-1455`,
`tests/test_callbacks.py`).

**FACT** `AbstractFileSystem.invalidate_cache(path=None)`
(`spec.py:322-337`) is the only cache-control hook; it is a manual
invalidation entry point, not a push-based notification.

**INFERENCE** fsspec has no change-notification, delta, or webhook
mechanism. Cache freshness is managed either by TTL
(`DirCache.listings_expiry_time`, `dircache.py:41-42,60-63`) or by
explicit `invalidate_cache` calls. A consumer that needs
event-driven updates must poll `ls`/`info` and diff itself.

---

## 9. Checkpoint / resume / scan-state persistence

**FACT** `DirCache` (`dircache.py:6-125`) stores listings in an
in-memory `OrderedDict` or `dict` (`dircache.py:51`), with optional
LRU eviction (`dircache.py:96-100`) and TTL expiry
(`dircache.py:60-63`). It has no `save`/`load`/`persist` method and
no disk serialization; `__reduce__` (`dircache.py:121-124`) only
reconstructs the *configuration* (empty cache), not cached contents.

**FACT** `AbstractFileSystem` defines no method named `checkpoint`,
`resume`, `save_state`, `scan_state`, or similar (verified against
the full method list of `spec.py:151-1906`).

**FACT** `to_json`/`from_json` (`spec.py:1502-1534`, `1535-1559`) and
`to_dict`/`from_dict` (`spec.py:1560-1638`) serialize the *filesystem
instance configuration* (class + args + storage_options), not scan
progress or listing state.

**INFERENCE** fsspec has no checkpoint/resume or scan-state
persistence. A long traversal that is interrupted must restart from
the root; the in-memory `dircache` is lost on process exit. Any
resumable scan state would have to be built by the consumer on top of
`walk`/`find`.

---

## 10. `AsyncFileSystem` / `CatFileSystem` / mixin capabilities

**FACT** `AsyncFileSystem` (`asyn.py:425-425`) extends
`AbstractFileSystem` and provides async defaults `_info`, `_ls`,
`_walk`, `_find`, `_glob`, `_cat`, `_cat_ranges`, `_copy`, `_rm`, etc.
(`asyn.py:848-852`, `854-919`, `921-1011`, `587-611`, `613-...`,
`485-543`, `466-476`). It adds *concurrency* (batch via
`_run_coros_in_chunks`) and a `mirror_sync_methods` flag
(`asyn.py:439`), but it does not add any new identity fields or
completeness guarantees beyond `AbstractFileSystem`.

**FACT** Async `_cat` supports `on_error` in `{"raise", "omit"}`
(`asyn.py:596-609`) — note: **not** `"return"`, unlike sync `cat`
which supports `{"raise", "omit", "return"}` (`spec.py:943-948,
967-970`). With `"omit"`, failed paths are filtered out of the result
dict (`asyn.py:605-609`); there is no way to receive the exception
object per-path in async `_cat`.

**FACT** Async `_cat_ranges` supports `on_error` in `{"return",
"raise"}` with default `"return"` (`asyn.py:613-635`), matching sync
`cat_ranges` (`spec.py:901-933`).

**FACT** There is no class named `CatFileSystem` in this repository
(verified: the only `cat`-related definitions are the `cat`,
`cat_file`, `cat_ranges` *methods* on `AbstractFileSystem`/
`AsyncFileSystem`). "CatFileSystem" is not an fsspec abstraction.

**FACT** `GenericFileSystem` (`generic.py:146`) is an
`AsyncFileSystem` subclass that dispatches by URL protocol across
registered filesystems (`generic.py:17-40`, `146-280`). It adds
multi-backend routing and an `rsync` helper (`generic.py:40-145`,
`280`), not identity or completeness primitives.

**FACT** `CachingFileSystem` / `WholeFileCacheFileSystem` /
`SimpleCacheFileSystem` (`implementations/cached.py:81`, `602`, `919`)
wrap another fs and cache file *contents* locally; they delegate
`info`/`ls` to the source fs (`cached.py:1009`, `1041`) and add no
new identity fields.

**INFERENCE** None of the mixin/wrapper classes in fsspec
(`AsyncFileSystem`, `GenericFileSystem`, `CachingFileSystem`,
`DirFileSystem`, `DaskWorkerFileSystem`, `ReferenceFileSystem`)
provide additional cross-backend stable identity, provider-complete
traversal guarantees, or partial-failure reporting beyond what
`AbstractFileSystem` defines. They add concurrency, routing, content
caching, path remapping, and reference indirection respectively.

---

## Consolidated gap summary

**FACT** The following are absent from fsspec's abstract interface
(`fsspec/spec.py:151-1906`, `fsspec/asyn.py:425-...`):

| Capability | Present? | Evidence |
| --- | --- | --- |
| Unified per-object stable id field in `info()` | No | `spec.py:746-747` (only name/size/type required); Section 3 |
| Unified `etag`/`version`/`generation` field | No | `spec.py:393-395` ("Additional information may be present, appropriate to the file-system") |
| Content-hash `checksum`/`ukey` by default | No (property-dict hash default) | `spec.py:766-777`, `1453-1455`; only webhdfs/git override |
| Algorithm-selectable hash interface | No | `checksum(path)` takes no algorithm arg (`spec.py:766`) |
| `ls` completeness guarantee in contract | No | `spec.py:377-416` (silent on completeness) |
| Pagination / continuation token on `ls`/`walk`/`find` | No | no such params; Section 7 |
| Partial-failure structured result from `walk`/`find` | No | only `on_error` policy on `walk`; `find` has no `on_error` (`spec.py:537`) |
| Change notification / delta / webhook | No | Section 8 |
| Checkpoint / resume / persisted scan state | No | Section 9 |
| Cross-backend `cat` partial-failure parity (sync vs async) | No | sync `cat` supports `"return"`, async `_cat` does not (`spec.py:943-948` vs `asyn.py:596-609`) |
| Sync/async `walk` error parity | No | sync skips errored path, async yields empty (`spec.py:485-492` vs `asyn.py:866-875`) |

**INFERENCE** fsspec is a *lowest-common-denominator* filesystem
abstraction: it standardizes path/size/type and a set of operation
names, but explicitly delegates identity, content hashing, listing
completeness, and failure semantics to each backend. Any system that
needs a provable cross-backend stable identity, provider-complete
traversal proof, or structured partial-failure reporting cannot obtain
it from fsspec alone and must either (a) restrict itself to backends
whose `info()` overrides it inspects and trusts, or (b) build a layer
above fsspec that imposes those guarantees per backend.

**INFERENCE** For the external cloud backends (s3/gcs/azure/abfs/
gdrive/dropbox/webdav) the questions of stable identity and traversal
completeness cannot be answered from this repository because their
source is not bundled here; they are only registered by import path
(`registry.py:62-241`). Answering those requires inspecting s3fs,
gcsfs, adlfs, gdrive_fsspec, dropboxdrivefs, and webdav4 separately.
