# Security model

## Why k3s in LXC needs relaxed isolation

k3s runs containerd and runc, which create nested containers (pods). Inside an
LXC container this requires mounting proc/sysfs/overlay filesystems, managing
cgroups, creating device nodes and reading `/dev/kmsg`. The default Proxmox LXC
configuration (AppArmor profile, dropped capabilities, device cgroup, read-only
proc/sys) blocks these operations.

Neither the k3s documentation nor the Proxmox documentation supports running
Kubernetes in LXC. Proxmox recommends a QEMU VM when you need full isolation.
If isolation matters more than density, use VMs.

## Profiles

### `privileged` (default, community-tested)

Requires `security.acknowledge_privileged_risk: true` (a toggle in the TUI).

| Setting | Why |
|---------|-----|
| `unprivileged: 0` | container UID 0 = host UID 0 |
| `features: nesting=1,keyctl=1` | nested container namespaces; keyring syscalls used by runc |
| `lxc.apparmor.profile: unconfined` | containerd/runc mount proc, sysfs and overlay filesystems for pods |
| `lxc.cap.drop:` (empty) | keeps capabilities that Proxmox drops by default, so pods can be given them |
| `lxc.cgroup2.devices.allow: a` | containerd creates device nodes for pods |
| `lxc.mount.auto: proc:rw sys:rw cgroup:rw` | kubelet and kube-proxy write sysctls and manage cgroups |
| `lxc.mount.entry: /dev/kmsg ...` | kubelet's OOM watcher needs `/dev/kmsg` |
| optional `/dev/net/tun` | VPN / WireGuard pods |
| optional `fuse=1` | off by default; Proxmox advises against FUSE in containers |

**Consequence:** a process that escapes a pod and its LXC container is root on
the Proxmox host. That includes every VM, every container and all storage on
the host. Use this profile only on hosts dedicated to workloads you trust.

The profile matches the configuration the project owner had running. Whether
every line is strictly required on current kernels has not been determined
yet. Minimising this set is an open item.

### `unprivileged-experimental`

Container root is mapped to an unprivileged host UID, which preserves host
isolation. Kubelet runs with the alpha `KubeletInUserNamespace` feature gate.
The k3s documentation does not describe this setup, so deployment may fail.
NFS mounts do not work inside unprivileged containers.

## Host changes

pk3s changes the host only in the ways listed in the plan:

- `/etc/modules-load.d/pk3s.conf` and `modprobe` for the modules k3s needs.
  Containers share the host kernel and cannot load modules themselves.
- Optional data directories under the configured base path.
- `pveam download` of a template, only when you enable it.
- Cache of verified k3s binaries in `/var/lib/pk3s/cache`.

pk3s never removes any of these automatically.

## Secrets

- No root password is set in containers. Use `pct enter <id>` or an SSH key.
- The first server gets a random server token and a separate agent token.
  Joining servers use the secure `K10…` server token. Agents only receive the
  agent token, which cannot be used to join servers.
- Tokens are written into `/etc/rancher/k3s/config.yaml` (0600) inside the
  container via `pct push`, from a 0600 temp file that is deleted right after.
  They never appear in command arguments (visible in `ps`) or logs.
- `secrets.json` and `kubeconfig` are stored with mode 0600 in a 0700 directory.
- All log lines and errors pass through a redactor as defence in depth.
  Tests check that no secret reaches command lines, stdin, events or `state.json`.

## Supply chain

- The k3s binary comes from the GitHub release of a pinned version (channels are
  resolved to an exact version at plan time and recorded in the state). It is
  verified against the release's `sha256sum-<arch>.txt`.
- This guarantees integrity, not authenticity: the checksum file comes from the
  same release. Signature verification is not implemented.
- Packages inside containers come from the template's apt repositories.
- Templates are downloaded with `pveam`, which verifies checksums from the
  Proxmox appliance index.
