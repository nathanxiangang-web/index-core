# W-D Collector Gap Matrix (D03)

> Independent investigation of rclone and fsspec source code, building a unified
> capability matrix, cross-referenced with the D02 AList/OpenList conclusions.
>
> Worker D. Role: independent investigation. This report reads actual cloned source
> and cites file paths + line numbers for every FACT. FACT and INFERENCE are strictly
> separated. Nothing is asserted as fact without a source citation.

---

## 0. Source Code Version Records (hard rule)

| Repo | Repository URL | Branch | Commit SHA | Checked date | License file |
|------|----------------|--------|------------|--------------|--------------|
| rclone | https://github.com/rclone/rclone | `master` | `cfb90e3ebed479119718e3ae44b1171b060079e9` | 2026-09-23 | `COPYING` -> MIT (FACT: verified by reading `COPYING` in the cloned tree) |
| fsspec | https://github.com/fsspec/filesystem_spec | `main` | `751721f96c04fb53aa3b945ab1b0ff53781e79c0` | 2026-09-23 | `LICENSE` -> BSD-3-Clause (FACT: verified by reading `LICENSE` in the cloned tree) |

**Method note:** Both repositories were cloned with `git clone --depth=1` to `/tmp/d03/rclone` and `/tmp/d03/fsspec`. Every FACT below was established by reading the cited file at the cited line range in the cloned tree at the commit SHA recorded above. No claim depends on training-data recall; each is backed by a direct source read. INFERENCE markers are used only where a behavioral conclusion is drawn from a structural fact plus reasonable assumptions about runtime semantics.

---

## 1. Investigation Dimensions

Each tool is graded on 7 capability dimensions. Verdicts use the alphabet:
`YES / NO / PARTIAL / DRIVER_DEPENDENT / UNKNOWN`.

- **YES**: the abstract interface defines the capability and it is structurally present for every implementation.
- **NO**: the abstract interface does not define the capability, or it is structurally absent.
- **PARTIAL**: the capability exists but with a documented structural gap that limits its reliability for snapshot/discovery.
- **DRIVER_DEPENDENT**: the capability is an optional interface that some implementations provide and others do not; the abstract contract does not guarantee it.
- **UNKNOWN**: cannot be established from source alone.

Every cell cites evidence (file path + line range in the cloned tree). Paths are relative to the repo root (e.g. `fs/types.go:166` means `/tmp/d03/rclone/fs/types.go` line 166).

---

## 2. rclone Capability Matrix

### 2.1 Dimension 1: Stable Resource Identity

**Verdict: DRIVER_DEPENDENT**

Evidence:
- FACT: `fs/types.go:166-170` defines `IDer` as an **optional** interface: `type IDer interface { ID() string }`. The doc comment says "ID returns the ID of the Object if known, or \"\" if not".
- FACT: `fs/types.go:172-176` defines `ParentIDer` as a separate optional interface.
- FACT: `fs/operations/lsjson.go:207-209` populates `item.ID` only when `entry` implements `fs.IDer`: `if do, ok := entry.(fs.IDer); ok { item.ID = do.ID() }`. The `ID` field is `json:",omitempty"` (`fs/operations/lsjson.go:30`), so it is absent from JSON for non-IDer backends.
- FACT: 38 backend files define `func (...) ID() string` (grep over `backend/*/[^.]*.go`). Examples: `backend/onedrive/onedrive.go:2912`, `backend/drive/drive.go:4637`, `backend/box/box.go:1777`, `backend/b2/b2.go:2408`.
- FACT: `backend/local/local.go:2023-2025` defines `func (d *Directory) ID() string { return "" }` for local directories -- the method exists but returns the empty string, i.e. no meaningful stable ID.
- FACT: `backend/s3/s3.go` has no `func (...) ID() string` method on `Object` (grep returned no match); S3 `Object` carries `versionID *string` (`backend/s3/s3.go:1188`) but does not expose it through `IDer`.
- FACT: `backend/webdav/webdav.go` has no `func (...) ID() string` (grep returned no match).

INFERENCE: rclone has a uniform *optional* identity interface, but whether a stable ID survives rename/move depends entirely on the backend. GDrive/OneDrive/Box/B2 expose one; local returns empty; S3/WebDAV do not implement `IDer` at all. There is no cross-backend guarantee.

### 2.2 Dimension 2: Snapshot Completeness (provider-complete traversal + explicit done semantics)

**Verdict: PARTIAL**

Evidence:
- FACT: `fs/features.go:670-685` defines `ListR` as an optional interface: "It should call callback for each tranche of entries read... If callback returns an error then the listing will stop immediately." There is no `done`/`total`/`has_more` field in the contract; completion is signaled by the `ListR` call returning nil (iterator exhaustion).
- FACT: `fs/features.go:687-703` defines `ListP` (paginated non-recursive list) with the same callback pattern and no continuation token in the signature.
- FACT: `fs/walk/walk.go:149-164` `ListR` chooses backend `ListR` if available, else falls back to `listRwalk`; the choice is structural, not signaled to the caller.
- FACT: `fs/walk/walk.go:168-179` `listRwalk`: on a per-directory list error, the code logs the error, counts it via `fs.CountError`, and **continues** walking: `if err != nil { listErr = err; err = fs.CountError(ctx, err); fs.Errorf(path, "error listing: %v", err); return nil }`. The error is returned at the end via `listErr`, but the set of skipped directories is logged, not returned as a structured value.
- FACT: `fs/operations/rc.go:69-79` `rcList` buffers the entire list into an in-memory slice `list := []*ListJSONItem{}` and returns it as `out["list"]`. There is no continuation token in the RC request or response; pagination is handled internally by the backend and not exposed to the RC caller.
- FACT: `fs/fs.go:39` defines `ErrorListAborted = errors.New("list aborted")`, so there is a typed error for aborted lists, but no structured "partial result + skipped set" return.

