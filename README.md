# Siegeproof

> **Dynamic security fuzzer, compliance scorer, and auto-hardening tool for Model Context Protocol (MCP) servers.**
> Break your MCP server before someone else does, then ship the config that stops them.

Siegeproof connects to a live MCP server, generates adversarial payloads from its *actual* tool schemas, and answers two questions: **how badly can it be broken**, and **exactly what should you deploy so it can't be**. It ships as a single static binary built for CI/CD gates and locked-down build agents: no interpreter, no `pip install`, no runtime to match.

> **Status: early-stage, solo-built, pre-release.** This README describes the target design. See [Status](#status) and the [Roadmap](#roadmap) for what is implemented versus planned.

---

## Contents

- [At a Glance](#at-a-glance)
- [Why This Exists](#why-this-exists)
- [How It Compares](#how-it-compares)
- [Status](#status)
- [Design Principles](#design-principles)
- [How It Works](#how-it-works)
- [Threat Model and Scope](#threat-model-and-scope)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Production Guide](#production-guide)
- [CLI Reference](#cli-reference)
- [Payload Taxonomy](#payload-taxonomy)
- [Reports](#reports)
- [Troubleshooting and FAQ](#troubleshooting-and-faq)
- [Roadmap](#roadmap)
- [Non-Goals (v1)](#non-goals-v1)
- [Responsible Use](#responsible-use)
- [Security Policy](#security-policy)
- [Contributing](#contributing)
- [License](#license)

---

## At a Glance

```text
$ siegeproof scan --config siegeproof.yaml --intensity medium
siegeproof 0.1.0 · corpus 2026.09.1 · scoring model v1 · seed 41827
target    stdio:python server.py — 3 tools, 1 resource, 0 prompts
coverage  3/3 tools · 5/6 families · 1,842 requests · 6m12s · complete

 CRITICAL  F-0002  run_command   OS command injection (CWE-78)          deterministic
 HIGH      F-0001  read_file     Path traversal, canary read (CWE-22)   deterministic
 MEDIUM    F-0004  search_docs   Stack trace in error response (CWE-209) deterministic
 MEDIUM    F-0006  fetch_url     Possible SSRF via redirect (CWE-918)   probabilistic
 LOW       F-0005  send_email    No per-session call limit (CWE-770)    deterministic

score 38/100 (capped at 40 by F-0002) · threshold 80 · FAIL
report reports/scan.json · next: siegeproof harden --from reports/scan.json
```

*Illustrative output. The final format may change before 1.0.*

**What you get from one run:**

| Output | Purpose |
|---|---|
| Findings with CWE-mapped severity and replayable evidence | Tells engineers what is broken and proves it |
| A 0–100 compliance score | A single number a pipeline can gate on |
| A hardened policy file (`siegeproof.hardened.yaml`) | Tells operators what to deploy in front of the server today |
| SARIF and JSON reports | Feeds code-scanning UIs, ticketing, and dashboards |

---

## Why This Exists

The Model Context Protocol has become a default way to connect LLMs to internal databases, developer tools, and enterprise services. Every server that implements it is a new, mostly untested attack surface:

- **Prompt injection through tool descriptions.** Hidden instructions in metadata that the model reads as trusted context.
- **Cross-tool contamination.** One tool's metadata hijacking another tool's call.
- **Unescaped parameters** reaching a shell, a filesystem path, or an outbound HTTP client.
- **Authorization boundaries that only exist in a docstring**, not in code.

Public scans and security research on the MCP ecosystem have repeatedly found path traversal, command and code injection, and internet-exposed servers running with no authentication. <!-- TODO: add citations for any specific figures you want to quote here. -->

Dedicated MCP security tooling is still sparse relative to the number of servers already in production. That gap is what Siegeproof targets.

## How It Compares

Open-source dynamic fuzzers for MCP already exist, notably `mcp-fuzzer` (protocol- and schema-aware mutation across stdio/HTTP/SSE) and `mcpsec` (live exploitation, prompt-injection and auth scanners, AI-generated payloads, SARIF export). Siegeproof is not the first tool in this space and does not try to out-fuzz them feature for feature. It targets three gaps neither one currently fills:

1. **A compiled, dependency-free binary.** No interpreter version to match and no network access at scan time beyond the target itself. Built for CI runners and locked-down build agents.
2. **Auto-remediation, not just a report.** A findings report is a to-do list. Siegeproof's primary output is a hardened policy (tool allowlist, per-parameter validators, rate limits) you can deploy in front of the server, plus a way to verify it worked.
3. **Fleet-level compliance scoring.** One score per server, aggregable across an organization's MCP inventory, designed to be a gate condition rather than a document a human reads once.

**Choose the existing tools if** you need the broadest payload coverage or AI-generated payloads *today*. **Choose Siegeproof if** you need a hermetic binary in CI, a deployable fix, and a gateable score. The tools are complementary; nothing stops you running both.

## Status

Early-stage, solo-built, pre-release. Check `CHANGELOG.md` (once it exists) for what is actually implemented versus planned. Caveats that apply from day one:

- **Only test servers you own or are explicitly authorized to assess.** Fuzzing is not implicit consent.
- **Deterministic findings** (crashes, error leakage, auth bypass, canary reads) are reliable from the start. **LLM-judged findings** (subtle cross-tool contamination) are probabilistic and labeled as such in every report.
- **The hardened-policy generator ships after the scoring engine** (Phase 5). It is not in the MVP.

## Design Principles

These decisions shape everything below. They are what make the tool safe to put in a pipeline.

1. **Fail closed.** An incomplete scan never returns success. If the time budget runs out, the target dies, or coverage falls below the configured minimum, the exit code says so.
2. **Deterministic first.** A baseline score needs no LLM. Probabilistic findings are labeled, discounted in scoring, and never trigger score caps on their own.
3. **Evidence over opinion.** Every finding carries the request, the response excerpt, and a replay handle. A finding you cannot reproduce is a rumor.
4. **Safe by default.** Payloads read canaries, echo tokens, and measure timing. They never delete, encrypt, or exfiltrate real data. Side-effecting tools are skipped unless you opt in.
5. **Reproducible.** Every run records the seed, corpus version, scoring model version, and tool version, so a score change can always be explained.
6. **Treat the target as hostile.** The server under test can send anything back. Its output is parsed defensively and sanitized before it reaches your terminal or your logs.

---

## How It Works

```
┌──────────────────┐     ┌───────────────────┐     ┌────────────────────┐
│  MCP Client       │────▶│  Introspection     │────▶│  Payload/Mutation   │
│  (stdio/SSE/HTTP) │     │  Engine (schema    │     │  Engine             │
│                   │     │  normalization)    │     │                     │
└──────────────────┘     └───────────────────┘     └──────────┬─────────┘
                                                                │
                                                                ▼
┌──────────────────┐     ┌───────────────────┐     ┌────────────────────┐
│  Hardened Policy  │◀────│  Scoring &         │◀────│  Execution          │
│  Generator        │     │  Verdict Engine    │     │  Orchestrator       │
│                   │     │                    │     │  (sandboxed)        │
└────────┬─────────┘     └─────────┬─────────┘     └────────────────────┘
         │                          │
         ▼                          ▼
  siegeproof.hardened.yaml    SARIF / JSON report
```

**Scan lifecycle**

1. **Preflight.** Validate config, check the scope allowlist, verify the sandbox is available, start the canary infrastructure.
2. **Connect and introspect.** Enumerate tools, resources, and prompts; normalize their schemas.
3. **Plan.** Build a payload plan per tool from its declared schema, bounded by the intensity level and budget.
4. **Execute.** Run payloads concurrently, rate-limited, inside the sandbox. Stateful chains carry session state across calls.
5. **Judge.** The deterministic verdict engine classifies each response. Optional probabilistic checks run last and are labeled.
6. **Score.** Deduplicate findings, apply weights and caps, compute coverage.
7. **Report.** Write JSON and SARIF, including replayable evidence.
8. **Harden.** (Separate command.) Turn findings into a policy, then re-scan through the policy to verify.

## Threat Model and Scope

Siegeproof plays the role of a **hostile or manipulated caller**: an attacker, or an LLM steered by injected instructions, sending crafted tool calls to your server. It tests whether the server holds up.

**In scope**

| Area | Examples |
|---|---|
| Input handling | Type confusion, boundary values, malformed JSON-RPC, unexpected fields |
| Injection into downstream sinks | Path traversal, OS command and argument injection, SSRF |
| Tool metadata safety | Hidden instructions or encoded payloads in tool and resource descriptions; cross-tool contamination |
| Authentication and authorization | Missing auth, privilege boundaries between identities, escalation via tool chaining |
| Information disclosure | Stack traces, internal paths, secrets in error messages |
| Resilience | Behavior under sustained and parallel load, timeouts, missing rate limits |
| Transport hygiene | Cleartext HTTP endpoints, permissive CORS on HTTP transports |

**Out of scope**

- Vulnerabilities in the MCP *host* or *client* application, or in model behavior itself.
- Dependency and supply-chain analysis of the server's codebase (see [Non-Goals](#non-goals-v1)).
- Business-logic authorization that is not expressed through tools and identities you configure. Siegeproof cannot know that `reader` should not call `delete_document` unless you declare it (see [Authentication and Authorization Testing](#authentication-and-authorization-testing)).
- Anything a black-box scan cannot observe. A clean report is evidence, not proof.

**Finding classes**

| Class | Typical CWE | Verdict type |
|---|---|---|
| Path traversal / unauthorized file read | CWE-22 | Deterministic (canary token) |
| OS command injection | CWE-78 | Deterministic (echoed token, timing) |
| Argument injection | CWE-88 | Deterministic |
| Code injection | CWE-94 | Deterministic |
| Server-side request forgery | CWE-918 | Deterministic (out-of-band callback) |
| Missing authentication | CWE-306 | Deterministic |
| Missing / broken authorization | CWE-862 | Deterministic (given declared expectations) |
| Error-message information leak | CWE-209 | Deterministic |
| Improper input validation | CWE-20 | Deterministic |
| Missing resource limits / rate limiting | CWE-770 | Deterministic |
| Cleartext transmission | CWE-319 | Deterministic |
| Prompt injection via tool metadata | CWE-1427 | Probabilistic |
| Cross-tool contamination | CWE-1427 | Probabilistic |

---

## Installation

> Distribution channels below are the **target**; they go live as releases are published.

```bash
# Homebrew
brew install withbrian-technologies/tap/siegeproof

# Go
go install github.com/withbrian-technologies/siegeproof/cmd/siegeproof@latest

# Docker (pin by digest in CI; never use :latest in a gate)
docker pull ghcr.io/withbrian-technologies/siegeproof@sha256:<digest>
```

**Prebuilt binaries** for Linux, macOS, and Windows (amd64/arm64) are attached to each GitHub release, built by `goreleaser`, with checksums, an SBOM, and keyless signatures. Verify before you run them in CI:

```bash
sha256sum --check checksums.txt --ignore-missing

cosign verify-blob \
  --bundle siegeproof_linux_amd64.tar.gz.sigstore.json \
  --certificate-identity-regexp 'https://github.com/YOUR_ORG/siegeproof/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  siegeproof_linux_amd64.tar.gz
```

**Build from source** (Go 1.22+):

```bash
git clone https://github.com/withbrian-technologies/siegeproof && cd siegeproof
go build -trimpath -o siegeproof ./cmd/siegeproof
```

**Compatibility**

| | Support |
|---|---|
| Transports | stdio, streamable HTTP, SSE (kept for older servers) |
| MCP spec revisions | Matrix published after Phase 0; tracked in `docs/spec-coverage.md` |
| Sandbox modes | `docker` (Linux/macOS/Windows), `bwrap` (Linux), `none` |
| Windows | Binary supported; use `docker` sandbox mode for stdio targets |

Run `siegeproof doctor` after installing to check sandbox availability, canary-listener port binding, and DNS/egress behavior.

## Quick Start

```bash
# 1. Discover the target's attack surface (read-only, sends no payloads)
siegeproof discover --stdio "python my_server.py"

# 2. Run a baseline fuzz pass
siegeproof scan --stdio "python my_server.py" --intensity medium --report reports/scan.json

# 3. Gate CI: fail if the compliance score drops below 80
siegeproof scan --endpoint https://staging.example.com/mcp --min-score 80 --format sarif

# 4. Generate a hardened policy from the last scan
siegeproof harden --from reports/scan.json --out siegeproof.hardened.yaml

# 5. Re-scan through the policy to verify the fixes hold
siegeproof harden --from reports/scan.json --out siegeproof.hardened.yaml --verify
```

For anything beyond a first look, use a config file and read the [Production Guide](#production-guide).

---

## Production Guide

This section is the operator's manual: how to roll Siegeproof out, run it safely, wire it into CI, and turn findings into deployed defenses.

- [Rollout Plan](#rollout-plan)
- [Where and How to Run It](#where-and-how-to-run-it)
- [Configuration File](#configuration-file)
- [Scope Guard and Authorization Record](#scope-guard-and-authorization-record)
- [Authentication and Authorization Testing](#authentication-and-authorization-testing)
- [Side Effects and Destructive Tools](#side-effects-and-destructive-tools)
- [Deterministic Evidence: Canaries and Callbacks](#deterministic-evidence-canaries-and-callbacks)
- [Reproducibility and Replay](#reproducibility-and-replay)
- [Baselines and Suppressions](#baselines-and-suppressions)
- [Scoring](#scoring)
- [Hardening Workflow](#hardening-workflow)
- [CI/CD Integration](#cicd-integration)
- [Containers and Kubernetes](#containers-and-kubernetes)
- [Fleet Scoring](#fleet-scoring)
- [Triage and Ownership](#triage-and-ownership)
- [Securing the Scanner Itself](#securing-the-scanner-itself)

### Rollout Plan

Do not start with a hard gate. A gate that fails on day one gets disabled on day two. Ratchet instead:

| Stage | Goal | How | Gate |
|---|---|---|---|
| 0. Inventory | Know what you run | `discover` against every MCP server; record an owner and a tier for each | None |
| 1. Observe | Learn the baseline and the noise | Nightly scan in staging, report-only (`--fail-on none`) | None |
| 2. Block the worst | Stop critical regressions | PR gate at `--intensity low --fail-on critical` | Critical findings |
| 3. Gate on score | Raise the floor | `--min-score` set to today's score, raised as issues are fixed | Score |
| 4. Defend | Deploy hardened policies | Generate, review, run in `shadow` mode, then `enforce`; verify in CI | Score + verified policy |
| 5. Fleet | Organization-wide view | `fleet scan` across the inventory with per-tier thresholds | Per-tier score |

Suggested starting thresholds (adjust to your risk appetite):

| Tier | Description | `min_score` | `fail_on` |
|---|---|---|---|
| 1 | Internet-facing, or touches sensitive or regulated data | 90 | `high` |
| 2 | Internal, production-connected | 80 | `high` |
| 3 | Internal developer tooling, low blast radius | 70 | `critical` |

### Where and How to Run It

Fuzzing writes garbage into whatever the server is connected to, trips alerts, and fills logs. Run it accordingly.

- **Target a staging or ephemeral environment, never production data.** In CI, spin the server up per run with disposable backing services: a scratch database, an SMTP sink instead of a real mail relay, provider sandboxes instead of live APIs, throwaway buckets.
- **Use dedicated scanner identities** with the minimum privileges the test needs, rotated regularly. Never reuse a human's or a production service's credentials.
- **Isolate the network.** The scan runner should reach the target and nothing else. Block cloud metadata endpoints (for example `169.254.169.254`) from the target's network where you can, so an SSRF that does land cannot reach real credentials.
- **Run as an unprivileged user in a container.** See [Containers and Kubernetes](#containers-and-kubernetes).
- **Tell your SOC and on-call.** Siegeproof tags its traffic so alerts can be filtered: an HTTP header (`scan.tag_header`, default `X-Siegeproof-Run`) on HTTP transports and a `SIEGEPROOF_RUN_ID` environment variable for stdio children. Put scan windows on a shared calendar.
- **Set budgets.** Every scan has a maximum duration, request count, and rate. The defaults are conservative.

**If you must scan production** (not recommended): set `scope.environment: production`. Siegeproof then refuses to enable side effects, caps the rate, and only runs the read-only families. Use a low-privilege identity, an off-peak window, written authorization, and a person watching the dashboards.

### Configuration File

Flags are fine for experiments. For anything repeatable, commit a `siegeproof.yaml` next to the server's code.

```yaml
# siegeproof.yaml
version: 1

target:
  name: docs-search                   # used in reports and fleet inventory
  transport: stdio                    # stdio | http | sse
  command: ["python", "server.py"]    # stdio only
  working_dir: ./server
  env:                                # the ONLY environment the child process receives
    DATABASE_URL: "postgres://scanner:${SCANNER_DB_PASSWORD}@127.0.0.1:5432/scratch"
  # endpoint: https://staging.mcp.example.com/mcp   # use instead of `command` for http/sse
  # wait: 60s                         # wait for readiness before scanning

scope:
  environment: disposable             # disposable | shared-staging | production
  allow_hosts:                        # the scanner refuses to contact anything else
    - 127.0.0.1
    - staging.mcp.example.com
  authorization:                      # copied into every report for audit
    owner: platform-team
    reference: SEC-1234

scan:
  intensity: medium                   # low | medium | high
  families: [schema, injection, params, auth, chain, exhaustion]
  side_effects: skip                  # skip | allow (allow requires environment: disposable)
  seed: 41827                         # omit to randomize; always recorded in the report
  corpus: 2026.09.1                   # pin so scores stay comparable between runs
  tag_header: X-Siegeproof-Run
  min_coverage: 0.9                   # fraction of tools that must be exercised, else exit 2
  budget:
    max_duration: 15m
    max_requests: 20000
    rps: 5
    concurrency: 4
    call_timeout: 10s

sandbox:
  mode: docker                        # docker | bwrap | none (none needs --unsafe-no-sandbox)
  memory: 512m
  cpus: 1
  egress_allow: ["127.0.0.1:5432"]    # what the sandboxed server may reach

canaries:
  files: true                         # plant token files inside the sandbox (stdio targets)
  remote_paths: []                    # remote targets: [{path: /srv/canary.txt, token_env: CANARY_TOKEN}]
  oob:
    bind: 0.0.0.0:0                   # callback listener for SSRF / blind injection
    public_url: ""                    # URL the TARGET can reach; empty disables callback checks

identities:                           # see "Authentication and Authorization Testing"
  - name: reader
    token_env: TARGET_READER_TOKEN
  - name: admin
    token_env: TARGET_ADMIN_TOKEN
expectations:
  deny:
    reader: [delete_document, run_migration]

scoring:
  model: v1
  min_score: 80
  fail_on: high                       # none | low | medium | high | critical

baseline: .siegeproof/baseline.json
suppressions: .siegeproof/suppressions.yaml

report:
  formats: [json, sarif]
  dir: reports/
  redact_headers: [Authorization, Cookie]
  sarif:
    anchor_file: server/server.py     # see "Reports"
```

**Precedence:** CLI flags, then `SIEGEPROOF_*` environment variables, then the config file, then defaults. **Secrets never live in the file.** Use `${VAR}` interpolation or `*_env` references; a missing variable is a configuration error (exit 3), not an empty string.

### Scope Guard and Authorization Record

Two mechanisms keep the tool from being pointed at something it should not touch.

- **Allowlist.** Every outbound connection, redirect, and callback is checked against `scope.allow_hosts`. Anything else is refused and the run exits with code 5. There is no flag to disable this check.
- **Authorization record.** The `scope.authorization` block (owner, reference to a ticket or written approval) is copied into every report. When an auditor asks who approved a scan of a given server, the answer is in the artifact.

### Authentication and Authorization Testing

Without credentials, Siegeproof can only tell you whether a tool is callable **with no authentication at all**. Authorization is different: whether `reader` *should* reach `delete_document` is a policy fact only you know. So you declare it.

- `identities` lists the credentials the scanner may use. For HTTP transports, MCP authorization is OAuth-based, so supply pre-issued bearer tokens. For stdio servers, credentials normally come from the environment.
- `expectations.deny` lists the tools each identity must **not** be able to invoke.
- The auth family then checks: unauthenticated access to every tool; each identity calling every tool on its deny list; and privilege escalation through chained calls. A denied tool that succeeds is a Critical or High finding depending on the tool.

Interactive OAuth login flows are not supported in v1. Provide a service identity with a pre-issued token.

### Side Effects and Destructive Tools

MCP tools can send email, write files, mutate databases, and move money. Server-declared annotations (`readOnlyHint`, `destructiveHint`) help classify tools by default, but they are self-reported by the server under test and are treated as hints, never guarantees.

| `scan.side_effects` | Behavior |
|---|---|
| `skip` (default) | Tools not known to be read-only are not invoked. They appear as **not tested** and count against coverage. |
| `allow` | All tools are fuzzed. Refused unless `scope.environment: disposable`. |

Rules the tool enforces regardless of config:

- `scope.environment: production` forces `skip`.
- Payloads are non-destructive by construction (see [Payload Safety Rules](#payload-safety-rules)), but a fuzzed tool can still do what it normally does with garbage input. Disposable backing services are what make `allow` safe.

Coverage is what stops `skip` from quietly turning into a false pass: if too few tools were exercised, the run fails closed (exit 2).

### Deterministic Evidence: Canaries and Callbacks

Findings you can trust come from observing something the server should never have done.

- **File canaries.** For sandboxed stdio targets, Siegeproof plants files containing unique tokens outside the server's intended workspace. If a traversal payload returns a token, that is a confirmed unauthorized read, not a guess. For remote targets, pre-plant a file and list it under `canaries.remote_paths`.
- **Command-injection markers.** Payloads try to make the target echo a unique token or delay by a measured interval. Nothing is deleted or modified.
- **Out-of-band callbacks.** For SSRF and blind injection, the scanner runs a listener; a hit on a unique URL proves the target made an outbound request on the attacker's behalf. The target must be able to reach `canaries.oob.public_url`. In many CI setups it cannot. When callbacks are unavailable, those checks fall back to error- and timing-based signals, are labeled **probabilistic**, and the report notes the reduced coverage.

### Reproducibility and Replay

A security gate that gives different answers on identical code is a gate people learn to ignore.

- Every report records the **seed, corpus version, scoring model version, and tool version**. Pin `scan.corpus` and `scoring.model` in CI and bump them deliberately, so a score change is always attributable to either your code or your upgrade.
- Every finding stores an **evidence bundle**: the redacted JSON-RPC transcript that triggered it.
- `siegeproof replay --from reports/scan.json --finding F-0002` re-sends just that transcript. Use it to confirm a fix in seconds instead of re-running a 15-minute scan.

### Baselines and Suppressions

Two different tools for two different situations.

**Baseline**: "we know about these; only fail me on new ones." Useful when adopting the tool on a server with existing findings.

```bash
siegeproof baseline create --from reports/scan.json --out .siegeproof/baseline.json
siegeproof scan --config siegeproof.yaml --baseline .siegeproof/baseline.json
```

**Suppression**: "this specific finding is accepted, for this reason, until this date."

```yaml
# .siegeproof/suppressions.yaml
suppressions:
  - rule: SP-SCHEMA-004
    tool: search_docs
    reason: "Verbose errors are intentional in staging; the production wrapper strips them."
    owner: platform-team
    ticket: SEC-2231
    expires: 2026-12-31
```

Rules: every suppression needs a `reason`, an `owner`, and an `expires` date (default maximum 180 days). An expired suppression re-activates the finding and fails the gate. Suppressed findings stay in the report and are excluded from the gate decision, and the report shows both the raw and the adjusted score, so an accepted risk is never invisible.

### Scoring

The score is a number a pipeline can gate on. It is deliberately simple enough to explain in a code review.

```
score = max(0, 100 − Σ deduction(f))  then  min(score, strictest cap triggered)

deduction(f) = severity_weight × confidence × 0.5^(n−1)
  where n = the position of f among findings sharing its rule ID (dedupes noisy classes)
```

| Severity | Weight | Cap on final score (confirmed findings only) |
|---|---|---|
| Critical | 30 | 40 |
| High | 12 | 70 |
| Medium | 4 | none |
| Low | 1 | none |

| Confidence | Multiplier | Can trigger a cap? |
|---|---|---|
| Deterministic | 1.0 | Yes |
| Probabilistic | 0.5 | No, unless confirmed by replay or promoted in triage |

A single confirmed critical finding is never averaged away by a large number of clean checks. Weights and caps are configurable, but the model is **versioned** (`scoring.model: v1`); changing them creates a new version so historic scores stay comparable. `siegeproof score --explain --from reports/scan.json` prints exactly which findings cost how many points.

A score is only meaningful alongside **coverage** (tools exercised, families run, callbacks available). Reports always include it, and `min_coverage` lets you fail a run that looked clean only because most of the surface was never touched.

### Hardening Workflow

`harden` turns findings into a deployable policy. Treat it as a pipeline, not a one-shot command.

```
scan  →  harden  →  review diff  →  shadow mode  →  enforce  →  verify  →  (fix root cause)
```

1. **Scan** and produce `reports/scan.json`.
2. **Generate:** `siegeproof harden --from reports/scan.json --out siegeproof.hardened.yaml`
3. **Review the diff in a pull request.** An allowlist inferred from a scan can be too tight, or wrong. A human should read every disabled tool.
4. **Run in `mode: shadow`.** The policy logs what it *would* have blocked without blocking it. Watch real traffic for a few days for false positives.
5. **Switch to `mode: enforce`.**
6. **Verify:** `siegeproof harden ... --verify` re-runs the scan through the enforcing policy and reports the score before and after. CI can require `verified: true`.
7. **Fix the root cause.** The policy is a mitigation, not a repair. Every generated rule carries the finding that caused it, so it can be removed once the code is fixed.

```yaml
# siegeproof.hardened.yaml (generated; review before deploying)
policy_version: 1
generated_from:
  scan: reports/scan.json
  siegeproof: 0.1.0
  score_before: 38
mode: shadow                       # shadow (log only) | enforce (block)

tools:
  read_file:
    allowed_paths: ["/workspace/**"]
    deny_traversal: true
    params:
      path: { max_length: 512, deny_patterns: ["\\.\\./", "%2e%2e"] }
    reason: "F-0001 path traversal (CWE-22)"
  run_command:
    disabled: true
    reason: "F-0002 unrestricted shell execution (CWE-78); no evidence of intended use"
  send_email:
    max_calls_per_session: 3
    require_confirmation: true
    reason: "F-0005 no call limit (CWE-770)"

rate_limits:
  default_rps: 5
  burst: 10

overrides: {}                      # hand-written rules live here and survive regeneration
```

| Directive | Effect |
|---|---|
| `disabled` | Removes the tool from listings and rejects calls to it |
| `allowed_paths`, `deny_traversal` | Normalizes and confines path arguments |
| `params.<name>` | Per-parameter validators: length, pattern, enum, numeric range |
| `max_calls_per_session` | Caps how often a tool may be called in one session |
| `require_confirmation` | Holds the call until an approval hook allows it |
| `rate_limits` | Token-bucket limits per server and per tool |
| `overrides` | Hand-written rules; never modified by regeneration |

**Enforcement.** A policy file is only useful if something enforces it. The planned enforcement point is `siegeproof guard`, a thin proxy that sits between the MCP client and the server, for stdio (as a wrapper command) and for HTTP (as a reverse proxy):

```bash
# stdio: replace the server command in your MCP client config with the guard
siegeproof guard --policy siegeproof.hardened.yaml --stdio "python my_server.py"

# HTTP: run in front of the server
siegeproof guard --policy siegeproof.hardened.yaml --listen :8443 --upstream http://127.0.0.1:8080/mcp
```

The policy format is documented and versioned, so it can also be translated into an API gateway or service-mesh policy if you already run one.

### CI/CD Integration

**Which scan runs where**

| Trigger | Intensity | Gate | Typical duration |
|---|---|---|---|
| Pull request | `low` | `--fail-on critical` plus `--min-score` | minutes |
| Merge to main | `medium` | Score threshold | 10-15 min |
| Nightly / weekly | `high` | Report + alert on drift | up to budget |
| Release candidate | `high` + `harden --verify` | Score + verified policy | up to budget |

**GitHub Actions**

```yaml
# .github/workflows/mcp-security.yml
name: MCP Security Gate
on:
  pull_request:
  schedule:
    - cron: "0 3 * * *"      # nightly deep scan
permissions:
  contents: read
  security-events: write     # required to upload SARIF
concurrency:
  group: mcp-security-${{ github.ref }}
  cancel-in-progress: true
jobs:
  scan:
    runs-on: ubuntu-latest
    timeout-minutes: 30
    steps:
      - uses: actions/checkout@<sha>            # pin third-party actions to a full commit SHA
      - uses: withbrian-technologies/siegeproof-action@<sha>
        with:
          config: siegeproof.yaml
          intensity: ${{ github.event_name == 'schedule' && 'high' || 'low' }}
          min-score: 80
          fail-on: critical
          format: sarif
      - uses: github/codeql-action/upload-sarif@<sha>
        if: always()                            # upload results even when the gate fails
        with:
          sarif_file: siegeproof-results.sarif
      - uses: actions/upload-artifact@<sha>
        if: always()
        with:
          name: siegeproof-report
          path: reports/
          retention-days: 30
```

**GitLab CI**

```yaml
mcp-security:
  stage: test
  image:
    name: ghcr.io/withbrian-technologies/siegeproof@sha256:<digest>
    entrypoint: [""]
  script:
    - siegeproof scan --config siegeproof.yaml --intensity low --min-score 80 --format sarif --report reports/siegeproof.sarif
  artifacts:
    when: always
    paths: [reports/]
    expire_in: 30 days
```

**Practical notes**

- For **stdio targets**, the scanner launches the server itself; there is nothing to start. For **HTTP targets**, start the server in a prior step (a service container or `docker compose up -d`) and use `target.wait`.
- **Fork pull requests** do not receive secrets. Scan a stdio target built from the PR with no credentials, or run the authenticated gate after merge.
- **Pipe on the exit code, not on log text.** Exit `1` means "insecure"; exit `2`, `4`, or `6` means "the scan itself failed," which you may want to retry or route differently. See [Exit Codes](#exit-codes).
- Keep report artifacts short-lived and access-controlled. They contain working exploit evidence. See [Securing the Scanner Itself](#securing-the-scanner-itself).
- SARIF results describe runtime behavior, not source lines. Set `report.sarif.anchor_file` to the server's entry point or manifest so findings render in code-scanning UIs.
- Scaffold the workflow with `siegeproof ci init --provider github`.

### Containers and Kubernetes

**Docker Compose**: an isolated network with no route to the internet or the host LAN:

```yaml
services:
  target:
    build: ./server
    networks: [scan-net]
    environment:
      DATABASE_URL: postgres://scanner:scanner@db:5432/scratch
    depends_on: [db]
  db:
    image: postgres:16
    networks: [scan-net]
    environment: { POSTGRES_USER: scanner, POSTGRES_PASSWORD: scanner, POSTGRES_DB: scratch }
  siegeproof:
    image: ghcr.io/YOUR_ORG/siegeproof@sha256:<digest>
    networks: [scan-net]
    depends_on: [target]
    command: ["scan", "--endpoint", "http://target:8080/mcp", "--wait", "60s",
              "--intensity", "medium", "--report", "/out/scan.json"]
    volumes: ["./reports:/out"]
networks:
  scan-net:
    internal: true      # no external connectivity
```

**Kubernetes Job** with a locked-down pod and an egress-restricted network policy. `--report -` writes the report to stdout (logs go to stderr), which suits Job logs and log-shipping pipelines.

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: siegeproof-docs-search
  namespace: security-scans
spec:
  backoffLimit: 0
  activeDeadlineSeconds: 1800
  ttlSecondsAfterFinished: 86400
  template:
    metadata:
      labels: { app: siegeproof }
    spec:
      restartPolicy: Never
      automountServiceAccountToken: false
      securityContext:
        runAsNonRoot: true
        seccompProfile: { type: RuntimeDefault }
      containers:
        - name: siegeproof
          image: ghcr.io/YOUR_ORG/siegeproof@sha256:<digest>
          args: ["scan", "--config", "/etc/siegeproof/siegeproof.yaml", "--report", "-"]
          securityContext:
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: ["ALL"] }
          resources:
            requests: { cpu: 250m, memory: 256Mi }
            limits: { cpu: "1", memory: 512Mi }
          envFrom:
            - secretRef: { name: siegeproof-target-creds }
          volumeMounts:
            - { name: config, mountPath: /etc/siegeproof, readOnly: true }
            - { name: tmp, mountPath: /tmp }
      volumes:
        - name: config
          configMap: { name: siegeproof-config }
        - name: tmp
          emptyDir: {}
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: siegeproof-egress
  namespace: security-scans
spec:
  podSelector:
    matchLabels: { app: siegeproof }
  policyTypes: [Egress]
  egress:
    - to:                                   # the target, and nothing else
        - namespaceSelector:
            matchLabels: { kubernetes.io/metadata.name: staging-mcp }
          podSelector:
            matchLabels: { app: docs-search-mcp }
      ports:
        - { protocol: TCP, port: 8080 }
    - to:                                   # cluster DNS
        - namespaceSelector: {}
          podSelector:
            matchLabels: { k8s-app: kube-dns }
      ports:
        - { protocol: UDP, port: 53 }
        - { protocol: TCP, port: 53 }
```

### Fleet Scoring

Once more than a handful of servers exist, scoring one at a time stops scaling. Describe the fleet in an inventory file:

```yaml
# servers.yaml
version: 1
defaults:
  scan: { intensity: low, budget: { max_duration: 10m } }
servers:
  - name: docs-search
    owner: platform-team
    tier: 2
    config: services/docs-search/siegeproof.yaml
  - name: billing-tools
    owner: finance-eng
    tier: 1
    config: services/billing-tools/siegeproof.yaml
```

```bash
siegeproof fleet scan   --inventory servers.yaml --concurrency 4 --out fleet/
siegeproof fleet report --from fleet/ --format json > fleet-summary.json
```

The summary is designed to be machine-consumed and to drive per-tier gates:

```json
{
  "schema": "siegeproof.fleet/v1",
  "generated": "2026-09-18T03:00:00Z",
  "servers": 42,
  "score": { "median": 81, "p10": 52, "min": 31 },
  "gate": { "pass": 35, "fail": 5, "incomplete": 2 },
  "worst": [{ "name": "legacy-shell", "tier": 1, "owner": "platform-team", "score": 31, "critical": 2 }]
}
```

*Illustrative. Fleet commands land with Phase 6; the hosted dashboard is a stretch goal.* Track the median and the worst-decile score over time, and alert on **incomplete** scans as loudly as on failing ones. A server that stopped being scanned is a server you no longer know anything about.

### Triage and Ownership

Every finding gets a stable ID, a rule ID (`SP-<FAMILY>-<NNN>`), and a fingerprint so the same issue is the same finding across runs.

Lifecycle: **new → triaged → mitigated → fixed → verified.** "Verified" means a replay or re-scan no longer reproduces it. An example SLA to adapt to your own policy:

| Severity | Mitigate (disable the tool or deploy the policy) | Fix root cause |
|---|---|---|
| Critical | 2 business days | 14 days |
| High | 7 days | 30 days |
| Medium | | 90 days |
| Low | | Backlog |

Route findings by the `owner` in the inventory, not by whoever happens to read the report.

### Securing the Scanner Itself

A tool that sends attack payloads and launches arbitrary server processes is itself a sensitive component.

- **Sandboxed child processes.** stdio targets run with a scrubbed environment (only what `target.env` provides), a temporary working directory, resource limits, and restricted egress. `sandbox.mode: none` requires an explicit `--unsafe-no-sandbox` flag.
- **The target is untrusted.** Responses are size-capped (default 1 MiB), JSON is parsed with depth limits, terminal control sequences are stripped before anything is printed, every read has a timeout, and nothing returned by the server is ever evaluated or executed.
- **Secrets stay out of output.** Configured tokens, `Authorization` and `Cookie` headers, and environment values are redacted from logs, reports, and evidence bundles.
- **Reports are sensitive.** They contain working exploit evidence against your own servers. Restrict access, keep retention short, and do not attach them to public issues.
- **No telemetry.** The scanner contacts only the target (and receives callbacks on your own listener). No usage data is sent anywhere.
- **Supply chain.** Releases are built reproducibly (`-trimpath`), signed keylessly, and ship with checksums and an SBOM. Pin the Docker image by digest and pin third-party CI actions by SHA.

---

## CLI Reference

| Command | Purpose |
|---|---|
| `discover` | Enumerate tools, resources, and prompts exposed by a target. Sends no payloads. |
| `scan` | Run the full fuzz pass and emit a compliance score plus findings. |
| `fuzz` | Run one payload family in isolation (`--family injection\|schema\|auth\|chain`). |
| `score` | Recompute or inspect a score from an existing report (`--explain`). |
| `harden` | Generate a hardened policy from a scan report (`--verify` to re-scan through it). |
| `guard` | *(Planned)* Enforce a hardened policy as a stdio wrapper or HTTP reverse proxy. |
| `replay` | Re-send the evidence transcript for one finding. |
| `baseline` | Create a baseline of accepted findings from a report. |
| `fleet` | Scan and summarize many servers from an inventory file. |
| `ci init` | Scaffold a CI workflow with a score-gated step. |
| `doctor` | Check sandbox, canary listener, and egress behavior on this machine. |
| `version` | Print tool, corpus, and scoring model versions. |

**Common flags**

| Flag | Description |
|---|---|
| `--config PATH` | Config file (default: `./siegeproof.yaml`) |
| `--stdio CMD` | Target is a local stdio server launched with this command |
| `--endpoint URL` | Target is a remote server; transport is autodetected (override with `--transport`) |
| `--header 'K: V'` | Extra request header (HTTP transports). Repeatable. |
| `--wait DURATION` | Wait for the target to become ready before scanning |
| `--intensity low\|medium\|high` | Payload volume and depth |
| `--families LIST` | Restrict to specific payload families |
| `--max-duration`, `--max-requests`, `--rps`, `--concurrency` | Budget controls |
| `--min-score N` | Fail (exit 1) if the score is below `N` |
| `--fail-on SEVERITY` | Fail (exit 1) on any unsuppressed finding at or above this severity |
| `--baseline PATH`, `--suppressions PATH` | Baseline and suppression files |
| `--allow-side-effects` | Fuzz tools that may have side effects (requires a disposable environment) |
| `--seed N` | Fix the random seed for reproducibility |
| `--format json\|sarif\|md` | Report format; repeat for multiple |
| `--report PATH` | Where to write the report (`-` for stdout) |
| `--log-format text\|json`, `-v`, `--no-color` | Logging controls; logs always go to stderr |

### Exit Codes

Exit codes are a stable, documented interface. Pipelines should branch on them.

| Code | Meaning |
|---|---|
| `0` | Pass: the scan completed and all gate conditions were met |
| `1` | Gate failed: score below threshold, or a finding at or above `--fail-on` |
| `2` | Scan incomplete: budget exhausted, target crashed, or coverage below `min_coverage` (**fails closed**) |
| `3` | Usage or configuration error |
| `4` | Target unreachable, or handshake/protocol failure |
| `5` | Scope violation: target, redirect, or callback outside `allow_hosts`, or side effects requested outside a disposable environment |
| `6` | Internal error (a bug in Siegeproof; please report it) |

---

## Payload Taxonomy

| Family | What It Tests |
|---|---|
| Schema mutation | Type confusion, boundary values, malformed JSON, unexpected or extra fields |
| Prompt injection | Hidden instructions and encoded payloads embedded in tool and resource descriptions |
| Cross-tool contamination | Whether one tool's metadata can alter another tool's behavior via shared context |
| Parameter manipulation | Path traversal, command and argument injection markers, SSRF probes in tool arguments |
| Auth boundary probing | Missing authentication, over-permissioned tools, privilege escalation via tool chaining |
| Stateful chains | Multi-step sequences (for example `navigate → snapshot → click`) that carry session state across calls |
| Resource exhaustion | Rate-limit and timeout behavior under sustained and parallel load |

### Payload Safety Rules

These are constraints on the corpus itself, enforced by tests in this repository:

- Payloads may **read canaries, echo unique tokens, sleep for a bounded interval, and call back to the scanner's own listener.**
- Payloads never delete, overwrite, encrypt, or exfiltrate real data, and never target hosts outside `allow_hosts`.
- Resource-exhaustion probes stay inside the configured rate and request budget. This tool is a fuzzer, not a load generator.

## Reports

Reports are the durable artifact of a run. JSON is the source of truth; SARIF and Markdown are derived from it.

```json
{
  "schema": "siegeproof.report/v1",
  "tool": { "version": "0.1.0", "corpus": "2026.09.1", "scoring_model": "v1" },
  "run": { "id": "run_01", "seed": 41827, "duration_s": 372, "complete": true },
  "authorization": { "owner": "platform-team", "reference": "SEC-1234" },
  "target": { "name": "docs-search", "transport": "stdio", "tools": 3 },
  "coverage": { "tools_tested": 3, "tools_total": 3, "families_run": 5, "callbacks_available": false },
  "score": { "value": 38, "cap": 40, "cap_reason": "F-0002", "adjusted_for_suppressions": 38, "threshold": 80, "pass": false },
  "findings": [
    {
      "id": "F-0002",
      "rule": "SP-PARAM-002",
      "severity": "critical",
      "cwe": "CWE-78",
      "confidence": "deterministic",
      "tool": "run_command",
      "summary": "Shell metacharacters in `command` are executed",
      "evidence": { "transcript": "evidence/F-0002.json" },
      "replay": "siegeproof replay --from reports/scan.json --finding F-0002",
      "suppressed": false
    }
  ]
}
```

**SARIF.** Each finding maps to a SARIF result with a stable `ruleId`, a severity `level`, and the CWE in the rule's properties. Because findings are runtime observations, results are anchored to `report.sarif.anchor_file` with the tool name as a logical location.

## Troubleshooting and FAQ

**Can I run this against production?**
It is not recommended. If you must, see [Where and How to Run It](#where-and-how-to-run-it): read-only families only, no side effects, low rate, written authorization.

**My score changed and I did not change any code.**
The corpus or scoring model changed. Reports record both; compare them. Pin `scan.corpus` and `scoring.model` in CI to control when scores move.

**The scan exited with code 2 ("incomplete").**
The budget ran out, the target crashed or hung, or too few tools were exercised (`min_coverage`). Check the coverage block in the report. Tools skipped by `side_effects: skip` count as not tested. Raise the budget, run against a disposable environment with `allow`, or lower `min_coverage` deliberately.

**SSRF and blind-injection checks are marked probabilistic.**
The target could not reach your callback listener. Set `canaries.oob.public_url` to an address the target can reach, or accept the reduced confidence. The report's coverage block says which.

**I get many probabilistic prompt-injection findings.**
Expected: they depend on how a model interprets text. They are discounted in scoring and cannot trigger caps on their own. Triage them, then suppress with a reason and an expiry, or replay to promote confirmed ones.

**Does the hardened policy replace fixing the code?**
No. It is a mitigation you can deploy today while the real fix is scheduled. Every rule cites the finding behind it so you can delete the rule after the fix ships.

**The scanner cannot connect to my stdio server.**
Run the command by hand in the same working directory and environment. Remember the child inherits only `target.env`, so a missing `PATH` or virtualenv is the usual cause. Run `siegeproof doctor`.

**Does it support interactive OAuth logins?**
Not in v1. Use a service identity with a pre-issued token.

**Does it send data anywhere?**
No. There is no telemetry. It contacts only your target and receives callbacks on your own listener.

---

## Roadmap

- [ ] **Phase 0**: Protocol research, spec-version coverage matrix, gap analysis vs. existing tools
- [ ] **Phase 1**: MCP client and schema introspection (stdio, SSE, streamable HTTP)
- [ ] **Phase 2**: Payload/mutation engine (schema, injection, parameter families)
- [ ] **Phase 3**: Sandboxed execution orchestrator with chained-call support, canaries, and callback listener
- [ ] **Phase 4**: Deterministic verdict engine, compliance scoring, coverage gating
- [ ] **Phase 5**: Hardened policy generator, `harden --verify`, and the `guard` enforcement proxy
- [ ] **Phase 6**: SARIF/JSON reporting, baselines and suppressions, GitHub Action, signed binary distribution, `fleet` commands
- [ ] **Phase 7 (stretch)**: Opt-in LLM-assisted verdicts for ambiguous contamination cases
- [ ] **Phase 8 (stretch)**: Hosted fleet dashboard aggregating scores across many servers

## Non-Goals (v1)

- Static source-code analysis (Semgrep-style). Dynamic-only for now; static scanning is a plausible v2 addition, not a launch feature.
- LLM-generated payloads as the primary engine. Schema-driven mutation is the default; an `--ai` flag for context-aware payloads is a later addition once the deterministic core is solid.
- A hosted SaaS dashboard. v1 is CLI plus CI artifacts only.
- Interactive OAuth flows and testing MCP *clients* or *hosts*.

## Responsible Use

Siegeproof sends adversarial input, including payloads designed to trigger command execution and data-exfiltration paths, and can start local processes when testing stdio servers. Use it **only against servers you own or have explicit written authorization to test.** The sandboxing in the execution orchestrator reduces accidental impact; it is not a substitute for running the target in a container or an isolated network segment you control.

## Security Policy

Found a vulnerability in Siegeproof itself? Please do not open a public issue. See `SECURITY.md` for private reporting instructions and the disclosure timeline.

## Contributing

Contributions are welcome once the core scanning engine stabilizes. Until then, the most useful contributions are issues describing gaps in payload coverage and false-positive or false-negative reports. Include the server, the tool schema, and the replay transcript where you can.

## License

MIT
