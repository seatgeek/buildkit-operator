# buildkit-operator

[![go.mod](https://img.shields.io/github/go-mod/go-version/seatgeek/buildkit-operator?style=flat-square)](go.mod)
[![LICENSE](https://img.shields.io/github/license/seatgeek/buildkit-operator?style=flat-square)](LICENSE)
[![Build Status](https://img.shields.io/github/actions/workflow/status/seatgeek/buildkit-operator/ci.yml?branch=main&style=flat-square)](https://github.com/seatgeek/buildkit-operator/actions?query=workflow%3Aci+branch%3Amain)
[![Codecov](https://img.shields.io/codecov/c/github/seatgeek/buildkit-operator?style=flat-square)](https://codecov.io/gh/seatgeek/buildkit-operator)

An operator for managing BuildKit instances on Kubernetes.

## Getting Started

Install [mise](https://mise.jdx.dev) for managing tool versions, then:

```bash
make init    # Install tools (Go, kind, frpc) and download dependencies
make create  # Create a local Kind cluster
make run     # Run the operator
```

Run `make help` to see all available targets.

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

### Mutual TLS

By default buildkitd accepts any client that can reach its port. When several tenants share a namespace, set `requireClientTLS: true` on the `BuildkitTemplate` so only the client that created a `Buildkit` can use it:

1. The client generates its own CA and a client certificate signed by it, and puts only the CA certificate in the `Buildkit`'s `spec.clientCA` (immutable). Private keys never leave the client.
2. The operator generates a server certificate for that instance, stores it in an immutable Secret owned by the `Buildkit` and named after the certificate's fingerprint (`<name>-tls-<16 hex>`), and starts buildkitd with `--tlscacert` set to the client CA, so connections without a certificate signed by it are refused. A `Buildkit`'s name can be at most 232 characters when its template requires TLS.

   The certificate is issued to outlive the pod, so TLS pods always have a deadline: `lifecycle.activeDeadlineSeconds` defaults to 90 days for these templates and may be at most 366 days. Each certificate lasts the deadline plus the termination grace period plus an hour for scheduling delay, and the operator re-checks it against the pod's actual start time before advertising the pod (issuing a new one and restarting the pod if it started too late). A running pod's certificate is never rotated, so builds aren't interrupted; long-lived instances end at their deadline instead. The client CA must be valid when the `Buildkit` is created; you get a warning if it expires before the pod's deadline.
3. Once ready, the status carries what the client needs to verify the server:

```yaml
status:
  endpoint: tcp://10.1.2.3:1234
  serverCA: |
    -----BEGIN CERTIFICATE-----
    ...
  serverName: buildkit-arm64-instance
```

```bash
docker buildx create --driver remote \
  --driver-opt cacert=server-ca.pem,cert=client.pem,key=client-key.pem,servername=buildkit-arm64-instance \
  tcp://10.1.2.3:1234
```

Kubelet gRPC probes can't present a client certificate, so these pods also run a `health-proxy` sidecar that serves only `grpc.health.v1` in plaintext on `healthPort` (default `1235`), answering each check from buildkitd's unix socket. The operator needs the Helm value `clientTLS.namespaces` to list the namespaces where such templates are used; it gets `get`/`create`/`delete` on Secrets only there. Turning `requireClientTLS` on stops any pod of that template still running without TLS within about a second, skipping its usual grace period; it is replaced by a TLS pod if its `Buildkit` has a `clientCA`.

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

Install [mise](https://mise.jdx.dev) for managing tool versions, then run `make init` to install Go, kind, and frpc automatically.

### Running the Operator

Bootstrap your environment and start a local kind cluster:

```bash
make init
make create
make run
```

Or to recreate an existing cluster:

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