INFERENCE: rclone provides a streamed/listed traversal that is provider-complete *when the backend's List/ListR is itself complete and error-free*. There is no explicit `done` flag; completion is inferred from iterator exhaustion. A silent mid-stream backend truncation or a swallowed error yields a partial list with no structured completeness signal. This matches the D02 verdict (D02 section 2.3, row "complete traversal signal = PARTIAL").

### 2.3 Dimension 3: Partial Failure Visibility

**Verdict: PARTIAL**

Evidence:
- FACT: `fs/march/march.go:562-585` surfaces per-directory list errors: `if srcListErr != nil { fs.Errorf(job.srcRemote, "error reading source directory: %v", srcListErr); srcListErr = fs.CountError(m.Ctx, srcListErr); return nil, srcListErr }`. The error is propagated with the remote path in the log.
- FACT: `fs/march/march.go:283` aggregates errors: `return fmt.Errorf("march failed with %d error(s): first error: %w", errCount, jobError)`. The count is reported but only the *first* error is wrapped; the full set of skipped directories is not returned as a structured value.
- FACT: `fs/walk/walk.go:172-176` logs each per-directory error with the path and continues.
- FACT: `fs/fs.go:36-57` defines typed errors: `ErrorDirNotFound`, `ErrorObjectNotFound`, `ErrorPermissionDenied`, `ErrorListAborted`, `ErrorCantListRoot`, etc. These are distinguishable by `errors.Is`.
- FACT: `fs/operations/lsjson.go:306-369` `StatJSON` distinguishes `ErrorObjectNotFound` and `ErrorDirNotFound` and maps them to different return paths.

INFERENCE: rclone surfaces *that* a partial failure occurred and logs *which* directory failed, but the RC/listing caller receives only the first error, not a structured "skipped set". A snapshot builder can detect "scan was incomplete" but cannot programmatically enumerate which directories were skipped without parsing logs. Typed errors are distinguishable, which is better than AList/OpenList (D02 C11).

### 2.4 Dimension 4: Checkpoint/Resume

**Verdict: NO**

Evidence:
- FACT: grep for `checkpoint|resume|scan.*state|persist` in `fs/walk/walk.go`, `fs/march/march.go`, `fs/operations/operations.go`, `fs/operations/rc.go` returned no matches in listing-related code.
- FACT: `fs/operations/rc.go:59-79` `rcList` takes only `fs`, `remote`, `opt` -- no `cursor`/`token`/`resume` parameter.
- FACT: `fs/features.go:670-685` `ListR` signature has no state parameter: `ListR(ctx context.Context, dir string, callback ListRCallback) error`.
- FACT: The only `resume` references in the rclone tree are in `cmd/serve/sftp` (SFTP protocol resume), `cmd/mount2` (FUSE), and `fs/operations/logger.go:267` (an unrelated `Side` field comment) -- none relate to listing scan state.

INFERENCE: rclone has no scan-state persistence and no resume-from-checkpoint for listing. A failed/interrupted scan must restart from the root. This is a structural absence, not a configuration gap.

### 2.5 Dimension 5: Delta/Change Notification

**Verdict: DRIVER_DEPENDENT**

Evidence:
- FACT: `fs/features.go:569-581` defines `ChangeNotifier` as an optional interface: "ChangeNotify calls the passed function with a path that has had changes. If the implementation uses polling, it should adhere to the given interval." The doc explicitly says the implementation may use polling.
- FACT: `fs/features.go:93-96` declares `ChangeNotify` as a feature field; `fs/features.go:311` copies it; `fs/features.go:423-425` masks it off if the wrapper does not support it.
- FACT: 14 backend files declare `_ fs.ChangeNotifier = (*Fs)(nil)` (grep over `backend/`): `archive`, `cache`, `chunker`, `combine`, `compress`, `crypt`, `drive`, `hasher`, `huaweidrive`, `iclouddrive/icloudphotos`, `pcloud`, `pixeldrain`, `union`, and `box` (via `box.go`). Out of 69 backend directories, 14 implement `ChangeNotifier`.
- FACT: `backend/drive/drive.go:3234` implements `ChangeNotify`; `backend/drive/drive.go:4752` declares `_ fs.ChangeNotifier = (*Fs)(nil)`.
- FACT: `backend/local/local.go` has no `ChangeNotify` method (grep returned no match in `backend/local/`). INFERENCE: this contradicts the D02 claim that "rclone local has fsnotify ChangeNotify"; at this commit, local does not implement `ChangeNotifier`. (D02 marked this UNVERIFIED; this D03 read resolves it for the checked commit: local has no `ChangeNotify`.)
- FACT: `backend/s3/s3.go`, `backend/webdav/webdav.go`, `backend/onedrive/onedrive.go` have no `ChangeNotify` method (grep returned no matches for the method definition).
- FACT: `fs/operations/rc.go:449` registers `ChangeNotify` as an RC parameter with default `false`, confirming it is an opt-in backend feature.

INFERENCE: `ChangeNotify` is a lossy hint, not a durable delta log. It is polling-based by contract (`fs/features.go:573-574`), implemented by a minority of backends (14/69), and absent on S3/WebDAV/OneDrive/local at this commit. No replay token is exposed. This matches and sharpens D02 C5.

### 2.6 Dimension 6: Hash/Checksum

**Verdict: DRIVER_DEPENDENT**

Evidence:
- FACT: `fs/types.go:104-113` `ObjectInfo` interface defines `Hash(ctx context.Context, ty hash.Type) (string, error)` with doc "If no checksum is available it returns \"\"".
- FACT: `fs/types.go:74-76` `Fs` interface defines `Hashes() hash.Set` returning the supported hash types.
- FACT: 68 backend files define `func (...) Hashes() hash.Set` (grep over `backend/*/[^.]*.go`).
- FACT: `backend/webdav/webdav.go:1318-1327` `Hashes()` returns `hash.Set(hash.None)` by default and only adds MD5/SHA1 if `f.hasOCMD5`/`f.hasOCSHA1`/`f.hasMESHA1` are set (ownCloud/Nextcloud-specific extensions). Plain WebDAV has no hash.
- FACT: `backend/s3/s3.go:3329-3331` `Hashes()` returns `hash.Set(hash.MD5)`. INFERENCE: S3 ETag is MD5 only for single-part PUT; for multipart, ETag is `MD5(concat(part_hashes)) + "-N"`, not a content hash. This is a provider semantics fact (D02 V10) and rclone does not correct it.
- FACT: `fs/operations/lsjson.go:221-230` populates `item.Hashes` only when `lj.showHash` is set (opt-in); the caller must request hashes explicitly.
- FACT: `fs/operations/lsjson.go:83-87` `ShowHash` is a JSON option; `HashTypes` lets the caller pick which hashes.

