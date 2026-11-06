# Implementation progress

Status of the MVP. Nothing below has been validated on a live Proxmox host
unless stated explicitly.

## Done
- `internal/runner` — command execution abstraction, fake runner, secret redactor.
- `internal/spec` — cluster spec, defaults, YAML; hostname prefix; per-node network overrides (`node_network`); NFS client and extra packages.
- `internal/pve` — inventory via `pvesh` JSON (nodes, storage, bridges, DNS, templates, guests, kernel modules, firewall); `pct`/`pveam` wrappers.
- `internal/lxcprofile` — `privileged` profile (based on the author's working script) and `unprivileged-experimental`; each raw `lxc.*` line has a justification.
- `internal/k3s` — channel resolution, binary download with SHA-256 verification and cache, `config.yaml` and systemd unit rendering, node status parsing.
- `internal/plan` — pure Spec + Inventory → Plan (layout, host changes, ordered resumable steps).
- `internal/validate` — documented k3s minimums, quorum rules, storage/bridge/template checks, CT ID / hostname / IP conflicts, per-node network checks.
- `internal/state` — `state.json`, `secrets.json` (0600), resource ledger, portable lock.
- `internal/engine` — step executor with resume, stop-between-steps, ownership checks; tokens passed only through files pushed with mode 0600.
- `internal/app` — wiring, status, ledger-based destroy (keeps data dirs and host files), recovery hints.
- `cmd/pk3s` — CLI: `init`, `plan`, `apply`, `resume`, `status`, `list`, `destroy`.
- `internal/tui` — Bubble Tea wizard (10 pages incl. per-node network editing), plan review, typed confirmation, progress, summary, status.
- Tests: inventory against fixtures captured read-only from the PVE 9.2 host `hp`; plan; validation table tests; engine against a Proxmox simulator (full run, secret leakage, resume, foreign CT refusal, stop).

## Next
- (done) TUI model tests, spec/state/runner tests.
- (done) README, docs/DESIGN.md, docs/SECURITY.md, docs/TESTING.md, LICENSE (Apache-2.0), Makefile.
- (done) Read-only `pk3s init` / `pk3s plan` on `hp`: inventory correct; 192.168.0.51 is taken by a LAN device (MAC 10:66:6a:53:1c:c9), .52-.65 free.
- Live `pk3s apply` on `hp` (only after explicit approval; creates containers). Suggested first run: 1 server + 1 agent from 192.168.0.52.

## Planned (later phases)
- Remote mode: `pk3s --host <proxmox>` from macOS (arm64) / Windows / Linux over SSH.
- Multi-host placement across Proxmox cluster nodes.
- Stable API endpoint (kube-vip VIP) for multi-server clusters.
