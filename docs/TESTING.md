# Testing pk3s safely

## 1. Automated tests (any machine)

```sh
go test ./...
```

What they cover:

- **Inventory parsing** against real `pvesh` output captured read-only from a
  Proxmox VE 9.2 host (`internal/pve/testdata/hp`).
- **Plan layout**: IDs, IPs, hostname prefix, per-node overrides, token routing, step order.
- **Validation**: quorum, resources, conflicts with existing guests, IPs, storage,
  templates, package-name injection, system paths.
- **Engine** against a Proxmox/k3s simulator:
  - a full deployment;
  - no secret leaks into command lines, stdin, events or state;
  - resume after a failed step;
  - refusal to touch a container that pk3s did not create;
  - stop between steps.
- **TUI**: wizard defaults, editing, per-node network, plan review, typed confirmation.

The tests do not prove that k3s starts inside LXC on your kernel. Only a live
run can show that.

## 2. A disposable Proxmox environment

Use one of these. Do not start on your production homelab host.

**A. Nested Proxmox in a VM (recommended).**

1. Create a VM on any hypervisor: 4+ vCPU, 16 GB RAM, 100 GB disk, CPU type
   `host`, one bridged NIC.
2. Install Proxmox VE 9.x from the ISO. LXC does not need nested hardware
   virtualization, but enabling it does no harm.
3. Add a `local-lvm` (LVM-thin) storage. That is the installer default.
4. **Take a VM snapshot** before you run pk3s. Rolling back is your cleanup.

**B. A spare physical host** that you can reinstall.

Pick an IP range on the test network that nothing else uses. pk3s pings every
planned address and refuses addresses that answer, but devices that ignore
ping are not detected.

## 3. Step-by-step first run

```sh
# on the test host, as root
pk3s init test-k3s > cluster.yaml
# set first_ip to a free range, servers: 1, agents: 1 for the first try
pk3s plan -f cluster.yaml      # read-only: inventory, ping, channel lookup
```

Read the plan: containers, host changes, security profile. Then:

```sh
pk3s apply -f cluster.yaml
pk3s status <name>
export KUBECONFIG=/var/lib/pk3s/clusters/<name>/kubeconfig
kubectl get nodes -o wide
kubectl run t --image=busybox --restart=Never -- sleep 30 && kubectl get pod t -o wide
```

Then try the failure paths:

- Press Ctrl-C during `apply`, then run `pk3s resume <name>`.
- Run `pk3s destroy <name>` and check that only the cluster's containers are gone
  (`pct list`). Data directories and `/etc/modules-load.d/pk3s.conf` stay.
- Create a container by hand with a CT ID in the planned range and check that
  `plan` refuses it.
- Scale up: 3 servers, 2 agents. Stop one server container and check the API
  still answers through another server.

## 4. Moving to the homelab

- Keep the first deployment small (1 server, 1 agent) and watch `pk3s.log`.
- Pick CT IDs and IPs away from existing guests. `plan` checks both.
- Remember the security profile: privileged containers expose the whole host.

## Live validation log

| Date | Host | What ran | Result |
|------|------|----------|--------|
| 2026-11-06 | `hp`, PVE 9.2.26, kernel 7.0.14 | `pk3s init`, `pk3s plan` (read-only) | Inventory correct (storages, templates, bridge gateway, DNS, CT 9000). Plan blocked 192.168.0.51 because it answers ping (a non-Proxmox device). No changes made. |
| 2026-11-06 | `hp`, PVE 9.2.26, kernel 7.0.14, privileged profile, Debian 13.1 template, LVM-thin | `pk3s apply` (1 server + 1 agent) | Steps 1-7 OK: host modules loaded, k3s v1.37.2+k3s1 downloaded and checksum-verified, CT created, profile applied, OS prepared without warnings, k3s server installed. **k3s server came up Ready inside LXC** (coredns, traefik, metrics-server, local-path running). Step 8 failed because pk3s called `k3s` by name and `pct exec` has no `/usr/local/bin` in PATH; fixed by using the absolute path. |
| 2026-11-06 | same | `pk3s resume lab` (pre-rename build + PATH fix) | Completed steps skipped; remaining 10 steps OK. Tokens read (secure `K10` format), agent joined, **2/2 nodes Ready**, kubeconfig exported. Independently verified: no token in `pk3s.log`/`state.json`; state files 0600 in a 0700 dir; nginx pod scheduled on the agent answered HTTP 200 from the server over flannel VXLAN; `mount.nfs` present; kubeconfig works from a macOS workstation over the LAN. |
| 2026-11-06 | same | owner: `apply` of `test-lb1` from the TUI (1 main + 2 node) | Containers were put on `vmbr100` (192.168.100.0/24, internal NAT bridge) with 192.168.0.x addresses: no gateway or DNS, so apt hung in "Prepare OS". F10 and Ctrl+C could not stop it (old build: F10 unbound; cancelled `pct exec` left pipes open). Fixed: bridge/subnet validation, network preflight + apt timeouts, process-group kill with WaitDelay. TUI also overflowed the window after a resize; fixed. |
