# buildkit-operator

[![go.mod](https://img.shields.io/github/go-mod/go-version/seatgeek/buildkit-operator?style=flat-square)](go.mod)
[![LICENSE](https://img.shields.io/github/license/seatgeek/buildkit-operator?style=flat-square)](LICENSE)
[![Build Status](https://img.shields.io/github/actions/workflow/status/seatgeek/buildkit-operator/ci.yml?branch=main&style=flat-square)](https://github.com/seatgeek/buildkit-operator/actions?query=workflow%3Aci+branch%3Amain)
[![Go Report Card](https://goreportcard.com/badge/github.com/seatgeek/buildkit-operator?style=flat-square)](https://goreportcard.com/report/github.com/seatgeek/buildkit-operator)
[![Codecov](https://img.shields.io/codecov/c/github/seatgeek/buildkit-operator?style=flat-square)](https://codecov.io/gh/seatgeek/buildkit-operator)

An operator for managing BuildKit instances on Kubernetes.

## How it Works

First, deploy one or more `BuildkitTemplate` resources that define the configuration and scheduling for BuildKit instances:

```yaml
apiVersion: buildkit.seatgeek.io/v1alpha1
kind: BuildkitTemplate
metadata:
  name: buildkit-arm64
  namespace: some-ns
spec:
  # This is a simplified example; many other spec fields are available.
  rootless: true
  port: 1234

  buildkitdToml: |
    [log]
      format = "json"

    [worker.oci]
      enabled = true
      max-parallelism = 3
      cniPoolSize = 16

    [worker.containerd]
      enabled = false

  resources:
    default:
      requests:
        cpu: 500m
        memory: 8Gi
      limits:
        memory: 8Gi
    maximum:
      cpu: 8
      memory: 16Gi

  scheduling:
    nodeSelector:
      kubernetes.io/arch: arm64
      kubernetes.io/os: linux
    tolerations:
      - key: dedicated
        operator: Equal
        value: buildkit
        effect: NoSchedule
```

Then create any number of `Buildkit` resources that reference the templates:

```yaml
apiVersion: buildkit.seatgeek.io/v1alpha1
kind: Buildkit
metadata:
  name: buildkit-arm64-instance
  namespace: some-ns
spec:
  template: buildkit-arm64
```

The operator will then deploy a BuildKit pod for each `Buildkit` resource, setting the TCP connection URL into the `Buildkit` resource's `status`:

```yaml
# ...
status:
  endpoint: tcp://10.1.2.3:1234
```

Use the `.status.endpoint` field to connect to the BuildKit instance. When you're done, delete the `Buildkit` resource and the associated pod will be cleaned up automatically.

## Installation

### Helm Chart (Recommended)

The easiest way to install the buildkit-operator is using the Helm chart:

```bash
# Install the latest stable release
helm install buildkit-operator \
  oci://ghcr.io/seatgeek/charts/buildkit-operator \
  --namespace buildkit-system \
  --create-namespace
```

#### Installation Options

**Install latest from main branch (bleeding edge):**
```bash
# Note: Chart versions for main branch use format 0.0.0-main-<commit-sha>
# You can find the exact version in GitHub Actions or use --devel flag
helm install buildkit-operator \
  oci://ghcr.io/seatgeek/charts/buildkit-operator \
  --namespace buildkit-system \
  --create-namespace \
  --set image.tag=main \
  --devel
```

**Install a specific version:**
```bash
helm install buildkit-operator \
  oci://ghcr.io/seatgeek/charts/buildkit-operator \
  --version 1.0.0 \
  --namespace buildkit-system \
  --create-namespace
```

**Test a pull request:**
```bash
helm install buildkit-operator \
  oci://ghcr.io/seatgeek/charts/buildkit-operator \
  --namespace buildkit-system \
  --create-namespace \
  --set image.tag=pr-123
```

### Container Images

Container images are available at:
- `ghcr.io/seatgeek/buildkit-operator:latest` - Latest stable release (tagged releases only)
- `ghcr.io/seatgeek/buildkit-operator:main` - Latest from main branch
- `ghcr.io/seatgeek/buildkit-operator:v1.0.0` - Specific version releases
- `ghcr.io/seatgeek/buildkit-operator:pr-123` - Pull request builds

All images support both `linux/amd64` and `linux/arm64` architectures.

**Note**: Development versions from the main branch use commit-based chart versions (e.g., `0.0.0-main-abc12345`) for better traceability and correlation with the source code.

### Uninstallation

```bash
helm uninstall buildkit-operator --namespace buildkit-system
```

## Local Development

### Prerequisites

You will need `kind` and `frpc`, which can be installed on macOS with `brew`:

```bash
brew install kind frpc
```

### Running the Operator

(Re)start your local kind cluster and run the operator:

```bash
make recreate
make run
```

#### Debugging the Operator

If you'd rather run the operator from your IDE with a debugger attached, run these commands instead:

```bash
make recreate
make start_webhook_reverse_proxy # Keep this running in the background until you're done debugging
```

Then have your IDE run `./cmd/operator` in debug mode with the following program arguments set: `--kubeconfig ./kind/kubeconfig --kubecontext kind-buildkit`

#### Interacting with the Cluster

You can interact with the cluster either of these ways:

- `kubectl --kubeconfig ./kind/kubeconfig [command]`
- `k9s --kubeconfig ./kind/kubeconfig --write`

Congratulations! You are now running buildkit-operator locally.
