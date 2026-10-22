---
type: proposal
tool: kilo-proxy
status: planning
owner: sergio-durban
author: murad-madi
date: 2026-10-22
revision: 2 (control plane — supersedes the hub-and-spokes draft)
tags: [kilo-proxy, mcp, proposal]
---
# Kilo Proxy — MCP setup

> Design proposal for a new **MCP** tab in Kilo Proxy: curated catalog, per-user auth, and
> one-click config fan-out to every harness. Repo: `/Users/murad.madi/Projects/kilo-proxy`
> (v0.56.2, Go 1.26). Staging copy — belongs in the repo as `docs/mcp.md`.
>
> **Rev 2 change.** Rev 1 proposed a hub: Kilo Proxy runs an aggregating MCP server and every
> harness points at it. That was over-engineered. Kilo Proxy is now a **control plane, not a
> data plane** — it configures harnesses and hands out credentials, then gets out of the way.
> Every harness calls each upstream MCP directly. See §2 for what that removes.

## 1. Goal

A user opens Kilo Proxy, goes to **MCP**, searches a LeadTech-curated list, toggles the
servers they want, authenticates each **once**, then syncs every harness config at once.

| Beat | Behaviour |
|---|---|
| Tab | New `MCP` nav entry beside Agents / Models / Activity / Settings |
| Browse | Search bar over the curated catalog, cards with state + auth badges |
| Toggle | Per-MCP on/off |
| Authenticate | Per-user OAuth (or token / none), held in the credential vault |
| Sync | Read every harness config, back up, rewrite only the MCP section |

## 2. Core idea: control plane, not hub

```
        ┌──────────── kilo-proxy (control plane) ────────────┐
        │  MCP tab:  catalog → toggle → authenticate → sync   │
        │  credentialVault   ·   per-harness config writers   │
        └───────────┬─────────────────────────────┬───────────┘
                    │ writes config + credential  │
                    ▼                             ▼
              harness A                      harness B
                    │                             │
                    └──────────► upstream MCPs ◄──┘
                       (direct, no intermediary)
```

Nothing is proxied at runtime. Each harness talks straight to Supermetrics, Amplitude, Airtable,
etc. Kilo Proxy's jobs end when the configs are written.

**What this deletes from rev 1** — the entire aggregator: upstream MCP client, per-upstream
sessions, `tools/list` merging, `tools/call` routing, protocol negotiation, error mapping,
concurrency limiting.

| Win | Cost |
|---|---|
| **No daemon dependency** — closing Kilo Proxy no longer breaks every MCP | **No tool-level filtering** — configs are all-or-nothing per server |
| Far less code; no aggregation or sessions | **No transport adaptation** — a harness that can't do remote HTTP can't use this |
| Failure isolation; no added latency | **Secret sprawl** — credential in N configs + N backups, vs 1 vault copy |
| **Tool names unchanged → the naming problem disappears; no refactor** | **No central call audit** (loses an Activity-tab angle) |
| Upstream protocol changes never touch Kilo | |

## 3. Decisions locked

| # | Decision | Consequence |
|---|---|---|
| 1 | Acting as the models proxy is fine | No product-scope objection |
| 2 | `headlessVault` private-files storage accepted | Reuse `credentialVault` as-is; no new crypto |
| 3 | Migration = cleanup/reconcile task | Read all harness configs → back up → rewrite MCP section only |
| 4 | Ship with filtering; keep it light | Achieved via **scoped tokens** (§7), not tool lists |
| 5 | Namespacing is a big refactor | **Now moot** — no hub means no tool renaming |
| 6 | **Per-user** OAuth, persistent config | No shared team token; each user authenticates their own |
| 7 | LeadTech curated list only | No Docker/community registry; one curated catalog |

## 4. The hard problem: OAuth tokens expire

This is the one thing that constrains the whole design.

A config file holds a **static string**. If Kilo Proxy performs one OAuth and writes the
resulting access token into harness configs, **every harness breaks when it expires** (≈1 hour,
typical). Harnesses read config at startup, so a running one will not even see a rewritten value.

Today's audit splits the catalog in half:

| MCP | Auth | Plain "write the secret" works? |
|---|---|---|
| Supermetrics | OAuth, **but issues long-lived `api_*` keys** | ✅ via API key |
| AppsFlyer | long-lived static bearer | ✅ |
| SignalBeacon | header / local | ✅ |
| Amplitude | OAuth | ❌ expires |
| Airtable / RevenueCat | OAuth | ❌ expires |

So the `token` half is clean, and the OAuth half needs a delivery mechanism. §5 provides four.

## 5. Delivery modes

