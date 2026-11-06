# Design

## Goals

- Show the user exactly what will be created or changed before anything happens.
- Never modify or delete resources pk3s did not create.
- Survive interruption: every step is recorded and can be resumed.
- Keep secrets out of logs, process lists and state files.
- Keep it modular so the UI, the host access layer and the k3s logic can be tested in isolation.

## Architecture

```
                 ┌──────────── cmd/pk3s (CLI) ───────────┐
                 │                                       │
           internal/tui (Bubble Tea)              internal/report
                 │                                       │
                 └──────────────► internal/app ◄─────────┘
                                      │
   pvesh/pct ─► internal/pve ─► Inventory ─┐
                                           ├─► internal/plan ─► Plan ─► internal/engine ─► internal/state
   user/YAML ─► internal/spec ─► Spec ─────┤        (pure)                (steps, events)     (state, ledger,
                                           └─► internal/validate                                secrets)
                                                    (pure)
   all host commands ─► internal/runner (Exec | Fake | future SSH)
```

- **runner**: every external command goes through `runner.Runner`. Tests use
  `runner.Fake` or a simulator.
- **pve**: reads inventory from `pvesh … --output-format json`, not from
  human-readable `pct list` output. Wraps `pct` and `pveam`.
- **spec**: the user input. YAML decoding rejects unknown fields.
- **plan**: a pure function `Build(spec, inventory, version) → Plan`. The plan
  holds the node layout (CT IDs, hostnames, IPs, per-node network), host
  changes and ordered steps with stable IDs.
- **validate**: pure checks returning findings (error/warning/info). Network
  probes (ping) and filesystem probes are injected.
- **engine**: runs steps and saves state after each one. It emits events for
  the UI and log, and checks container ownership by tag before acting.
- **state**: `state.json` (steps + ledger), `secrets.json` (0600), a lock
  file so two processes cannot work on one cluster.
- **tui**: wizard pages built from field descriptors bound to the spec, with
  live validation, plan review, typed confirmation, progress and summary.

## Deployment flow

1. Inventory (read-only).
2. Spec from the wizard or YAML, with defaults taken from the host (gateway,
   prefix, DNS, search domain, storage, template).
3. Validate the spec, resolve the k3s channel to an exact version, build the plan,
   then validate the plan (conflicts, ping, data directories).
4. Show the plan and require typed confirmation (`yes`).
5. Steps:
   1. load host modules (if enabled);
   2. download the template (if missing and allowed);
   3. download and verify the k3s binary;
   4. for each node, servers first, one at a time:
      - create the CT (tags `pk3s`, `pk3s-<cluster>`, no password, swap 0);
      - apply the LXC profile and bind mount;
      - start the CT and wait for systemd;
      - prepare the OS (apt packages, sysctls, verify `/dev/kmsg` and cgroup v2);
      - push the k3s binary, systemd unit and 0600 config;
      - start k3s without blocking;
      - wait for readiness (server: `/readyz`; agent: node registered);
      - after the first server, read the secure join tokens;
   5. wait until every node is `Ready`;
   6. export a kubeconfig rewritten to the endpoint.
6. Summary: nodes, IPs, roles, resources, kubeconfig path, access commands.

### Idempotency and resume

- Completed steps are skipped on `resume`. The artifact step always re-runs,
  because it is cheap and re-verifies the cached binary.
- `ct-create`:
  - if the CT is in the ledger, continue;
  - if it exists with the cluster tag but is not in the ledger (crash between
    create and save), adopt it;
  - otherwise refuse.
- Raw `lxc.*` lines are appended only if missing. Configs with snapshot
  sections are never edited.
- Tokens are generated once and saved before use, so a resumed run uses the same tokens.

## Datastore and endpoint

| Servers | Datastore | Fault tolerance |
|---------|-----------|-----------------|
| 1 | embedded etcd (`cluster-init`) | none; can grow to 3 later |
| 2 | rejected | quorum 2: losing either server stops the control plane |
| 3 / 5 / 7 | embedded etcd (`cluster-init` on the first) | 1 / 2 / 3 server containers |

SQLite is never used: a single server also runs embedded etcd, so adding
servers later is a join (no datastore conversion) and etcd snapshots are
available for backups.

