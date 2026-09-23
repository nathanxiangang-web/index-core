# Collectors

Collectors acquire external resource facts and normalize them into Snapshot evidence.

They do **not** write Canonical Inventory directly. Identity, completeness acceptance, removal decisions, reconcile, generation, and Journal are Kernel responsibilities.

Runtime collector kinds currently supported:

- `rclone`
- `alist`
- `openlist`

## Safety posture

The current rclone and AList/OpenList runtime collectors are **additive-safe by default**.

They do not provide the positive skip/completeness evidence required to authorize destructive-safe COMPLETE reconciliation.

Practical consequence:

> A successful ordinary scan may add/update canonical resources, but absence from an rclone/AList/OpenList scan is not automatically proof of deletion.

Do not "fix" this in a consumer.

---

# rclone

IndexCore runs rclone as an external process using:

```text
rclone lsjson --recursive <target>
```

The Docker image bundles rclone. Host deployments may set:

```bash
export INDEXCORE_RCLONE_PATH=/usr/local/bin/rclone
export INDEXCORE_RCLONE_CONFIG=/path/to/rclone.conf
```

## Local filesystem

```bash
indexcore root adapter set \
  --root-id "$ROOT_ID" \
  --collector rclone \
  --config '{"remote":"","path":"/srv/media"}'
```

With an empty `remote`, `path` is passed as a local filesystem path.

## Named remote

```bash
indexcore root adapter set \
  --root-id "$ROOT_ID" \
  --collector rclone \
  --config '{"remote":"myremote","path":"/archive"}'
```

This scans:

```text
myremote:/archive
```

Provider object IDs and hashes emitted by rclone are treated as evidence, not automatically as canonical identity.

---

# AList / OpenList

IndexCore walks the tree through the AList/OpenList HTTP API.

Supported collector kind names:

```text
alist
openlist
```

## Token mode

Store only the **environment variable name** in the adapter config:

```bash
export ALIST_TOKEN='actual-secret-token'

indexcore root adapter set \
  --root-id "$ROOT_ID" \
  --collector alist \
  --config '{"base_url":"http://127.0.0.1:5244","path":"/","token_env":"ALIST_TOKEN"}'
```

At scan time IndexCore resolves `ALIST_TOKEN` from the runtime environment.

## Username/password login

```bash
export ALIST_USERNAME='admin'
export ALIST_PASSWORD='secret'

indexcore root adapter set \
  --root-id "$ROOT_ID" \
  --collector openlist \
  --config '{"base_url":"http://127.0.0.1:5244","path":"/data","username_env":"ALIST_USERNAME","password_env":"ALIST_PASSWORD"}'
```

The adapter logs in through `/api/auth/login` and then enumerates `/api/fs/list`.

## Secret rule

Do not persist plaintext provider secrets in the adapter JSON.

Persist environment-variable references such as:

- `token_env`
- `username_env`
- `password_env`

and inject the actual values at runtime.

## Pagination / truncation

The AList/OpenList adapter paginates directory listings and verifies that collected item count matches the server-declared total. A truncated listing fails closed rather than being treated as a complete successful list.

Even so, the collector remains additive-safe because it cannot positively prove all skipped scopes are empty.

---

# Adding another Collector

A new provider integration should implement the Collector boundary, not enter Kernel code.

The required conceptual output is normalized evidence:

```text
entries
traversal status
skipped-scope evidence
freshness
failure visibility
provider identity assurance
```

Do not make a vendor field mandatory merely because one provider supports it.

Architecture contract:

`docs/architecture/GATE1C-COLLECTOR-ADAPTER-CONTRACT.md`
