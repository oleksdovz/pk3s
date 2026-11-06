package k3s

import (
	"encoding/json"
	"fmt"
	"time"
)

// State ConfigMap: a copy of the pk3s cluster state kept inside the
// cluster, so the state can be recovered from the cluster itself. It never
// contains secrets.
const (
	StateConfigMapName      = "pk3s-state"
	StateConfigMapNamespace = "kube-system"
)

// ClusterState is what pk3s stores in the ConfigMap.
type ClusterState struct {
	SpecYAML   string // spec.yaml
	LayoutJSON string // []plan.NodeRecord as JSON
	K3sVersion string
	UpdatedAt  time.Time
	Tool       string
}

// StateConfigMap renders the ConfigMap manifest as JSON for kubectl apply.
func StateConfigMap(s ClusterState) ([]byte, error) {
	doc := map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      StateConfigMapName,
			"namespace": StateConfigMapNamespace,
			"labels":    map[string]string{"app.kubernetes.io/managed-by": "pk3s"},
		},
		"data": map[string]string{
			"spec.yaml":   s.SpecYAML,
			"layout.json": s.LayoutJSON,
			"k3s-version": s.K3sVersion,
			"updated-at":  s.UpdatedAt.UTC().Format(time.RFC3339),
			"tool":        s.Tool,
		},
	}
	return json.Marshal(doc)
}

// ParseStateConfigMap reads `kubectl get configmap -o json` output.
func ParseStateConfigMap(data []byte) (ClusterState, error) {
	var doc struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ClusterState{}, fmt.Errorf("decode %s: %w", StateConfigMapName, err)
	}
	d := doc.Data
	if d["spec.yaml"] == "" || d["layout.json"] == "" || !IsExactVersion(d["k3s-version"]) {
		return ClusterState{}, fmt.Errorf("%s/%s is incomplete", StateConfigMapNamespace, StateConfigMapName)
	}
	t, _ := time.Parse(time.RFC3339, d["updated-at"])
	return ClusterState{SpecYAML: d["spec.yaml"], LayoutJSON: d["layout.json"], K3sVersion: d["k3s-version"], UpdatedAt: t, Tool: d["tool"]}, nil
}
