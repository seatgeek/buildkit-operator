# buildkit-operator Helm Chart

[![GitHub Package Registry](https://img.shields.io/badge/ghcr.io-charts-blue?style=flat-square)](https://github.com/seatgeek/buildkit-operator/pkgs/container/charts%2Fbuildkit-operator)
[![LICENSE](https://img.shields.io/github/license/seatgeek/buildkit-operator?style=flat-square)](https://github.com/seatgeek/buildkit-operator/blob/HEAD/LICENSE)

A Helm chart for deploying the BuildKit Operator on Kubernetes. The BuildKit Operator manages BuildKit instances through custom resources, enabling dynamic BuildKit deployments with configurable templates and instances.

## Quick Start

```bash
helm install buildkit-operator \
  oci://ghcr.io/seatgeek/charts/buildkit-operator \
  --namespace buildkit-system \
  --create-namespace
```

### With Configuration

Create a `values.yaml` file with your customizations:

```yaml
# High availability setup
replicaCount: 3

# Resource configuration
operator:
  resources:
    requests:
      memory: "128Mi"
      cpu: "100m"
    limits:
      memory: "256Mi"
      cpu: "500m"

  nodeSelector:
    kubernetes.io/os: linux
```

Then install with your custom values:

```bash
helm install buildkit-operator \
  oci://ghcr.io/seatgeek/charts/buildkit-operator \
  --namespace buildkit-system \
  --create-namespace \
  --values values.yaml
```

## Configuration

### Common Configuration Options

| Parameter                     | Description                               | Default                              |
|-------------------------------|-------------------------------------------|--------------------------------------|
| `replicaCount`                | Number of operator replicas for HA        | `2`                                  |
| `image.repository`            | Operator container image repository       | `ghcr.io/seatgeek/buildkit-operator` |
| `image.tag`                   | Operator container image tag              | `""` (uses chart appVersion)         |
| `operator.leaderElection`     | Enable leader election for HA             | `true`                               |
| `operator.resources`          | Resource limits/requests for operator     | See [values.yaml](./values.yaml)     |
| `webhook.enabled`             | Enable admission webhooks                 | `true`                               |
| `webhook.certManager.enabled` | Use cert-manager for webhook certificates | `true`                               |
| `rbac.create`                 | Create RBAC resources                     | `true`                               |
| `crds.install`                | Install CRDs with chart                   | `true`                               |

### Values Reference

For a complete list of configurable values, see the [values.yaml](./values.yaml) file.

## Chart Packages

Chart packages are available at:
- **OCI Registry**: `oci://ghcr.io/seatgeek/charts/buildkit-operator`
- **GitHub Packages**: https://github.com/seatgeek/buildkit-operator/pkgs/container/charts%2Fbuildkit-operator

## Development

This chart uses templates that are kept in sync with kubebuilder-generated manifests.

To validate template synchronization after making changes:

```bash
make validate-helm-templates
```

For more information about the BuildKit Operator itself, see the [main project README](../../README.md).
