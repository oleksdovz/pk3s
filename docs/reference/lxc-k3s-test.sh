#!/usr/bin/env bash
# Reference only: the owner's working script on host "hp" before pk3s.
# Kept to document the LXC settings known to work. Do not run as-is.
set -Eeuo pipefail

CLUSTER_NAME="lxc-k3s"
TEMPLATE="debian-13-standard_13.1-2_amd64.tar.zst"
BRIDGE="vmbr0"
CIDR="24"
GATEWAY="192.168.0.1"
DNS="192.168.0.1"
STORAGE="x-data"
TEMPLATE_STORAGE="local"
CT_PASSWD='<redacted>'
BASE_MOUNT_DIR="/data/k3s-storage/${CLUSTER_NAME}"
MASTER_ID=5000
MASTER_IP="192.168.0.51"
AGENTS=("5001:192.168.0.52" "5002:192.168.0.53" "5003:192.168.0.54" "5004:192.168.0.55")
CORES=2
MEMORY=4096
DISK=20
K3S_CHANNEL="stable"
K3S_SERVER_ARGS="server --tls-san=${MASTER_IP}  --write-kubeconfig /root/.kube/config --write-kubeconfig-mode 600"

# create_ct:
#   pct create "$id" "${TEMPLATE_STORAGE}:vztmpl/${TEMPLATE}" --hostname "${CLUSTER_NAME}-${id}" \
#     --cores "$CORES" --memory "$MEMORY" --swap 0 --onboot 1 --ostype debian --password "$CT_PASSWD" \
#     --net0 "name=eth0,bridge=${BRIDGE},ip=${ip}/${CIDR},gw=${GATEWAY}" --nameserver "$DNS" \
#     --rootfs "${STORAGE}:${DISK}" --unprivileged 0 --features "nesting=1,keyctl=1,fuse=1"
#   appended to /etc/pve/lxc/<id>.conf:
#     timezone: Europe/Kyiv
#     lxc.apparmor.profile: unconfined
#     lxc.cap.drop:
#     lxc.cgroup2.devices.allow: a
#     lxc.mount.auto: proc:rw sys:rw cgroup:rw
#     lxc.mount.entry: /dev/kmsg dev/kmsg none bind,optional,create=file
#     lxc.cgroup2.devices.allow: c 10:200 rwm
#     lxc.mount.entry: /dev/net/tun dev/net/tun none bind,optional,create=file
#   pct set "$id" -mp0 "${BASE_MOUNT_DIR}/ct-${id},mp=/var/lib/rancher/k3s/storage"
#
# prepare_node (inside CT):
#   apt-get install curl ca-certificates openssh-server iproute2 iptables nftables ebtables ethtool
#     socat conntrack open-iscsi kmod
#   swapoff; /etc/modules-load.d/k8s.conf (overlay, br_netfilter); modprobe (no effect in a CT)
#   /etc/sysctl.d/99-kubernetes-cri.conf: bridge-nf-call-iptables/ip6tables=1, ip_forward=1
#   [ -e /dev/kmsg ] || ln -sf /dev/console /dev/kmsg; pct reboot
#
# install_master: curl -sfL https://get.k3s.io | INSTALL_K3S_CHANNEL=stable INSTALL_K3S_EXEC="$K3S_SERVER_ARGS" sh -
#   token copied from /var/lib/rancher/k3s/server/node-token to /tmp/<cluster>-k3s-token
# install_agent: curl -sfL https://get.k3s.io | K3S_URL=https://${MASTER_IP}:6443 K3S_TOKEN=<token> sh -
# delete_cluster: pct stop/destroy by ID + rm -rf of mount dirs (no ownership check)
#
# Issues addressed by pk3s: curl | sh, hard-coded root password, token in /tmp and in
# process arguments, destroy by ID without ownership check, status only lists 3 agents,
# modprobe inside the CT is a no-op.
