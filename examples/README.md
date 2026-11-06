# Example cluster specs

Storage, bridge and template names match the development host (Proxmox VE 9.2
with `local`, `DATA`, `local-lvm`, `x-data` and `vmbr0`). Adjust them and the
IP addresses for your host, then run `pk3s plan -f <file>` before `apply`.

| File | Main | Nodes | What it shows |
|------|------|-------|---------------|
| [01-single-main.yaml](01-single-main.yaml) | 1 | 0 | Smallest cluster, SQLite, workloads on the main node |
| [02-dev-small.yaml](02-dev-small.yaml) | 1 | 2 | Hostname prefix, traefik disabled, SSH key, timezone |
| [03-etcd-3-main.yaml](03-etcd-3-main.yaml) | 3 | 3 | Embedded etcd, pinned k3s version, extra TLS SANs, persistent data, custom autostart |
| [04-per-node-network.yaml](04-per-node-network.yaml) | 1 | 3 | Per-container IP, DNS and gateway overrides |
| [05-vlan-custom-cidrs.yaml](05-vlan-custom-cidrs.yaml) | 1 | 2 | VLAN tag, MTU 9000, Ubuntu 24.04, custom pod/service CIDRs, `/dev/net/tun` |
| [06-unprivileged-experimental.yaml](06-unprivileged-experimental.yaml) | 1 | 1 | Experimental unprivileged profile, NFS client off |
| [07-many-workers.yaml](07-many-workers.yaml) | 1 | 5 | Large workers, extra packages, staggered autostart, host modules managed manually |
| [08-local-path-storage.yaml](08-local-path-storage.yaml) | 1 | 2 | Per-node host directories for the `local-path` StorageClass; test with [manifests/local-path-pvc-test.yaml](manifests/local-path-pvc-test.yaml) |

`go test ./examples/` checks that every file parses and validates without
errors against the captured host inventory (network probes are not run).
