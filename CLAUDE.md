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
make build        # dist/pk3s-linux-amd64 for the Proxmox host
make dist         # all platforms, Linux binaries embedded for --host
```
Install on the test host without disturbing a running pk3s:
`scp dist/pk3s-linux-amd64 hp:/usr/local/bin/pk3s.new && ssh hp 'mv -f /usr/local/bin/pk3s.new /usr/local/bin/pk3s'`
