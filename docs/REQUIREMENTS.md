# Original requirements

Verbatim brief that started the project (2026-11-06). Later decisions that
refine it are in [DECISIONS.md](DECISIONS.md).

---

Act as a senior DevOps engineer and software architect. Help me design and
implement an open-source CLI tool with a terminal UI for deploying k3s clusters
in Proxmox VE LXC containers.

## Idea

I connect to a Proxmox server over SSH and run the tool directly on that host.
The tool provides an interactive, keyboard-driven terminal UI inspired by
Midnight Commander: panels, menus, forms, clear navigation, progress indicators,
and readable error messages.

For the overall user experience, take inspiration from Proxmox VE Community
Scripts: https://community-scripts.org/. The goal is to simplify a complex
setup, while showing the user what will be created or changed.

## Core workflow

The TUI should ask me for:

1. Cluster name.
2. Number of k3s server nodes (control plane) and agent nodes (workers).
3. Proxmox storage for LXC templates.
4. Proxmox storage for container root disks.
5. Root disk size per container, default: 30 GB.
6. CPU cores per container, default: 2.
7. RAM per container, default: 2 GB.
8. Network bridge, IP addressing, gateway, DNS, and any other settings
   necessary for the nodes to communicate.
9. k3s version and other essential settings.

Use the requested CPU and RAM values as editable defaults, not as a claim that
they are sufficient for every topology or workload. Warn when the selected
resources are unsuitable based on verified requirements.

The tool should then:

- Detect available Proxmox nodes, storage, templates, bridges, and architecture.
- Allow me to select an existing compatible LXC template or download one after
  explicit confirmation.
- Validate the configuration and show a complete deployment plan before making
  changes.
- Create and configure the first control-plane container.
- Apply and verify the LXC and host prerequisites required by the selected
  k3s version and configuration.
- Install k3s, securely handle the node join token, then create and join
  additional server and agent nodes.
- Wait for nodes to become Ready and report the actual cluster status.
- Show a final summary with node names, IPs, roles, resource allocations,
  access instructions, and paths to any generated files.

## Architecture and safety requirements

- Verify current k3s and Proxmox documentation before choosing LXC settings.
  Clearly identify configurations that are unsupported, experimental, or
  dependent on the host kernel.
- Evaluate unprivileged LXC first. If privileged containers, nesting, modified
  AppArmor settings, writable sysctls, or host-level changes are necessary,
  explain the reason and security trade-off. Never silently weaken host security.
- Support both a single-server cluster and a multi-server cluster. Explain and
  validate the chosen datastore, quorum requirements, and stable API endpoint.
  Do not describe multiple server nodes as HA unless the complete design
  actually provides it.
- Let me place containers on different Proxmox nodes when available; for the
  first MVP, single-host deployment is acceptable if multi-host support is
  documented as a later phase.
- Detect resource conflicts, including CT IDs, hostnames, IP addresses, ports,
  and storage capacity. Do not overwrite existing containers or resources.
- Never print or log join tokens, passwords, private keys, or kubeconfig
  credentials. Store necessary secrets with restrictive permissions.
- Handle interruptions and partial failures safely. Record which resources were
  created by this tool, provide clear recovery instructions, and never delete
  existing resources automatically. Ask before cleaning up created resources.
- Make operations repeatable where practical; clearly describe what can be
  resumed or retried.
- Do not use unverified remote scripts with curl | bash. Pin or verify
  dependencies and installation artifacts where practical.
- Keep the tool modular, testable, and understandable. Recommend a suitable
  language and TUI library, and explain the choice briefly.

## What I want from you

1. First, evaluate feasibility, risks, and the proposed LXC approach using
   current official documentation. Highlight any unresolved compatibility
   questions.
2. Propose a practical MVP architecture, including a simple TUI mockup,
   deployment flow, and project structure.
3. Identify decisions that genuinely require my input; use sensible defaults
   for the rest.
4. After we agree on the design, implement the MVP with readable code, a README,
   installation instructions, and meaningful tests. Do not claim live
   validation unless the tool has actually run on a Proxmox host.
5. Explain how I can safely test it on a disposable Proxmox environment before
   using it on my homelab.

Respond in Ukrainian, but write the source code, CLI labels, and project
documentation in English.

A shell script doing similar work was provided as a reference:
[reference/lxc-k3s-test.sh](reference/lxc-k3s-test.sh).
