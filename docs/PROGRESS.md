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

- (done) Declarative update of a running cluster: stable node layout in state, diff plan, agent scale up/down, server scale-up for etcd clusters, rolling k3s upgrade with datastore backup, CPU/RAM/disk/autostart changes, resumable pending operation. Tested against the simulator only.

- (done) Always embedded etcd (SQLite removed), so 1→3 servers is a join.
- (done) Cluster state ConfigMap `kube-system/pk3s-state` written after every apply; host state restored from it when missing.
- (done) Scale-down sequence via kubectl on main1: cordon → drain → delete node → stop → destroy.

- (done) `pk3s init NAME`: host scan into an annotated starter spec (alternatives commented, free CT IDs checked), or export of the deployed spec for an existing cluster. Verified read-only on `hp`.

- (done) TUI: Midnight-Commander F1..F10 bar (Help, Save, View, Edit, New, Resume, Export, Destroy, Plan/Apply, Quit/Stop), Alt+digit fallback, "Review plan & deploy" entry, F1 help, destroy from the TUI with typed name.
- (done) Activity log /var/log/pk3s/pk3s.log (commands, validation, TUI edits, confirmations, steps, errors). Verified on `hp`.
- (done) Clear errors for destroyed/unknown clusters.
- `lab` on `hp` was destroyed by the owner with `pk3s destroy` at 2026-11-06 21:09 (archive in /var/lib/pk3s/clusters/.destroyed/).

## Next
- (done) TUI model tests, spec/state/runner tests.
- (done) README, docs/DESIGN.md, docs/SECURITY.md, docs/TESTING.md, LICENSE (Apache-2.0), Makefile.
- (done) Read-only `pk3s init` / `pk3s plan` on `hp`: inventory correct; 192.168.0.51 is taken by a LAN device (MAC 10:66:6a:53:1c:c9), .52-.65 free.
- (done) First live deployment on `hp`: 1 server + 1 agent, CT 5000-5001, 192.168.0.52-53, cluster `lab` (old `-s1/-a1` names). Works end to end after the PATH fix; see docs/TESTING.md.
- (done, by the owner) `pk3s destroy lab` on `hp`: CTs 5000-5001 removed, state archived to /var/lib/pk3s/clusters/.destroyed/lab-20261106T190901Z, other guests and /data untouched.
- Not yet tested live:
  - a fresh `apply` with main/node names, autostart and embedded etcd on one server;
  - update flow: agents up/down, servers 1→3, rolling k3s upgrade (start from v1.36.x), CPU/RAM/disk;
  - state ConfigMap kube-system/pk3s-state and recovery when host state is missing;
  - persistent data bind mounts + local-path PVCs (examples/manifests/local-path-pvc-test.yaml);
  - TUI F-key navigation in a real terminal (macOS Terminal / iTerm over SSH);
  - unprivileged-experimental profile.
- (done, live) parallel provision + merged steps; scale-up agents 2→5; scale-down 5→1; servers 1→3; remote mode from macOS (`--host`).
- Not yet tested live: rolling k3s upgrade (needs a cluster on v1.36.x), CPU/RAM/disk change, ConfigMap recovery, persistent data + PVCs, remote TUI over `ssh -t`, Windows client, unprivileged profile.
- Not implemented: removing servers (etcd member removal); multi-host placement; memory capacity check; pure SSH runner (pk3s only on the workstation).

## Planned (later phases)
- Remote mode: `pk3s --host <proxmox>` from macOS (arm64) / Windows / Linux over SSH.
- Multi-host placement across Proxmox cluster nodes.
- Stable API endpoint (kube-vip VIP) for multi-server clusters.
