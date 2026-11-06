# pk3s — notes for AI assistants and contributors

Go CLI + TUI that deploys k3s clusters into Proxmox VE LXC containers.

Start here when resuming work:
1. [docs/REQUIREMENTS.md](docs/REQUIREMENTS.md) — the original brief.
2. [docs/DECISIONS.md](docs/DECISIONS.md) — every decision made with the owner.
3. [docs/PROGRESS.md](docs/PROGRESS.md) — what is done and what is next.

## Working agreements
- Talk to the owner in Ukrainian; write code, CLI labels and docs in English.
- Never claim live validation unless pk3s actually ran on a Proxmox host.
- Test host `hp` (`ssh hp`, PVE 9.2): read-only commands are fine. Creating
  containers, loading modules or any other change needs explicit approval.
  Never touch CT 9000.
- Secrets (join tokens, kubeconfig) must never appear in logs, events,
  command arguments or `state.json`; `internal/engine` tests enforce this.

## Layout
- `cmd/pk3s` — CLI entry (`tui`, `init`, `plan`, `apply`, `resume`, `status`, `list`, `destroy`).
- `internal/runner` — all external commands go through `Runner` (fake for tests).
- `internal/pve` — inventory (`pvesh` JSON) and `pct`/`pveam` wrappers.
- `internal/spec` → `internal/plan` → `internal/validate` — pure, no I/O.
- `internal/engine` — resumable step executor; `internal/state` — state, ledger, secrets.
- `internal/app` — wiring for CLI/TUI; `internal/report` — text rendering.
- `internal/tui` — Bubble Tea UI.
- `internal/pve/testdata/hp` — real responses captured read-only from `hp`.

## Commands
```
go test ./...
go vet ./...
GOOS=linux GOARCH=amd64 go build -o dist/pk3s ./cmd/pk3s
```
