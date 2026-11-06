# Decisions log

All decisions made with the project owner, in order (2026-11-06).

| # | Topic | Decision |
|---|-------|----------|
| 1 | Language / TUI | Go; Bubble Tea + Bubbles + Lip Gloss. Single static binary, model `Update` is testable without a terminal. |
| 2 | Default LXC security profile | `privileged`, requires explicit risk acknowledgement (`security.acknowledge_privileged_risk`). `unprivileged-experimental` available as an opt-in. |
| 3 | Host kernel modules | pk3s writes `/etc/modules-load.d/pk3s.conf` and runs `modprobe` (shown in the plan, confirmed before apply). |
| 4 | Persistent data | Separate wizard page. If enabled, ask for a host base path and bind-mount `<path>/<cluster>/<hostname>` into each container at `/var/lib/rancher/k3s/storage`. Never deleted automatically. |
| 5 | License / module | Apache-2.0, `github.com/oleksdovz/pk3s`. |
| 6 | Reference config | Privileged profile is taken from the owner's working script (`docs/reference/lxc-k3s-test.sh`). From it: optional `/dev/net/tun`, timezone, traefik/servicelb enabled by default (k3s defaults). `fuse=1` is optional and off by default (Proxmox advises against FUSE in CTs). |
| 7 | NFS | `nfs-common` installed in every container by default; host loads `nfs`/`nfsv4` modules. Warning for unprivileged profile (kernel blocks NFS mounts there). |
| 8 | Naming | Configurable `hostname_prefix` (defaults to cluster name): `<prefix>-main1..`, `<prefix>-node1..`. |
| 9 | Per-node network | Every container's IP, prefix, gateway and DNS is editable (`node_network` in YAML, "Node network" page in the TUI). Unset fields fall back to cluster-wide defaults; IPs default to consecutive addresses from `network.first_ip`. |
| 10 | Remote mode (later phase) | Run pk3s from a workstation (macOS arm64, Windows, Linux) with `--host <proxmox>`; commands executed over SSH. The `runner.Runner` abstraction is the extension point; host-side file writes (state, cache, temp files for `pct push`) must also move behind it. Builds for windows/darwin already compile. |
| 11 | Test host | Proxmox VE 9.2 host `hp` (reachable as `ssh hp`). Read-only inventory is fine; anything that creates containers or loads modules needs explicit approval first. Existing CT 9000 (Proxmox Backup Server, 192.168.0.6) must never be touched. |
| 12 | Language of communication | Replies in Ukrainian; code, CLI labels and docs in English. |
| 13 | Node names | Servers are `<prefix>-main1..N`, agents `<prefix>-node1..N`; `node_network` keys are `main1`, `node1`, ... (replaces the earlier `-s1`/`-a1` scheme). Clusters created by builds before this change must not be resumed with newer builds (hostnames would differ). |
| 14 | Autostart | `onboot: 1` plus Proxmox `startup`: main `order=1,up=10`, nodes `order=2,up=10` (editable on the "Autostart" page / `startup:` in YAML). Proxmox shuts guests down in reverse order, so main stops last. |
| 15 | Update / scale | Declarative only: edit the spec and `apply` again (TUI: `e` on a cluster). No `scale`/`upgrade` commands. Changes run one at a time in the order shown in the plan: container settings → k3s upgrade (servers, then agents) → node removal → new nodes → readiness check. |
| 16 | Scale-down | Remove the agent with the highest CT ID first (agents 5001-5004, agents -1 → 5004). Drain (5 min) → delete node → destroy CT; data directory kept. |
| 17 | Upgrade | Only an exact version upgrades; channels never upgrade a running cluster. One minor version at a time, no downgrades. Datastore backup first; servers cordoned, agents drained. |
| 18 | Stable layout | Deployed nodes keep CT ID, IP and data dir (`state.json` layout); new nodes take the next free ID/IP at or after their default position. Server count changes 1→3 and server removal are rejected until implemented. |
| 19 | Datastore | Always embedded etcd, also for one server (`cluster-init` on main1). No SQLite, so 1→3 servers is a plain join and upgrades start with an etcd snapshot. |
| 20 | State in the cluster | ConfigMap `kube-system/pk3s-state` (spec, node layout, k3s version; no secrets) is written after every successful apply. Host state (`/var/lib/pk3s`) stays authoritative (needed before main1 exists, during restarts, for cleanup). With no host state, pk3s restores it from the ConfigMap through main1 (found by tag), re-reading tokens and the kubeconfig. |
| 21 | Scale-down via main | All Kubernetes operations run with kubectl on main1. Agent removal: cordon → drain → delete node → stop → destroy CT. |
| 22 | `pk3s init NAME` | Name is required. Scans the host (templates, storages, bridges, DNS, IDs and addresses in use) and writes an annotated spec: chosen values active, alternatives and optional sections commented. `ctid_start` is the first block of 100 whose IDs for the spec plus 10 spare are free cluster-wide; the result is stated in the file. Writes NAME.yaml, or stdout when redirected (`pk3s init lsb1-k3s > cluster.yaml`). For an existing cluster (host state or restorable from the ConfigMap) it exports the deployed spec. |