All containers are on one Proxmox host, so the host is a single point of
failure. pk3s never calls this "HA".

The registration address is the first server's IP. Its certificate SANs also
cover all server IPs and any user-provided SANs. Agents learn every server
after joining (k3s client-side load balancer). The exported kubeconfig points
at the first server. A floating VIP (kube-vip) is planned.

## Known LXC issues handled

- **Kernel modules** cannot be loaded from a container. They are loaded on the
  host and persisted.
- **`nf_conntrack_max`** is host-global. kube-proxy gets
  `conntrack-max-per-core=0` so it does not try to change it.
- **`systemctl start` blocks forever** for a broken `Type=notify` unit with
  `TimeoutStartSec=0`. pk3s uses `--no-block` and polls readiness.
- **Swap**: containers are created with `swap 0`.
- **ZFS root disks**: containerd's overlayfs needs OpenZFS ≥ 2.2. pk3s warns.
- **Proxmox firewall** may block VXLAN (UDP 8472). pk3s warns when the
  firewall is enabled.

## Roadmap

## Updating a running cluster

`apply` on an existing cluster calls `plan.BuildUpdate`, which compares the
target spec with the recorded node layout (`state.json` → `layout`) and
produces operation-scoped steps (`op-<timestamp>/…`). The order is:

1. container settings (`pct set`, `pct resize`);
2. rolling k3s upgrade: etcd snapshot, servers one at a time (cordon →
   new binary → restart → Ready on the new version → uncordon), then agents
   (drain instead of cordon);
3. node removal, highest CT ID first: cordon → drain → delete node (kubectl
   on main1) → stop → destroy (only with the cluster tag); the data
   directory is kept;
4. new nodes, servers first;
5. readiness check, then the state ConfigMap.

The target spec is stored as `pending` in `state.json` before the first
step. `spec.yaml`, `layout` and the version are replaced only after the last
step, so `resume` continues the same operation.

## Cluster state ConfigMap

`kube-system/pk3s-state` holds `spec.yaml`, `layout.json`, `k3s-version`
and `updated-at`. It is written through `kubectl apply -f -` on main1 at the
end of every create/update. The host state stays authoritative, because it
is needed before main1 exists (creation), while main1 restarts (upgrade),
and for cleanup after failures. The ConfigMap is a recovery source: with no
host state, pk3s finds main1 by the `pk3s-<name>` tag and rebuilds
`state.json`, `spec.yaml`, `secrets.json` (tokens re-read from main1) and
the kubeconfig.

### Remote mode: run from a workstation (planned)

Goal: `pk3s --host root@pve` from macOS (arm64), Windows or Linux.

- Add an `SSHRunner` that implements `runner.Runner`. It runs
  `ssh -o BatchMode=yes <host> -- <shell-quoted command>` (system OpenSSH,
  which also ships with Windows 10+ and macOS), so the user's existing keys,
  agent and `~/.ssh/config` work. Stdin is forwarded, which `pct exec … sh -s`
  and `tee` already use.
- Move the remaining host-side file operations behind the runner:
  - `engine.FS` writes (modules file, data directories, temp configs for
    `pct push`) become `install -m 0600 /dev/stdin <path>` over SSH;
  - the state store keeps its files on the Proxmox host
    (`/var/lib/pk3s`), so local and remote runs see the same state and lock.
    The local machine keeps only an optional kubeconfig copy (0600).
- k3s artifacts: download on the Proxmox host (`curl -fLo` + `sha256sum -c`
  through the runner) so the workstation's architecture and bandwidth do not
  matter. Checksums are still compared in Go.
- Validation probes (ping, data directories) run on the Proxmox host, not on
  the workstation.
- Cross-compiled builds for windows/darwin already compile (the lock uses
  build tags).

### Other phases

- Multi-host placement: a `node` per container, storage and bridge checks per
  Proxmox node, `pct` calls routed to the right node (or `pvesh create
  /nodes/<n>/lxc`).
- Floating API endpoint with kube-vip; HA wording only when control-plane
  containers are spread across several Proxmox hosts.
- `add-agent` / scale-out for existing clusters.
- Minimise the privileged profile through experiments on current kernels.
- Air-gapped installs (k3s airgap image tarballs, already published with checksums).
