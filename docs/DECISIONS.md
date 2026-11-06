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