The UX stays identical (toggle → authenticate → sync). The **delivery mechanism is an
implementation detail behind it**, chosen per MCP.

| Mode | Mechanism | Secret in config | Refresh | Use for |
|---|---|---|---|---|
| **A. Literal** | Write URL + secret into the config | yes (plaintext) | never | Long-lived keys: AppsFlyer, Supermetrics `api_*`, SignalBeacon |
| **B. Env at launch** | Config names an env var; Kilo injects it when *it* launches the harness | no | at launch | OAuth. Codex supports `bearer_token_env_var` |
| **C. Helper command** | Config points at `kilo-proxy --mcp-headers <id>`; helper reads the vault and refreshes | no | live | OAuth on harnesses Kilo does not launch |
| **D. Hub** | The rev-1 aggregator | no | live | **Only** if tool filtering / transport / audit becomes a hard requirement |

**P0 ships A + B.**

- **A** covers roughly half the catalog today with near-zero code.
- **B** delivers the "authenticate once" promise for OAuth **without a running daemon** — Kilo
  performs the OAuth, stores the token, and injects a fresh value each time it launches the
  harness. `codex_config.go:92` already manages Codex's `http_headers` and `env_http_headers`.
- **C** is the escape hatch for harnesses launched outside Kilo. Codex already exposes an
  `http_headers_helper` field for exactly this.
- **D** is explicitly deferred; revisit only on a concrete requirement.

**Honest trade-off on A.** Mode A writes plaintext secrets into configs — the same problem
visible today with the AppsFlyer `Bearer eyJ…` sitting in `~/.codex-kilo-desktop/config.toml`.
It is acceptable for long-lived keys because there is no alternative, but it means the secret
also lands in every backup. Consider preferring `api_*`-style keys wherever an upstream offers
one (Supermetrics does) so mode A stays confined to keys that are revocable and scoped.

## 6. Auth and persistence

| Kind | Flow | Vault slot |
|---|---|---|
| `oauth` | loopback callback on 127.0.0.1, PKCE S256, refresh | `mcp/<id>/oauth` |
| `token` | paste → validate via `initialize` + `tools/list` → store | `mcp/<id>/token` |
| `none` | nothing | — |

**Client identity.** MCP OAuth needs one, and the redirect port is user-configurable
(`connection.port`, default 8877), which breaks a statically registered client_id:

1. **Preferred — CIMD:** Kilo hosts one client-metadata document; all users share the client
   identity, each user holds their own token. Supermetrics advertises CIMD support.
2. **Fallback — DCR:** register per install (`/oauth/register`); noisier but portable.

Both must be validated per upstream (§11) before P1 closes.

**Per-user auth (decision 6).** Each user authenticates their own account. Toggling is a
persistent, user-owned config — so it must live outside the app bundle.

**Persistence follows the distribution model.** Users update by replacing the `.app`, so
anything embedded dies on update:

| Artifact | Location | Survives update | Curated by |
|---|---|---|---|
| `mcp-catalog.json` (default) | `//go:embed` | ❌ replaced | LeadTech, via release |
| `recommended-mcps.json` (refresh) | fetched, like `recommended-models.json` | ❌ replaced | LeadTech, **no release needed** |
| `mcp-state.json` (toggles, mode) | `os.UserConfigDir()/kilo-proxy` | ✅ | the user |
| OAuth tokens / keys | keychain · `headlessVault` | ✅ | the user |

Mirrors the existing pair: `model-packs.json` (embedded, `model_packs.go:20`) and
`recommended-models.json` (embedded, `recommended_models.go:22`), with user state in the config
dir beside `settings.json` and `models.json`.

> **Recommendation:** make the curated list **remotely refreshable *and* embedded**. Embedded
> only means every new MCP needs a release plus every user manually re-dragging the app. The
> `recommended-models.json` fetch path already proves the pattern — reuse it.

**No re-auth on update** — credentials live in the vault, not the bundle. Invalidate only if
the OAuth client identity itself changes.

## 7. Filtering without a hub (decision 4)

Rev 1 met "keep it light" with per-client tool lists — only possible behind an aggregator.
Config-file fan-out is all-or-nothing per server, so the equivalent levers are:

1. **Curated catalog** — only LeadTech-approved MCPs are offered. This is the primary filter
   and it is free.
2. **Scoped tokens** — issue the narrowest credential the upstream supports. Supermetrics
   exposes `ds_platform_write`; a read-only token handed to CI restricts capability **at the
   token level**, which is arguably stronger than tool filtering and works in design B.
3. **Selective sync** — per harness, choose which enabled MCPs are written. A CI harness gets
   three; an interactive one gets all.