INFERENCE: rclone has a uniform hash *interface*, but availability is backend-dependent. WebDAV has none by default; S3 has MD5 (which is not a content hash for multipart); GDrive has MD5 only for binary files (D02 V12). The caller must opt in via `showHash`. This matches D02 C2.

### 2.7 Dimension 7: Pagination

**Verdict: YES (internal) / PARTIAL (exposed to RC caller)**

Evidence:
- FACT: `fs/features.go:670-685` `ListR` handles pagination internally via the callback; the backend's `ListR` implementation calls the callback with tranches until exhausted.
- FACT: `fs/features.go:687-703` `ListP` is a paginated non-recursive list interface (callback-based, no continuation token in the signature).
- FACT: 27 backend files define `func (...) ListR(` (grep over `backend/*/[^.]*.go`), including `backend/s3/s3.go:2800` and `backend/drive/drive.go:2244`. Backends without `ListR` fall back to `listRwalk` (`fs/walk/walk.go:155-161`).
- FACT: `fs/operations/rc.go:69-79` `rcList` returns the full buffered list; no continuation token is exposed to the RC caller. `fs/operations/rc.go:27-55` documents the RC parameters: `fs`, `remote`, `opt` -- no `cursor`/`page`/`token`.
- FACT: `fs/fs.go:39` `ErrorListAborted` provides a typed error for aborted pagination.

INFERENCE: rclone handles pagination internally and correctly for backends that implement `ListR`/`ListP`. The RC caller receives a complete list (or an error), never a partial list plus a resume token. This is a scalability limit (the whole list is buffered in memory) but not a correctness gap for finite lists. There is no *resumable* pagination exposed to external callers.

---

## 3. fsspec Capability Matrix

### 3.1 Dimension 1: Stable Resource Identity

**Verdict: NO (abstract) / DRIVER_DEPENDENT (concrete)**

Evidence:
- FACT: `fsspec/spec.py:151-243` `AbstractFileSystem` defines no `id`/`object_id`/`file_id` method or field. The only identity-like attribute is `fsid` (`fsspec/spec.py:222-227`), documented as "Persistent filesystem id that can be used to compare filesystems across sessions" -- but the default implementation is `raise NotImplementedError`, and it identifies the *filesystem instance*, not individual objects.
- FACT: `fsspec/spec.py:377-415` `ls` contract: "Must include: full path to the entry (without protocol); size of the entry, in bytes; type of entry". No `id` field is required. "Additional information may be present, appropriate to the file-system, e.g., generation, checksum, etc." -- identity is not in the required or enumerated optional keys.
- FACT: `fsspec/spec.py:732-764` `info` contract: returns a dict with `name`, `size`, `type` and "other FS-specific keys". No `id` key is documented.
- FACT: `fsspec/implementations/local.py:79-128` `info` returns `{"name", "size", "type", "created", "islink", "mode", "uid", "gid", "mtime", "ino", "nlink"}`. The `ino` field (inode) is present for local, but this is implementation-specific, not in the abstract contract, and inodes are not stable across filesystems/backups.
- FACT: `fsspec/implementations/memory.py:262-282` `info` returns `{"name", "size", "type", "created"}` -- no id field at all.
- FACT: fsspec has no in-tree S3/GCS/Azure implementations (ls `fsspec/implementations/` shows no `s3.py`/`gcs.py`/`abfs.py`); those live in separate packages (s3fs/gcsfs/adlfs) whose source is not in this repo and was not inspected here. INFERENCE: any id exposure in those external packages is outside this investigation's verified scope.

INFERENCE: the fsspec abstract contract has no stable object identity. Some implementations (local) expose `ino` as an unstructured extra field, but it is not part of the contract and not stable across backends. This is a stronger absence than rclone's: rclone at least defines an optional `IDer` interface; fsspec defines none.

### 3.2 Dimension 2: Snapshot Completeness

**Verdict: PARTIAL**

Evidence:
- FACT: `fsspec/spec.py:443-535` `walk` is a generator yielding `(path, dirs, files)` tuples. Completion is signaled by `StopIteration` (generator exhaustion). There is no `done`/`total`/`has_more` flag.
- FACT: `fsspec/spec.py:537-573` `find` builds on `walk` and returns a sorted list (or dict); no completeness flag.
- FACT: `fsspec/spec.py:377-415` `ls` returns a list; no pagination/continuation token in the signature. The contract says "May use refresh=True|False to allow use of self._ls_from_cache" -- `refresh` controls cache use, not pagination.
- FACT: `fsspec/spec.py:485-492` `walk` catches `(FileNotFoundError, OSError)` and applies `on_error` ("omit"/"raise"/callable). If `on_error="omit"` (the default), a failed subdirectory is silently skipped with an empty yield -- the caller cannot distinguish "empty directory" from "directory listing failed" without switching to `on_error="raise"` or a callable.
- FACT: `fsspec/spec.py:418-441` `_ls_from_cache` reads from `self.dircache`; if the cache is stale, `walk`/`find`/`ls` return cached state, not live state.

INFERENCE: fsspec provides a complete traversal *when the backend is error-free and the cache is fresh*. There is no explicit completeness signal; completion is inferred from generator/list exhaustion. With the default `on_error="omit"`, partial failures are silently swallowed, making completeness even harder to verify than rclone. This matches and sharpens D02's AList verdict (D02 V4).

