# Example cluster specs

Storage, bridge and template names match the development host (Proxmox VE 9.2
with `local`, `DATA`, `local-lvm`, `x-data` and `vmbr0`). Adjust them and the
IP addresses for your host, then run `pk3s plan -f <file>` before `apply`.

| File | Main | Nodes | What it shows |
|------|------|-------|---------------|
| [01-single-main.yaml](01-single-main.yaml) | 1 | 0 | Smallest cluster (embedded etcd, can grow to 3 servers), workloads on the main node |
| [02-dev-small.yaml](02-dev-small.yaml) | 1 | 2 | Hostname prefix, traefik disabled, SSH key, timezone |
| [03-etcd-3-main.yaml](03-etcd-3-main.yaml) | 3 | 3 | Embedded etcd, pinned k3s version, extra TLS SANs, persistent data, custom autostart |
| [04-per-node-network.yaml](04-per-node-network.yaml) | 1 | 3 | Per-container IP, DNS and gateway overrides |
| [05-vlan-custom-cidrs.yaml](05-vlan-custom-cidrs.yaml) | 1 | 2 | VLAN tag, MTU 9000, Ubuntu 24.04, custom pod/service CIDRs, `/dev/net/tun` |
| [06-unprivileged-experimental.yaml](06-unprivileged-experimental.yaml) | 1 | 1 | Experimental unprivileged profile, NFS client off |
| [07-many-workers.yaml](07-many-workers.yaml) | 1 | 5 | Large workers, extra packages, staggered autostart, host modules managed manually |
| [08-local-path-storage.yaml](08-local-path-storage.yaml) | 1 | 2 | Per-node host directories for the `local-path` StorageClass; test with [manifests/local-path-pvc-test.yaml](manifests/local-path-pvc-test.yaml) |
| [09-shared-storage.yaml](09-shared-storage.yaml) | 1 | 2 | ONE host directory shared by every container for the `local-shared` StorageClass (ReadWriteMany, no node affinity); test with [manifests/local-shared/pvc-test.yaml](manifests/local-shared/pvc-test.yaml) |

`go test ./examples/` checks that every file parses and validates without
errors against the captured host inventory (network probes are not run).

## manifests/local-shared

The `local-shared` StorageClass that 09-shared-storage.yaml's `shared_data`
enables: it behaves like `local-path` (dynamic per-PVC provisioning, same
directory-per-PVC layout) but without `local-path`'s node affinity, so a
PVC can be mounted from any node. It runs a second, independent
`local-path-provisioner` deployment in upstream's "shared filesystem" mode,
backed by one host directory bind-mounted at `/k3s-shared` in every
container (same directory everywhere, unlike `local-path`'s per-node
directories).

When `shared_data.enabled` is set, pk3s applies
[provisioner.yaml](manifests/local-shared/provisioner.yaml) and
[storageclass.yaml](manifests/local-shared/storageclass.yaml) itself (its
embedded copy is `internal/k3s/manifests/local-shared.yaml` — keep the two
in sync) and creates the bind mount on every container. These files are
also here to deploy and test the same setup by hand, independently of pk3s:

- [manifests/local-shared/provisioner.yaml](manifests/local-shared/provisioner.yaml) — the provisioner deployment
- [manifests/local-shared/storageclass.yaml](manifests/local-shared/storageclass.yaml) — the StorageClass, plus the manual bind-mount steps
- [manifests/local-shared/kustomization.yaml](manifests/local-shared/kustomization.yaml) — deploys both with `kubectl apply -k manifests/local-shared/`
- [manifests/local-shared/pvc-test.yaml](manifests/local-shared/pvc-test.yaml) — a PVC shared by two pods on different nodes, to test it
