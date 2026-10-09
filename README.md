# Anubis Ledger

**Persistent project memory for coding agents. Local, searchable, and built to remember when an old answer stopped being true.**

A fresh coding-agent session often begins by rereading the repository, reconstructing decisions, and repeating experiments that already failed. A long `ledger.md` helps, but eventually becomes hard to search and easy to misread: a conclusion from last month may have been disproved yesterday.

Anubis Ledger turns that working history into a small, inspectable project memory. Agents can ask for a concise task briefing, record important findings, and explicitly invalidate or supersede old knowledge. Humans can get a quick status or handoff without reading the entire history.

```text
ledger context "fix transparency ordering"
  → current facts, decisions, useful experiments, warnings, and open work

ledger history FACT-0012
  → the earlier belief, what changed, and why
```

Ledger is a single Go CLI. It needs no server, account, API key, LLM, embeddings, or network connection at runtime.

## Where it fits best

- **Long-running coding projects** where agents or humans return after days or weeks and need to resume quickly.
- **Reverse engineering and debugging** where hypotheses change as traces and tests reveal new facts.
- **Performance work** where a local optimization may be correct but fail to improve the whole application.
- **Multi-session agent work** where the next agent needs the current state, known pitfalls, and a concrete next step.

Ledger is deliberately small. It does not replace Git, documentation, code search, or an issue tracker. It works best for knowledge that would save a future contributor from rediscovering something important.

## Install

You need Go 1.27 or newer to build from source. The SQLite driver is pure Go, so no C compiler or separate SQLite installation is required.

```bash
git clone https://github.com/ReeceKetley/Anubis-Ledger.git
cd Anubis-Ledger
```

On **Windows**, run in PowerShell:

```powershell
.\install.ps1
```

This builds `ledger.exe` into `%LOCALAPPDATA%\Programs\Ledger` and adds that directory to your user PATH once. Open a new terminal to pick up the saved PATH. If PowerShell blocks local scripts, use `powershell -NoProfile -ExecutionPolicy Bypass -File .\install.ps1`.

On **Linux or macOS**:

```sh
sh ./install.sh
```

This installs to `~/.local/bin` and updates your shell profile if needed. Open a new shell afterward. Set `LEDGER_INSTALL_DIR` to choose a different install directory. You can also build directly with `go build -o ledger .` (or `ledger.exe` on Windows).

Ledger builds for Windows x64, Linux x64/ARM64, and macOS Intel/Apple Silicon.

## Quick start

Inside a project:

```bash
ledger init
```

Edit `.ledger/GOAL.md` and `.ledger/STATE.md` to describe what the project is trying to achieve and what is true today. Ledger also creates `.ledger/AGENT-INSTRUCTIONS.md`. It never overwrites an existing `AGENTS.md`.

At the start of work:

```bash
ledger brief
ledger context "implement renderer batching"
ledger search "renderer depth"
```

Record findings that another session will need:

```bash
ledger add fact "GPU command buffers are rebuilt every frame." --tag renderer --file src/renderer.go
ledger add experiment "Persistent buffers reduced frame CPU time by 18%." --tag renderer --confidence 0.9
ledger add warning "Do not regenerate protocol IDs." --importance critical --topic protocol-ids
ledger add task "Profile texture upload time."
```

When new evidence disproves an entry, preserve the old one and mark the change:

```bash
ledger invalidate FACT-0001 --because EXP-0001
ledger supersede FACT-0002 FACT-0003
ledger history FACT-0001
```

At a handoff or when returning later:

```bash
ledger handoff
ledger since "3 days ago"
ledger status
```

Commands work from subdirectories: Ledger finds the nearest `.ledger/` while walking upward, like Git finds `.git/`.

## Current truth and history

Entries can be facts, decisions, experiments, failures, hypotheses, warnings, tasks, or notes. Each has a stable ID such as `FACT-0001` and a status such as `ACTIVE`, `INVALIDATED`, `SUPERSEDED`, `UNVERIFIED`, `DEPRECATED`, or `RESOLVED`.

Normal search and context favor current ACTIVE knowledge. Invalidated and superseded entries remain available through `ledger history`, `ledger show`, or `ledger search --all`. Ledger never silently rewrites or deletes a disproved experiment.

`ledger add` can suggest related ACTIVE facts or decisions that may need review; it never supersedes them automatically. `ledger doctor` detects structural inconsistencies, including multiple ACTIVE decisions with the same `--topic`. These checks are deterministic. Ledger does **not** claim to understand natural-language contradictions.

Use `ledger context "task" --short`, `--normal`, or `--deep` to choose briefing depth. Context ranks matching knowledge, includes critical warnings, and filters obvious repeated entries. The compact `ledger brief` or task-independent `ledger handoff` is useful at session boundaries.

File and symbol references keep memory near the code it describes:

```bash
ledger add fact "buildOrderingTable controls transparent draw order." \
  --file src/render.cpp --entity buildOrderingTable
ledger file src/render.cpp
ledger entity buildOrderingTable
```

For maintenance, `ledger stale` lists entries with old verification dates; `ledger verify ID` updates that date without automatically changing confidence. `ledger archive ID` hides a low-value current entry without declaring it false. `ledger compact` is advisory and suggests possible duplicates.

## Files and recovery

Each initialized project contains a `.ledger/` directory:

```text
.ledger/
├── GOAL.md                  project objective and constraints
├── STATE.md                 current project reality
├── DECISIONS.md             human-readable high-level decisions
├── AGENT-INSTRUCTIONS.md    concise agent workflow
├── ledger.jsonl             append-only durable event history
├── ledger.db                rebuildable SQLite FTS5 index
├── entities/
└── archive/
```

The Markdown files and JSONL history are the durable memory. SQLite is the search index, not the sole copy. `ledger rebuild` recreates it from JSONL and backs up the previous database. `ledger backup` copies durable files into a timestamped archive with SHA-256 checksums; `ledger doctor` checks integrity and references without changing project memory.

Ledger serializes CLI access with a local lock and replays an appended event if SQLite was not updated before a process stopped. Schema version 2 migrates version-1 databases with a backup. Keep the human-owned Markdown editable; avoid changing `ledger.jsonl` by hand.

## Development

```bash
go test ./...
go vet ./...
```

Tests use temporary projects and do not modify your repositories. Run `ledger --help` or `ledger <command> --help` for command syntax. JSON output is available on commands intended for automation.
