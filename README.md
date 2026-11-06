# pk3s

Deploy [k3s](https://k3s.io) clusters into Proxmox VE LXC containers from an
interactive terminal UI or a YAML file.

pk3s runs on the Proxmox host. It reads the host (storage, bridges, templates,
existing guests), shows a complete plan of what it will create or change, and
deploys only after you confirm. Progress is saved after every step, so a failed
or interrupted deployment can be resumed.

> **Status: MVP, not yet validated end-to-end on a live host.** Read-only
> commands (`init`, `plan`) have been run against Proxmox VE 9.2. Container
> creation has only been tested against a simulator. Use a disposable host
> first: see [docs/TESTING.md](docs/TESTING.md).

> **k3s in LXC is not supported upstream.** Neither k3s nor Proxmox document
> this setup. The default `privileged` profile works in practice, but **root
> in the container is root on the Proxmox host**. Read
> [docs/SECURITY.md](docs/SECURITY.md) before you use it.

## Features

- Keyboard-driven TUI with panels and live validation. Also has a scriptable CLI.
- Single-server clusters (SQLite) and 3/5/7-server clusters with embedded etcd. Quorum rules are enforced.
- Static networking for every container: IP, prefix, gateway, DNS. Hostnames use a configurable prefix.
- Conflict checks for CT IDs, hostnames, IPs (existing guests, host bridges, ping), storage capacity, templates and bridges.
- No `curl | sh`. The k3s binary is downloaded from GitHub releases for a pinned version and verified against the release SHA-256 file.
- Separate server and agent join tokens. Secure `K10…` tokens are delivered in files with mode 0600 and never appear in logs or command lines.
- Optional host-directory bind mount for local-path volumes. NFS client installed by default.
- Resource ledger and Proxmox tags (`pk3s`, `pk3s-<cluster>`). `destroy` only removes containers pk3s created that still carry the tag. Data directories are never deleted.

## Requirements

- Proxmox VE 8.x or 9.x with cgroup v2. Developed against 9.2.
- Root shell on the Proxmox host.
- Internet access from the host (k3s binary, apt inside containers).
- A Debian 12/13 or Ubuntu LTS standard LXC template, or permission to download one.

## Install

On the Proxmox host:

```sh
# Build on any machine with Go >= 1.24.2
make build                       # -> dist/pk3s-linux-amd64
scp dist/pk3s-linux-amd64 root@pve:/usr/local/bin/pk3s

# or build on the host
git clone https://github.com/oleksdovz/pk3s && cd pk3s
CGO_ENABLED=0 go build -o /usr/local/bin/pk3s ./cmd/pk3s
```

## Usage

### Interactive

```sh
pk3s
```

| Key | Action |
|-----|--------|
| `Tab` | switch between the page list and the form |
| `↑↓` / `PgUp` `PgDn` | move between fields / pages |
| `Enter` | edit a field, toggle, or cycle a choice |
| `←→` | pick from a list (storage, bridge, template, profile) |
| `F2` | save the spec as `<name>.yaml` |
| `F9` | build and review the plan |
| `Esc` | back |
| `F10` | quit |

Wizard pages: Cluster, Topology, Resources, Storage, Network, Node network
(per-container IP/gateway/DNS), Persistent data, k3s, OS & access, and Security & host.
Each page shows its validation results live. The review screen lists every
container, every host change and every step. Deployment starts only after
you type `yes`.

### CLI

```sh
pk3s init > cluster.yaml         # starter spec filled from this host
$EDITOR cluster.yaml             # set network.first_ip, review the rest
pk3s plan  -f cluster.yaml       # validate + show the plan (changes nothing)
pk3s apply -f cluster.yaml       # plan, confirm with "yes", deploy
pk3s status lab                  # containers + Kubernetes node status
pk3s resume lab                  # continue after a failure or Ctrl-C
pk3s destroy lab                 # remove pk3s-created containers (type the name)
pk3s list
```

Ctrl-C during `apply` finishes the current step and stops. Press it a second time to abort that step.

### Updating a running cluster

There are no separate scale or upgrade commands. Edit the spec and apply it
again. pk3s compares it with the deployed cluster, shows the changes, and
applies them one at a time after you type `yes`. In the TUI, open the
cluster from the main menu and press `e`.

```sh
cp /var/lib/pk3s/clusters/lab/spec.yaml lab.yaml   # the deployed spec
$EDITOR lab.yaml                                    # e.g. agents: 4 -> 3
pk3s plan  -f lab.yaml                              # shows the diff only
pk3s apply -f lab.yaml
```

| Change in the spec | What happens |
|--------------------|--------------|
| `agents` up | new agents are created and joined, with the next free CT ID and IP |
| `agents` down | the agent with the highest CT ID is drained, deleted from Kubernetes and destroyed; its data directory stays on the host |
| `servers` 3→5→7 | new servers join embedded etcd one at a time |
| `k3s.version` (exact) | rolling upgrade: datastore backup (etcd snapshot or SQLite copy), servers one by one (cordon), then agents (drain, 5 min timeout), each waiting for `Ready` on the new version |
| CPU / RAM / disk / autostart | `pct set` / `pct resize` on the running containers (disks only grow) |

Existing nodes always keep their CT ID, IP and data directory.

The following are rejected by validation:

- downgrades, or skipping a minor version;
- going from 1 to 3 servers (needs SQLite → etcd conversion) and removing servers (not supported yet);
- changing the security profile, k3s networking, disabled components, persistent data, the hostname prefix, or the network of an existing node.

A channel such as `stable` never upgrades a running cluster; set an exact
version to upgrade. An interrupted update continues with `pk3s resume <name>`.

### Example spec

```yaml
name: lab
hostname_prefix: k3s-lab        # containers: k3s-lab-main1.. (servers), k3s-lab-node1.. (agents)
servers: 3                      # 1 = SQLite; 3/5/7 = embedded etcd
agents: 2
ctid_start: 5000
template:
  storage: local
  name: debian-13-standard_13.1-2_amd64.tar.zst
  download: false
rootfs_storage: local-lvm
server_resources: {cores: 2, memory_mib: 4096, disk_gib: 30}
agent_resources:  {cores: 2, memory_mib: 2048, disk_gib: 30}
network:
  bridge: vmbr0
  first_ip: 192.168.0.52        # nodes get consecutive addresses
  prefix: 24
  gateway: 192.168.0.1
  dns: [192.168.0.2]
node_network:                   # optional per-container overrides
  - node: node2
    ip: 192.168.0.60
    dns: [1.1.1.1]
k3s:
  version: stable               # or an exact release, e.g. v1.37.2+k3s1
  disable: [traefik]
security:
  profile: privileged
  acknowledge_privileged_risk: true
persistent_data:
  enabled: true
  host_path: /data/k3s-storage  # -> /data/k3s-storage/lab/<hostname>
os:
  nfs_client: true
onboot: true
startup:                        # Proxmox boot order; shutdown runs in reverse
  main_order: 1                 # main (servers) start first and stop last
  main_delay_sec: 10            # wait after main before starting nodes
  node_order: 2
  node_delay_sec: 10            # nodes start 10 s apart
access:
  ssh_public_key_file: /root/.ssh/id_ed25519.pub
```

Unknown keys are rejected, so a typo cannot silently fall back to a default.
More complete examples are in [examples/](examples/).

## Files

| Path | Content |
|------|---------|
| `/var/lib/pk3s/clusters/<name>/state.json` | step progress and the ledger of created resources |
| `/var/lib/pk3s/clusters/<name>/secrets.json` | join tokens (0600) |
| `/var/lib/pk3s/clusters/<name>/kubeconfig` | admin kubeconfig pointing at the first server (0600) |
| `/var/lib/pk3s/clusters/<name>/pk3s.log` | redacted event log |
| `/var/lib/pk3s/cache/k3s/<version>/` | verified k3s binaries |
| `/etc/modules-load.d/pk3s.conf` | host kernel modules (if enabled) |

## Limitations

- All containers are placed on the Proxmox node where pk3s runs. Multi-node placement is planned.
- The API endpoint is the first server's IP, not a floating VIP. With 3+ servers, agents fail over through k3s' client-side load balancer, but your kubeconfig does not. This is not host-level HA: one Proxmox host is a single point of failure.
- IPv4 only.
- Remote mode (`pk3s --host <proxmox>` from macOS/Windows) is planned. See [docs/DESIGN.md](docs/DESIGN.md).

## Documentation

- [docs/DESIGN.md](docs/DESIGN.md): architecture, deployment flow, LXC analysis, roadmap
- [docs/SECURITY.md](docs/SECURITY.md): security profiles and their trade-offs
- [docs/TESTING.md](docs/TESTING.md): how to test safely on a disposable Proxmox host

## License

Apache-2.0
