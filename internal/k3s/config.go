package k3s

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Role of a node.
type Role string

// Node roles.
const (
	RoleServer Role = "server"
	RoleAgent  Role = "agent"
)

// ServiceName returns the systemd unit name for a role.
func (r Role) ServiceName() string {
	if r == RoleServer {
		return "k3s"
	}
	return "k3s-agent"
}

// Paths inside the containers.
const (
	ConfigPath       = "/etc/rancher/k3s/config.yaml"
	BinaryPath       = "/usr/local/bin/k3s"
	ServerTokenPath  = "/var/lib/rancher/k3s/server/token"
	AgentTokenPath   = "/var/lib/rancher/k3s/server/agent-token"
	KubeconfigPath   = "/etc/rancher/k3s/k3s.yaml"
	LocalStoragePath = "/var/lib/rancher/k3s/storage"
)

// NodeConfig is rendered to /etc/rancher/k3s/config.yaml. It holds join
// tokens and is therefore always written with mode 0600 and never logged.
type NodeConfig struct {
	Server              string   `yaml:"server,omitempty"`
	Token               string   `yaml:"token,omitempty"`
	AgentToken          string   `yaml:"agent-token,omitempty"`
	ClusterInit         bool     `yaml:"cluster-init,omitempty"`
	NodeIP              string   `yaml:"node-ip,omitempty"`
	NodeName            string   `yaml:"node-name,omitempty"`
	TLSSAN              []string `yaml:"tls-san,omitempty"`
	Disable             []string `yaml:"disable,omitempty"`
	ClusterCIDR         string   `yaml:"cluster-cidr,omitempty"`
	ServiceCIDR         string   `yaml:"service-cidr,omitempty"`
	WriteKubeconfigMode string   `yaml:"write-kubeconfig-mode,omitempty"`
	KubeletArg          []string `yaml:"kubelet-arg,omitempty"`
	KubeProxyArg        []string `yaml:"kube-proxy-arg,omitempty"`
	NodeLabel           []string `yaml:"node-label,omitempty"`
}

// Render returns the YAML document.
func (c NodeConfig) Render() ([]byte, error) {
	body, err := yaml.Marshal(c)
	if err != nil {
		return nil, err
	}
	return append([]byte("# Managed by pk3s. Contains secrets: keep mode 0600.\n"), body...), nil
}

// GenerateToken returns a random 32-byte hex secret.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SystemdUnit renders the service unit, modelled on the upstream install
// script. Kernel modules are loaded on the host, not here.
func SystemdUnit(role Role) string {
	return fmt.Sprintf(`# Managed by pk3s.
[Unit]
Description=Lightweight Kubernetes (%[1]s)
Documentation=https://k3s.io
Wants=network-online.target
After=network-online.target

[Install]
WantedBy=multi-user.target

[Service]
Type=notify
KillMode=process
Delegate=yes
User=root
LimitNOFILE=1048576
LimitNPROC=infinity
LimitCORE=infinity
TasksMax=infinity
TimeoutStartSec=0
Restart=always
RestartSec=5s
ExecStart=%[2]s %[1]s
`, role, BinaryPath)
}

// NodeStatus is a parsed node from `kubectl get nodes -o json`.
type NodeStatus struct {
	Name    string
	Ready   bool
	Roles   []string
	Version string
	IP      string
}

// ParseNodes parses kubectl JSON output.
func ParseNodes(data []byte) ([]NodeStatus, error) {
	var doc struct {
		Items []struct {
			Metadata struct {
				Name   string            `json:"name"`
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
				Addresses []struct {
					Type    string `json:"type"`
					Address string `json:"address"`
				} `json:"addresses"`
				NodeInfo struct {
					KubeletVersion string `json:"kubeletVersion"`
				} `json:"nodeInfo"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("decode nodes: %w", err)
	}
	var out []NodeStatus
	for _, it := range doc.Items {
		n := NodeStatus{Name: it.Metadata.Name, Version: it.Status.NodeInfo.KubeletVersion}
		for _, c := range it.Status.Conditions {
			if c.Type == "Ready" {
				n.Ready = c.Status == "True"
			}
		}
		for _, a := range it.Status.Addresses {
			if a.Type == "InternalIP" {
				n.IP = a.Address
			}
		}
		for k := range it.Metadata.Labels {
			if r, ok := strings.CutPrefix(k, "node-role.kubernetes.io/"); ok {
				n.Roles = append(n.Roles, r)
			}
		}
		out = append(out, n)
	}
	return out, nil
}

// RewriteKubeconfig points the kubeconfig at the external endpoint and
// renames the default context to the cluster name.
func RewriteKubeconfig(kc []byte, endpoint, cluster string) []byte {
	s := string(kc)
	s = strings.ReplaceAll(s, "https://127.0.0.1:6443", "https://"+endpoint+":6443")
	s = strings.ReplaceAll(s, "name: default", "name: "+cluster)
	s = strings.ReplaceAll(s, "cluster: default", "cluster: "+cluster)
	s = strings.ReplaceAll(s, "user: default", "user: "+cluster)
	s = strings.ReplaceAll(s, "current-context: default", "current-context: "+cluster)
	return []byte(s)
}