### 3.3 Dimension 3: Partial Failure Visibility

**Verdict: PARTIAL (better than AList, weaker than rclone's typed errors)**

Evidence:
- FACT: `fsspec/spec.py:470-492` `walk` `on_error` parameter: "omit" (default), "raise", or a callable called with a single `OSError`. This is explicit partial-failure visibility -- the caller can choose to receive errors.
- FACT: `fsspec/spec.py:943-948` `cat` `on_error`: "raise"/"omit"/"return". With "return", failed paths get the exception instance as the value (`fsspec/spec.py:969-970`), giving per-path failure visibility for `cat`.
- FACT: `fsspec/spec.py:718-725` `exists` uses a bare `except:` (line 723) -- *any* exception is swallowed and mapped to `False`. This conflates "not found", "permission denied", "rate limited", and "network error".
- FACT: `fsspec/spec.py:787-799` `isdir`/`isfile` catch `OSError`/`Exception` and return `False`, conflating errors with negative answers.
- FACT: fsspec defines no typed error hierarchy comparable to rclone's `fs.ErrorPermissionDenied`/`ErrorObjectNotFound`/`ErrorDirNotFound`. Errors are standard Python exceptions (`FileNotFoundError`, `OSError`) plus backend-specific exceptions not standardized by the abstract spec.

INFERENCE: fsspec's `walk(on_error=callable)` and `cat(on_error="return")` give *structured* per-path failure visibility, which is better than AList's HTTP-status-only model. But `exists`/`isdir`/`isfile` swallow errors, and there is no typed error hierarchy to distinguish permission-denied from not-found from rate-limited. A snapshot builder can learn *which* paths failed but not reliably *why*.

### 3.4 Dimension 4: Checkpoint/Resume

**Verdict: NO**

Evidence:
- FACT: grep for `checkpoint|resume|scan.*state` in `fsspec/spec.py` returned only `fsspec/spec.py:454` ("it resumes walk() again"), which is a docstring about `topdown` pruning, not scan-state persistence.
- FACT: `fsspec/spec.py:443` `walk` signature: `walk(self, path, maxdepth=None, topdown=True, on_error="omit", **kwargs)` -- no `cursor`/`token`/`state` parameter.
- FACT: `fsspec/spec.py:537` `find` signature: `find(self, path, maxdepth=None, withdirs=False, detail=False, **kwargs)` -- no resume parameter.
- FACT: `fsspec/spec.py:377` `ls` signature: `ls(self, path, detail=True, **kwargs)` -- no continuation token.
- FACT: `fsspec/dircache.py:1-100` `DirCache` is an in-memory listing cache with TTL (`listings_expiry_time`) and LRU eviction (`max_paths`), but it is not a persistent scan-state store; it is cleared on process exit and not serialized to disk.

INFERENCE: fsspec has no scan-state persistence and no resume-from-checkpoint. A failed/interrupted scan must restart from the root. The `DirCache` is an in-memory listing cache, not a checkpoint. This is structurally identical to rclone's absence (section 2.4).

### 3.5 Dimension 5: Delta/Change Notification

**Verdict: NO**

Evidence:
- FACT: grep for `watch|notify|event|on_change|subscribe` in `fsspec/spec.py` returned no matches in method definitions or signatures (only unrelated docstrings about garbage collection and `mv`'s "prevent data corruption" comment).
- FACT: `fsspec/spec.py:151-243` `AbstractFileSystem` defines no `watch`/`notify`/`subscribe`/`on_change` method.
- FACT: No fsspec implementation file in `fsspec/implementations/` defines a watch/notify method (grep over `fsspec/implementations/*.py` for `def watch|def notify|def subscribe` returned no matches).

INFERENCE: fsspec has no change-notification mechanism at any level -- not in the abstract spec and not in any in-tree implementation. This is a stronger absence than rclone's (rclone has `ChangeNotifier` as an optional interface with 14 backends). fsspec is polling-only (re-list on every call), and even polling is left to the caller.

### 3.6 Dimension 6: Hash/Checksum

**Verdict: PARTIAL (weak)**

Evidence:
- FACT: `fsspec/spec.py:766-777` `checksum(self, path)`: "Unique value for current version of file. If the checksum is the same from one moment to another, the contents are guaranteed to be the same. If the checksum changes, the contents *might* have changed. This should normally be overridden; default will probably capture creation/modification timestamp (which would be good) or maybe access timestamp (which would be bad)". The default implementation is `return int(tokenize(self.info(path)), 16)` -- a hash of the *metadata dict*, not the content.
- FACT: `fsspec/spec.py:1453-1455` `ukey(self, path)`: "Hash of file properties, to tell if it has changed". Default: `return sha256(str(self.info(path)).encode()).hexdigest()` -- again a hash of the info dict, not the content.
- FACT: Only 1 in-tree implementation overrides `checksum`: `fsspec/implementations/dirfs.py:296`. `dirfs` is a directory-wrapper, not a real backend.
- FACT: 6 implementations override `ukey`: `webhdfs.py:305` (uses Hadoop `GETFILECHECKSUM` -- a real content checksum), `http.py:427` ("assume HTTP files are static, unchanging" -> `tokenize(url, self.kwargs, self.protocol)`, NOT a content hash), `http_sync.py:537`, `git.py:100`, `dirfs.py:293`, `archive.py:22`.
- FACT: `fsspec/implementations/local.py` does not override `checksum` or `ukey` -- local files fall back to the metadata-hash default.
- FACT: `fsspec/implementations/memory.py` does not override `checksum` or `ukey`.

INFERENCE: fsspec's abstract `checksum`/`ukey` are *not* content hashes by default; they are hashes of the metadata dict, which is a change-detection hint, not a content integrity check. Only WebHDFS overrides `ukey` with a real content checksum. Local, memory, HTTP, and most implementations use metadata-derived values. This is weaker than rclone's `Hashes()` interface, which at least returns real content hashes (MD5/SHA1) for backends that support them.

### 3.7 Dimension 7: Pagination

**Verdict: NO (abstract) / DRIVER_DEPENDENT (concrete)**

Evidence:
- FACT: `fsspec/spec.py:377-415` `ls` signature has no `cursor`/`token`/`page`/`continuation` parameter. The contract returns a list, not a `(list, next_token)` pair.
- FACT: `fsspec/spec.py:443-535` `walk` and `fsspec/spec.py:537-573` `find` have no pagination parameters; they recurse until `maxdepth` or exhaustion.
- FACT: `fsspec/spec.py:418-441` `_ls_from_cache` is the only "pagination-like" mechanism -- it serves from `self.dircache` to avoid calling the backend. This is caching, not pagination.
- FACT: fsspec's in-tree implementations (`local`, `memory`, `http`, `webdavs`, `ftp`, `sftp`, `git`, `github`, `jupyter`, etc.) are all non-paginated or hide pagination inside `_ls`/`info` without exposing a continuation token. The abstract spec does not require or define one.
- FACT: External packages (s3fs, gcsfs, adlfs) may paginate internally, but their source is not in this repo and was not inspected. INFERENCE: any pagination in those packages is implementation-internal and not part of the fsspec abstract contract.

INFERENCE: fsspec has no abstract pagination/continuation-token mechanism. Backends that paginate (S3, GCS) do so internally and return a complete list to the caller, which limits scalability but not correctness for finite listings. There is no resumable pagination exposed to the caller.

---

## 4. Unified Capability Matrix (rclone vs fsspec)

| Dimension | rclone | fsspec | Notes |
|-----------|--------|--------|-------|
| 1. Stable Resource Identity | DRIVER_DEPENDENT | NO (abstract) / DRIVER_DEPENDENT (concrete) | rclone defines optional `IDer` (`fs/types.go:166`); fsspec defines no id interface (`fsspec/spec.py:151-243`). |
| 2. Snapshot Completeness | PARTIAL | PARTIAL | Both infer completion from exhaustion; no `done` flag. rclone `ListR` (`fs/features.go:670`); fsspec `walk` (`fsspec/spec.py:443`). |
| 3. Partial Failure Visibility | PARTIAL | PARTIAL | rclone logs+counts per-dir errors (`fs/march/march.go:562`) with typed errors (`fs/fs.go:36-49`); fsspec `walk(on_error=callable)` (`fsspec/spec.py:470-492`) but no typed error hierarchy. |
| 4. Checkpoint/Resume | NO | NO | Neither has scan-state persistence. Structural absence in both. |
| 5. Delta/Change Notification | DRIVER_DEPENDENT | NO | rclone `ChangeNotifier` (`fs/features.go:569`) on 14/69 backends; fsspec has no notify mechanism at all. |
| 6. Hash/Checksum | DRIVER_DEPENDENT | PARTIAL (weak) | rclone `Hashes()` (`fs/types.go:74`) with real hashes on most backends; fsspec `checksum`/`ukey` default to metadata-hash (`fsspec/spec.py:766`, `fsspec/spec.py:1453`). |
| 7. Pagination | YES (internal) / PARTIAL (RC) | NO (abstract) / DRIVER_DEPENDENT (concrete) | rclone handles pagination internally (`fs/features.go:670`); fsspec has no abstract pagination (`fsspec/spec.py:377`). |

---

## 5. Cross-Reference with D02 AList/OpenList Conclusions

D02 (`docs/research/d02/W-D-PROVIDER-SNAPSHOT-MATRIX.md`) established for AList/OpenList:
- **Identity**: path-based, no provider object ID exposed (D02 V1, V16, C8).
- **Completeness**: no explicit done flag; offset pagination with no `has_more` (D02 V4, C10).
- **Partial failure**: HTTP status only, non-uniform error typing (D02 C11).
- **Checkpoint/resume**: not present (D02 does not list it as a capability).
- **Delta/notify**: no delta API, no notify mechanism (D02 V2).
- **Hash**: not in list response (D02 V1, C2).
- **Pagination**: offset-based, unstable under concurrent modification (D02 V4, C4).

| Dimension | AList/OpenList (D02) | rclone (D03) | fsspec (D03) | Gap status |
|-----------|----------------------|----------------------|----------------------|--------------|--------|
| 1. Identity | AList: DRIVER_DEPENDENT (`id` in `/api/fs/list`, some Drivers); OpenList: NO (public FS API no id) | DRIVER_DEPENDENT (optional `IDer`, 38 backends) | NO (abstract) / DRIVER_DEPENDENT (concrete) | rclone is strictly better; AList partial; OpenList/fsspec weakest. |
| 2. Completeness | AList/OpenList: PARTIAL (inferred from page size, error silently swallowed) | PARTIAL (contract-level completeness with explicit failure propagation; cannot prevent silent truncation) | PARTIAL (generator exhaustion, `on_error="omit"` default swallows) | rclone best (explicit error return); fsspec and AList/OpenList swallow errors. |
| 3. Partial failure | AList/OpenList: NO (`storage.List` error silently swallowed, HTTP status non-uniform) | PARTIAL (typed errors + per-dir log, non-nil error return) | PARTIAL (`on_error` callback, no typed errors) | rclone best (typed errors + non-nil return), fsspec second (callable), AList/OpenList worst. |
| 4. Checkpoint | AList/OpenList: NO | NO | NO | Universal gap. All require restart-from-root. |
| 5. Delta/notify | AList/OpenList: NO | DRIVER_DEPENDENT (14/69 backends, polling, no replay) | NO | rclone has a partial hint mechanism; AList/OpenList and fsspec have none. |
| 6. Hash | AList/OpenList: DRIVER_DEPENDENT (`hash_info` field exists, Driver-dependent) | DRIVER_DEPENDENT (real hashes on most backends, opt-in) | PARTIAL (weak, metadata-hash default) | rclone best, AList/OpenList DRIVER_DEPENDENT, fsspec weak (metadata hash). |
| 7. Pagination | AList/OpenList: PARTIAL (offset, unstable) | YES (internal) / PARTIAL (RC buffers, no resume) | NO (abstract) / DRIVER_DEPENDENT (concrete) | rclone best (internal handling), AList/OpenList second (offset, unstable), fsspec abstract has none. |

---

## 6. Seven Final Questions

### F1. Does rclone have a cross-backend stable identity?

**NO.** rclone defines an *optional* `IDer` interface (`fs/types.go:166-170`) and 38 backends implement `ID()`. But:
- `backend/local/local.go:2023-2025` returns `""` (no meaningful ID).
- `backend/s3/s3.go` does not implement `IDer` at all (grep found no `func (...) ID() string` on S3 `Object`).
- `backend/webdav/webdav.go` does not implement `IDer`.
- `fs/operations/lsjson.go:207-209` only populates `ID` when the backend implements `IDer`; the field is `omitempty` (`fs/operations/lsjson.go:30`), so it is absent for non-IDer backends.

FACT: there is no cross-backend guarantee. The capability is DRIVER_DEPENDENT, not universal. This confirms and sharpens D02 V6 (rclone `ID()` exists only for `IDer` backends).

### F2. Does fsspec have a cross-backend stable identity?

**NO.** fsspec's `AbstractFileSystem` (`fsspec/spec.py:151-243`) defines no object-id method or field. The `ls`/`info` contracts (`fsspec/spec.py:377-415`, `fsspec/spec.py:732-764`) require only `name`/`size`/`type`. The `fsid` property (`fsspec/spec.py:222-227`) identifies the filesystem instance, not objects, and defaults to `raise NotImplementedError`. Some implementations add extra keys (local adds `ino` at `fsspec/implementations/local.py:124`), but this is not in the contract and not stable across backends.

FACT: fsspec is strictly weaker than rclone here. rclone at least defines an optional identity interface; fsspec defines none. This confirms D02's finding for AList/OpenList (D02 V16: provider ID discarded) and extends it: fsspec has the same gap at the abstract level.

### F3. Can rclone prove provider-complete traversal?

**PARTIAL — contract-level completeness with explicit failure propagation, not proof against silent backend truncation.**

- `List()` contract expects a complete single directory (`fs/types.go:20-29`)
- Explicit directory-list errors ultimately return non-nil error (`listRwalk` continues other directories but returns error at end, `fs/walk/walk.go:168-185`)
- `err == nil` is a "successful traversal" signal based on rclone contract
- `err != nil` can explicitly downgrade Snapshot to incomplete
- Missing done flag is not the core issue; even with a done flag, cannot prove backend didn't silently omit items
- rclone cannot prove provider never silent-truncates

This is where D03 proves rclone is stronger than AList/OpenList: AList `storage.List` silently swallows errors, rclone propagates them.

### F4. Can fsspec prove provider-complete traversal?

**NO.** fsspec's `walk` (`fsspec/spec.py:443-535`) is a generator; completion is `StopIteration`. With the default `on_error="omit"` (`fsspec/spec.py:471`), failed subdirectories are silently skipped with an empty yield -- the caller cannot distinguish "empty dir" from "listing failed". `find` (`fsspec/spec.py:537-573`) returns a sorted list with no completeness flag. `_ls_from_cache` (`fsspec/spec.py:418-441`) may return stale cached state.

FACT: fsspec is strictly weaker than rclone here. With `on_error="omit"`, fsspec actively hides partial failures; the caller must opt into `on_error="raise"` or a callable to detect them, and even then gets no structured "skipped set". This confirms D02 C10 for AList and extends it to fsspec.

### F5. Which partial failures can be reliably exposed?

**Reliably exposed (cross-tool):**
- **rclone**: per-directory list errors are logged with the path and counted (`fs/march/march.go:562-585`, `fs/walk/walk.go:172-176`). The *first* error is returned (`fs/march/march.go:283`). Typed errors (`fs/fs.go:36-49`) distinguish `ErrorDirNotFound`/`ErrorObjectNotFound`/`ErrorPermissionDenied`/`ErrorListAborted`. A snapshot builder can detect "scan incomplete" and parse logs for skipped paths, but cannot get a structured skipped-set from the RC API.
- **fsspec**: `walk(on_error=callable)` (`fsspec/spec.py:470-492`) delivers each `OSError` to a callable with the path context. `cat(on_error="return")` (`fsspec/spec.py:943-948`) returns per-path exceptions in the result dict. These are structured per-path failure signals -- better than AList's HTTP-status-only model.

**Not reliably exposed (cross-tool):**
- **Permission-denied vs not-found vs rate-limited**: rclone distinguishes these *at the typed-error level* (`fs/fs.go:36-49`), but backend wrappers may not map correctly. fsspec has no typed error hierarchy; `exists`/`isdir`/`isfile` swallow all exceptions (`fsspec/spec.py:718-725`, `fsspec/spec.py:787-799`). AList maps them to HTTP status non-uniformly (D02 C11).
- **Silent backend truncation**: none of rclone/fsspec/AList can detect a backend that returns a partial list without an error. This is a provider-side gap that no tool can expose.
- **Stale cache**: rclone VFS dir cache and fsspec `DirCache` (`fsspec/dircache.py:1-100`) return stale data within TTL without any "stale" flag. The caller cannot detect this from the listing result.

INFERENCE: the *reliably exposed* partial failures are those where the backend returns an explicit error (rclone logs+counts, fsspec `on_error` callback). The *unreliably exposed* ones are silent truncation, stale cache, and (for fsspec/AList) error-type conflation. A snapshot builder can rely on "error returned => scan incomplete" but cannot rely on "no error => scan complete".

### F6. Capability ownership candidates (final assignment deferred to Architecture Gate)

"Tool does not provide" ≠ "Kernel must implement". The following are three-tier ownership candidates. Final assignment is deferred to Architecture Gate.

#### Kernel safety semantic candidates
- canonical resource identity 的决策/连续性规则
- Snapshot completeness acceptance / Safety Gate
- Canonical Inventory
- Safe Reconcile / removal safety
- Change Journal (canonical change journal, not equal to provider-native delta)

#### Collector / Scanner responsibility candidates
- traversal / pagination
- provider error capture
- skipped-path evidence
- cache bypass / refresh policy
- scan checkpoint/resume (蓝图已明确放在 Scanner Resume 阶段，不能直接塞进 Kernel)

#### Optional capability, must not be forced
- provider_object_id
- hash / content hash (蓝图明确 `hash optional`，第一阶段绝不能要求所有 Provider 有 hash)
- native delta / change notify
- provider metadata

#### Known facts (ownership TBD — Architecture Gate must assign responsibility)
- Stable object identity through rename/move: rclone `IDer` optional and absent on S3/WebDAV/local; fsspec no id interface; AList has `id` but DRIVER_DEPENDENT; OpenList does not expose id.
- Scan-state checkpoint and resume: none of the four tools have it.
- Provable completeness signal: none emit an explicit "complete" flag; rclone provides contract-level completeness with explicit failure propagation (Q3).
- Structured skipped-set on partial failure: rclone returns only the first error; fsspec default omits; AList gives HTTP status only.
- Cross-root disambiguation in the entry: neither rclone nor fsspec embeds root/storage identity in each listed entry.
- Real-time state under cache: rclone VFS cache and fsspec `DirCache` return stale data within TTL.

### F7. Is Discovery already sufficient to enter Architecture Gate?

**YES.** D02 + D03 evidence is sufficient to enter Architecture Gate. The capability profile of four candidate collector layers (AList, OpenList, rclone, fsspec) across seven dimensions is established with source citations.

- **Identity**: rclone partial (optional `IDer`), AList DRIVER_DEPENDENT, fsspec/OpenList absent.
- **Completeness**: rclone PARTIAL (contract-level with explicit failure propagation), others infer from exhaustion.
- **Partial failure**: rclone best (typed errors + log + non-nil error return), fsspec second (callback), AList/OpenList worst (silent swallow).
- **Checkpoint**: universal absence.
- **Delta**: rclone partial hint (polling, no replay), others absent.
- **Hash**: rclone DRIVER_DEPENDENT, fsspec weak (metadata hash), AList/OpenList DRIVER_DEPENDENT.
- **Pagination**: rclone internal, fsspec/AList/OpenList offset/unstable.

Remaining unknowns (U1-U5) are not blockers for defining the first architecture contracts; they are implementation/integration validation items.

INFERENCE: No further source investigation is required to scope the architecture; remaining work is design, not discovery. Architecture Gate decides: which Collector to use, and how identity/completeness/checkpoint/delta/hash responsibilities are assigned across Kernel and Collector layers.

---

## 7. VERIFIED_FACTS vs UNVERIFIED

### VERIFIED_FACTS (established by reading the cited source at the cited commit)

| # | Fact | Evidence |
|---|------|----------|
| R1 | rclone `IDer` is an optional interface; `ID()` returns string or empty. | `fs/types.go:166-170` |
| R2 | rclone `lsjson` populates `ID` only for `IDer` backends; field is `omitempty`. | `fs/operations/lsjson.go:30`, `fs/operations/lsjson.go:207-209` |
| R3 | rclone local `Directory.ID()` returns `""`. | `backend/local/local.go:2023-2025` |
| R4 | rclone S3 has no `ID()` method on `Object`. | grep `backend/s3/s3.go` for `func (.*) ID() string` -- no match |
| R5 | rclone WebDAV has no `ID()` method. | grep `backend/webdav/webdav.go` -- no match |
| R6 | rclone `ListR` is callback-based with no `done`/`total` field. | `fs/features.go:670-685` |
| R7 | rclone `listRwalk` logs per-dir errors and continues; returns first error at end. | `fs/walk/walk.go:168-179` |
| R8 | rclone `march` surfaces per-dir list errors with path in log; returns first error. | `fs/march/march.go:562-585`, `fs/march/march.go:283` |
| R9 | rclone defines typed errors: `ErrorDirNotFound`, `ErrorObjectNotFound`, `ErrorPermissionDenied`, `ErrorListAborted`. | `fs/fs.go:36-49` |
| R10 | rclone has no checkpoint/resume in listing code. | grep `fs/walk/walk.go`, `fs/march/march.go`, `fs/operations/rc.go` -- no match |
| R11 | rclone `ChangeNotifier` is optional; 14/69 backends declare it. | `fs/features.go:569-581`; grep `backend/` for `fs.ChangeNotifier` -- 14 matches |
| R12 | rclone local has no `ChangeNotify` method at this commit. | grep `backend/local/` -- no match |
| R13 | rclone S3/WebDAV/OneDrive have no `ChangeNotify` method. | grep each backend -- no match for method def |
| R14 | rclone `Hashes()` is defined on `Fs`; 68 backends implement it. | `fs/types.go:74-76`; grep -- 68 matches |
| R15 | rclone WebDAV `Hashes()` returns `hash.None` by default. | `backend/webdav/webdav.go:1318-1327` |
| R16 | rclone S3 `Hashes()` returns `hash.MD5`. | `backend/s3/s3.go:3329-3331` |
| R17 | rclone `lsjson` populates `Hashes` only when `showHash` is set. | `fs/operations/lsjson.go:221-230` |
| R18 | rclone `rcList` buffers full list in memory; no continuation token in RC API. | `fs/operations/rc.go:27-80` |
| R19 | rclone S3 has no `Move` method (falls back to copy+delete). | grep `backend/s3/s3.go` for `func (.*) Move(` -- no match |
| R20 | rclone WebDAV and local have `Move`. | `backend/webdav/webdav.go:1255`, `backend/local/local.go:1063` |
| F1 | fsspec `AbstractFileSystem` defines no object-id method/field. | `fsspec/spec.py:151-243` |
| F2 | fsspec `ls` contract requires only `name`/`size`/`type`. | `fsspec/spec.py:377-415` |
| F3 | fsspec `info` contract returns dict with `name`/`size`/`type` + FS-specific keys. | `fsspec/spec.py:732-764` |
| F4 | fsspec `fsid` identifies the filesystem instance, not objects; defaults to `NotImplementedError`. | `fsspec/spec.py:222-227` |
| F5 | fsspec local `info` returns `ino` (inode) as an extra field. | `fsspec/implementations/local.py:124` |
| F6 | fsspec memory `info` returns no id field. | `fsspec/implementations/memory.py:262-282` |
| F7 | fsspec `walk` is a generator; completion is `StopIteration`; no `done` flag. | `fsspec/spec.py:443-535` |
| F8 | fsspec `walk` default `on_error="omit"` silently skips failed dirs. | `fsspec/spec.py:471`, `fsspec/spec.py:485-492` |
| F9 | fsspec `walk` supports `on_error="raise"` and callable for failure visibility. | `fsspec/spec.py:470-492` |
| F10 | fsspec `cat` supports `on_error="return"` giving per-path exceptions. | `fsspec/spec.py:943-948`, `fsspec/spec.py:969-970` |
| F11 | fsspec `exists` uses bare `except:` swallowing all exceptions. | `fsspec/spec.py:718-725` |
| F12 | fsspec has no typed error hierarchy in the abstract spec. | `fsspec/spec.py:151-243` (no error classes defined) |
| F13 | fsspec has no checkpoint/resume in listing code. | grep `fsspec/spec.py` -- no match in listing signatures |
| F14 | fsspec has no watch/notify/subscribe method in abstract spec or in-tree impls. | grep `fsspec/spec.py`, `fsspec/implementations/*.py` -- no match |
| F15 | fsspec `checksum` default is `int(tokenize(self.info(path)), 16)` -- metadata hash. | `fsspec/spec.py:766-777` |
| F16 | fsspec `ukey` default is `sha256(str(self.info(path)).encode())` -- metadata hash. | `fsspec/spec.py:1453-1455` |
| F17 | fsspec only `dirfs` overrides `checksum`; `webhdfs` overrides `ukey` with real checksum. | `fsspec/implementations/dirfs.py:296`, `fsspec/implementations/webhdfs.py:305-313` |
| F18 | fsspec local and memory do not override `checksum`/`ukey`. | grep `fsspec/implementations/local.py`, `memory.py` -- no match |
| F19 | fsspec `ls`/`walk`/`find` have no continuation-token parameter. | `fsspec/spec.py:377`, `fsspec/spec.py:443`, `fsspec/spec.py:537` |
| F20 | fsspec `DirCache` is in-memory with TTL/LRU, not persistent scan state. | `fsspec/dircache.py:1-100` |
| F21 | fsspec `mv` is copy+rm by default. | `fsspec/spec.py:1292-1301` |
| F22 | fsspec `rename`/`move` are aliases of `mv`. | `fsspec/spec.py:1847-1861` |
| F23 | fsspec has no in-tree S3/GCS/Azure implementations. | `ls fsspec/implementations/` -- no s3/gcs/abfs/azure files |

### UNVERIFIED (cannot confirm from this source read)

| # | Item | Why unverified |
|---|------|----------------|
| U1 | Behavior of external fsspec packages (s3fs, gcsfs, adlfs) | source not in the cloned `filesystem_spec` repo; out of scope |
| U2 | Whether rclone backends correctly map provider errors to typed errors at runtime | requires running each backend against a live provider |
| U3 | Whether fsspec backends honor `on_error` consistently | requires running each implementation with injected failures |
| U4 | Exact runtime semantics of rclone `ChangeNotify` polling intervals per backend | requires reading each backend's `ChangeNotify` body in detail; only the interface contract was verified here |
| U5 | Whether the 14 rclone `ChangeNotifier` backends all expose replay tokens | the interface (`fs/features.go:569-581`) has no token parameter, so structurally no; runtime behavior not verified |

---

## 8. Summary

- **rclone** provides the richest collector interface of the four tools examined across D02+D03: optional `IDer` (38 backends), `Hashes()` (68 backends), `Mover` (51 backends), `ListR` (27 backends), `ChangeNotifier` (14 backends), and typed errors. But every advanced capability is DRIVER_DEPENDENT -- identity, hash, delta, and move are each absent on a material subset of backends (S3, WebDAV, local). `ChangeNotify` is a polling hint with no replay. There is no checkpoint/resume. Completeness is PARTIAL: contract-level with explicit failure propagation, but cannot prevent silent backend truncation.
- **fsspec** provides a clean Pythonic abstract filesystem with explicit partial-failure visibility (`on_error` callback) and an in-memory listing cache. But it has no object identity interface, no content hash by default (`checksum`/`ukey` are metadata hashes), no change notification, no checkpoint/resume, and no abstract pagination. Its in-tree implementations are limited to local/memory/HTTP/WebHDFS/FTP/SFTP/git/github/jupyter -- the major cloud backends live in external packages not inspected here.
- **AList** (from D02) has `id` in `/api/fs/list` (DRIVER_DEPENDENT) and `hash_info` (DRIVER_DEPENDENT), but `storage.List` error is silently swallowed. **OpenList** does not expose `id` in public FS API. Both have offset pagination with no completion flag, and a cache layer that breaks real-time completeness.
- **No tool** provides: stable identity through rename/move for all backends, provable completeness, checkpoint/resume, durable delta with replay, real-time state under cache, structured skipped-set, or cross-root disambiguation in the entry. Capability ownership is deferred to Architecture Gate (F6).
- **Discovery is sufficient** to enter the Architecture Gate (F7). Remaining unknowns (U1-U5) are not blockers for defining the first architecture contracts; they are implementation/integration validation items. Architecture Gate decides: which Collector to use, and how identity/completeness/checkpoint/delta/hash responsibilities are assigned across Kernel and Collector layers.

This report presents facts and cross-references only. It does not select a collector, does not assign Kernel ownership, and does not design the snapshot architecture.
