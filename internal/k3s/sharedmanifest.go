package k3s

import _ "embed"

// SharedStorageManifest is the local-shared-provisioner Deployment, RBAC
// and StorageClass applied once a cluster has shared_data enabled. See
// manifests/local-shared.yaml for what it does and why.
//
//go:embed manifests/local-shared.yaml
var SharedStorageManifest []byte
