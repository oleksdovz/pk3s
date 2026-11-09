# pk3s

Deploy [k3s](https://k3s.io) clusters into Proxmox VE LXC containers from an
interactive terminal UI or a YAML file.

pk3s runs on the Proxmox host. It reads the host (storage, bridges, templates,
existing guests), shows a complete plan of what it will create or change, and
deploys only after you confirm. Progress is saved after every step, so a failed
or interrupted deployment can be resumed.

> **Status: early release (v0.1.x).** Tested live on Proxmox VE 9.2:
> create, scaling agents up and down, servers 1→3, remote mode from macOS,
> and the `shared_data` ReadWriteMany StorageClass. Not yet tested live: k3s
> upgrades, CPU/RAM/disk changes, PVCs through `persistent_data`'s
> `local-path` StorageClass, recovery from the state ConfigMap, the Windows
> client and the unprivileged profile. Use a disposable host first: see
> [docs/TESTING.md](docs/TESTING.md).

> **k3s in LXC is not supported upstream.** Neither k3s nor Proxmox document
> this setup. The default `privileged` profile works in practice, but **root
> in the container is root on the Proxmox host**. Read
> [docs/SECURITY.md](docs/SECURITY.md) before you use it.

## Features

- Keyboard-driven TUI with panels and live validation. Also has a scriptable CLI.
- Embedded etcd for every cluster (1, 3, 5 or 7 servers). Servers can be added later as plain joins, and etcd snapshots are taken before upgrades. Quorum rules are enforced.
- Static networking for every container: IP, prefix, gateway, DNS. Hostnames use a configurable prefix.
- Conflict checks for CT IDs, hostnames, IPs (existing guests, host bridges, ping), storage capacity, templates and bridges.
- No `curl | sh`. The k3s binary is downloaded from GitHub releases for a pinned version and verified against the release SHA-256 file.
- Separate server and agent join tokens. Secure `K10…` tokens are delivered in files with mode 0600 and never appear in logs or command lines.
- Optional host-directory bind mount for local-path volumes (`persistent_data`, per node) or a ReadWriteMany `local-shared` StorageClass backed by one directory shared by every node (`shared_data`). NFS client installed by default.
- Resource ledger and Proxmox tags (`pk3s`, `pk3s-<cluster>`). `destroy` only removes containers pk3s created that still carry the tag. Data directories are never deleted.

## Requirements

- Proxmox VE 8.x or 9.x with cgroup v2. Developed against 9.2.
- Root shell on the Proxmox host.
- Internet access from the host (k3s binary, apt inside containers).
- A Debian 12/13 or Ubuntu LTS standard LXC template, or permission to download one.

## Install

### From a release

Every `v*` tag publishes binaries for Linux (amd64, arm64), macOS (arm64,
amd64) and Windows (amd64, arm64) with `SHA256SUMS` on the
[Releases](https://github.com/oleksdovz/pk3s/releases) page. The macOS and
Windows builds contain the Linux binaries for remote mode (`--host`).

```sh
# on the Proxmox host
VER=v0.1.0
curl -fLO https://github.com/oleksdovz/pk3s/releases/download/$VER/pk3s-linux-amd64
curl -fLO https://github.com/oleksdovz/pk3s/releases/download/$VER/SHA256SUMS
sha256sum --ignore-missing -c SHA256SUMS
install -m 0755 pk3s-linux-amd64 /usr/local/bin/pk3s
```

### From source

On the Proxmox host:

```sh
# Build on any machine with Go >= 1.24.2
make build                       # -> dist/pk3s-linux-amd64
scp dist/pk3s-linux-amd64 root@pve:/usr/local/bin/pk3s

# or build on the host
git clone https://github.com/oleksdovz/pk3s && cd pk3s
CGO_ENABLED=0 go build -o /usr/local/bin/pk3s ./cmd/pk3s
```

## Run from your workstation (remote mode)

pk3s can be started on macOS, Windows or Linux and run against a Proxmox
host over SSH (system OpenSSH client; your keys, agent and `~/.ssh/config`
are used):

```sh
pk3s --host root@pve                          # TUI in your terminal
pk3s --host root@pve init lab                 # writes ./lab.yaml locally
pk3s --host root@pve plan  -f lab.yaml        # the local file is uploaded
pk3s --host root@pve apply -f lab.yaml
pk3s --host root@pve kubeconfig lab > ~/.kube/lab.yaml
```

After every successful create or update, pk3s writes `<name>-kubeconfig.yaml`
(mode 0600) into the directory you started it from, locally and in remote
mode alike: `KUBECONFIG=./lab-kubeconfig.yaml kubectl get nodes`. Use
`--kubeconfig-dir DIR` to choose another directory or `--kubeconfig-dir -`
to turn it off.

pk3s checks that the host runs Proxmox VE and that you are root. It uploads
the Linux pk3s binary for the host's architecture to
`/var/lib/pk3s/bin/pk3s-<sha256>`, verifies its SHA-256, and runs it there.
State, logs and the k3s cache stay on the host, so local and remote runs
see the same clusters. Builds from `make dist` embed the Linux binaries;
otherwise pass `--remote-bin PATH`. Options: `--ssh-port`, `--ssh-key`.

## Usage

### Interactive

```sh
pk3s
```

Midnight-Commander-style function keys; the same verb is on the same key on
every screen (dimmed when not available). If the terminal captures F-keys,
use `Alt+1` … `Alt+0`. `Ctrl+L` redraws the screen at the current terminal
size; the layout also follows window resizes automatically.

| Key | Verb |
|-----|------|
| `F1` | Help for the current screen |
| `F2` | Save the spec as `<name>.yaml` |
| `F3` | View cluster status (Refresh on the status screen) |
| `F4` | Edit: a field in the wizard; a deployed cluster (scale, upgrade, resize) |
| `F5` | New cluster |
| `F6` | Resume an interrupted operation |
| `F7` | Export the deployed spec to `<name>.yaml` |
| `F8` | Destroy (type the cluster name to confirm) |
| `F9` | Plan in the wizard; Apply on the plan review |
| `F10` | Quit (Stop while a deployment runs) |

In the wizard: `Tab` switches panels, `↑↓`/`PgUp`/`PgDn` move, `Enter` edits,
`←→` picks from a list, `Space` toggles. The page list ends with
**▶ Review plan & deploy**.

Wizard pages: Cluster, Topology, Resources, Storage, Network, Node network
(per-container IP/gateway/DNS), Persistent data, k3s, OS & access, and Security & host.
Each page shows its validation results live. The review screen lists every
container, every host change and every step. Deployment starts only after
you type `yes`.

### CLI

`pk3s init NAME` scans the host (templates, storages, bridges, DNS, CT IDs
and addresses in use) and writes an annotated spec. The chosen values are
active, and every alternative found on the host is listed in a comment. If
cluster NAME already exists, `init` exports its deployed spec instead, ready
to edit and `apply`. Without redirection the spec goes to `NAME.yaml`; an
existing file is only overwritten with `--force`.

```sh
pk3s init lsb1-k3s > cluster.yaml   # scan the host: annotated starter spec
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
pk3s init lab > lab.yaml                            # exports the deployed spec
$EDITOR lab.yaml                                    # e.g. agents: 4 -> 3
pk3s plan  -f lab.yaml                              # shows the diff only
pk3s apply -f lab.yaml
```

| Change in the spec | What happens |
|--------------------|--------------|
| `agents` up | new agents are created and joined, with the next free CT ID and IP |
| `agents` down | the agent with the highest CT ID goes through cordon → drain → delete node (kubectl on main1), then its container is stopped and destroyed; the data directory stays on the host |
| `servers` 1→3→5→7 | new servers join embedded etcd one at a time |
| `k3s.version` (exact) | rolling upgrade: etcd snapshot, then servers one by one (cordon), then agents (drain, 5 min timeout), each waiting for `Ready` on the new version |
| CPU / RAM / disk / autostart | `pct set` / `pct resize` on the running containers (disks only grow) |

Existing nodes always keep their CT ID, IP and data directory.

The following are rejected by validation:

- downgrades, or skipping a minor version;
- removing servers (not supported yet);
- changing the security profile, k3s networking, disabled components, persistent or shared data, the hostname prefix, or the network of an existing node.

A channel such as `stable` never upgrades a running cluster; set an exact
version to upgrade. An interrupted update continues with `pk3s resume <name>`.

### Cluster state inside the cluster

After every successful `apply`, pk3s writes the spec, the node layout and the
k3s version (no secrets) to the ConfigMap `kube-system/pk3s-state`. If the
host-side state in `/var/lib/pk3s` is missing (reinstalled host, another
Proxmox node), `pk3s plan`/`apply` finds the cluster's containers by the
`pk3s-<name>` tag and restores the state from that ConfigMap through main1.
Join tokens are re-read from main1, and the kubeconfig is exported again.

### Example spec

```yaml
name: lab
hostname_prefix: k3s-lab        # containers: k3s-lab-main1.. (servers), k3s-lab-node1.. (agents)
servers: 3                      # 1/3/5/7, always embedded etcd
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
  host_path: /data/k3s-storage  # -> /data/k3s-storage/lab/<hostname>, local-path StorageClass
shared_data:
  enabled: true
  host_path: /data/k3s-shared   # same directory on every node, local-shared StorageClass (RWX)
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
| `/var/log/pk3s/pk3s.log` | activity log of everything pk3s does: session start, host scan, every executed command (exit code, duration, stderr on failure; never stdout), validation results, TUI edits and confirmations, deployment steps, destroy (redacted, 0600, rotated at 10 MiB) |
| `/var/lib/pk3s/clusters/<name>/pk3s.log` | deployment steps of one cluster |
| `/var/lib/pk3s/cache/k3s/<version>/` | verified k3s binaries |
| `/etc/modules-load.d/pk3s.conf` | host kernel modules (if enabled) |

## Limitations

- All containers are placed on the Proxmox node where pk3s runs. Multi-node placement is planned.
- The API endpoint is the first server's IP, not a floating VIP. With 3+ servers, agents fail over through k3s' client-side load balancer, but your kubeconfig does not. This is not host-level HA: one Proxmox host is a single point of failure.
- IPv4 only.
- Remote mode uploads and runs pk3s on the Proxmox host. A mode that keeps pk3s only on the workstation (SSH command runner) is not implemented.

## Documentation

- [docs/DESIGN.md](docs/DESIGN.md): architecture, deployment flow, LXC analysis, roadmap
- [docs/SECURITY.md](docs/SECURITY.md): security profiles and their trade-offs
- [docs/TESTING.md](docs/TESTING.md): how to test safely on a disposable Proxmox host

## Releasing

Every push to `main` that passes `CI` is released automatically: the
`Release` workflow tags the commit with the next patch version (latest
`vX.Y.Z` + 1, starting at `v0.1.0`), builds all targets with `make dist`
and attaches them and `SHA256SUMS` to the GitHub release. A push of several
commits at once produces one release, for the last commit.

To bump the minor or major version, push a tag yourself; automatic tags
continue from it:

```sh
git tag v0.2.0 && git push origin v0.2.0
```

Tags with a `-`, such as `v0.2.0-rc1`, are marked as pre-releases and do
not affect the automatic numbering. `CI` runs vet, tests with the race
detector, and a cross-compile on every push and pull request.

## License

Apache-2.0