4. **Tool budget warning** — surface enabled tool counts per harness and warn past a threshold,
   so a user sees when they are bloating a session's tool list.

Items 1 and 3 depend purely on our own data. Item 2 depends on upstream scope support and needs
per-upstream validation.

## 8. Migration — the cleanup pass (decision 3)

One explicit action, never silent at launch:

1. **Discover** installed harnesses (reuse `installedClaude()` and the detection in `native_clients.go`).
2. **Read** each config; parse its MCP section; record foreign entries.
3. **Back up** each original to a timestamped sibling.
4. **Write** with **only the MCP section changed** — every other key preserved byte-for-byte.
5. **Report** per harness: changed / skipped / failed, with the backup path.

Requirements, matching existing conventions:

- Preserve **foreign** MCP entries; never clobber them — `codex_images.go:66` already refuses
  and says so.
- Abort that harness cleanly on parse failure: write nothing, explain why.
- Roll back on a partial write; never leave a half-file.
- Reuse `toml_merge.go`, `hujson`, and the existing `json.RawMessage` merge style.

## 9. Fan-out matrix

| Harness | Target | Format | State |
|---|---|---|---|
| Codex / Codex CLI | `config.toml` | TOML `mcp_servers` | ✅ merge exists |
| Oh My Pi | `~/.omp-kilo/mcp.json` | JSON `mcpServers` | ✅ merge exists |
| OpenCode / Open Design | config | HUJSON `mcp` | ✅ merge exists |
| **Claude Code** | `.claude.json` → `mcpServers` | JSON | ❌ **nothing writes it today** |
| Claude Desktop | connector gateway | — | ❌ |
| Zed | settings JSON | JSON | ❌ |
| T3 Code · Synara · OpenMausBot · Xcode | — | TBD | ❌ |

Claude Code is the notable gap, and the one that required hand-editing in practice.
`.claude.json` is large and holds unrelated user state, so it needs read-modify-write with full
key preservation — or project-scoped `.mcp.json` (safer, but per-project).

**Mode support per harness.** Modes A and B are not uniformly available; this needs confirming
per harness and is a P0 deliverable:

| Harness | Mode A | Mode B |
|---|---|---|
| Codex / Codex CLI | ✅ | ✅ `bearer_token_env_var`, `env_http_headers` |
| Claude Code | ✅ | ⚠️ env-injected via `settings.json` `env` block — confirm on launch |
| Oh My Pi · OpenCode · Zed · T3 | ✅ | ⚠️ confirm |

## 10. Phasing

| Phase | Scope | Unlocks | Risk |
|---|---|---|---|
| **P0** | catalog + tab shell + toggle + `token`/`none` auth + fan-out via **modes A + B** to the 3 existing writers; selective sync | Full loop end-to-end, **no hub** | low |
| **P1** | vault integration + `oauth` (PKCE, CIMD/DCR, loopback) + mode B token injection | The actual "authenticate once" | medium |
| **P2** | `oauth` for the remaining upstreams + scoped-token support + tool-budget warnings | Catalog coverage | medium |
| **P3** | Remaining harnesses + cleanup pass + `recommended-mcps.json` + MCP packs (Martech / Growth / Eng) | LeadTech-wide default | medium |
| **deferred** | Mode C (header helper), mode D (hub) | Only on a concrete requirement | — |

P0 proves the loop with a pasted long-lived token — Supermetrics `api_*` and the AppsFlyer
bearer both work today — before any OAuth complexity lands.

## 11. Validation tasks

1. Does each OAuth upstream (Supermetrics, Amplitude, Airtable, RevenueCat) accept **CIMD** with
   a loopback redirect on an arbitrary port, or is **DCR** required?
2. Does any upstream reject a shared client identity used by many users?
3. OAuth refresh-token lifetime; behaviour when the port changes.
4. Which upstreams support **scoped tokens** (needed for §7 item 2)?
5. Does each harness support mode B, and how exactly is the env var read at launch (§9)?
6. Per-upstream tool counts, to calibrate the tool-budget warning.

## 12. Accepted risks

| Risk | Status |
|---|---|
| Product scope: proxy → MCP manager | accepted (decision 1) |
| `headlessVault` is files, not encrypted | accepted (decision 2) |
| Breaking change for hand-written MCP entries | handled by the cleanup pass (§8) |
| Tool bloat | handled by curation + selective sync + budget warning (§7) |
| Namespace churn | **eliminated** — no hub, no renaming (decision 5) |
| Plaintext secrets where mode A applies | accepted; confined to long-lived, revocable, scoped keys (§5) |
| Per-user auth on each new machine | inherent to decision 6; mitigated by the remote catalog |
