# buidkit-operator

[![go.mod](https://img.shields.io/github/go-mod/go-version/seatgeek/buildkit-operator?style=flat-square)](go.mod)
[![LICENSE](https://img.shields.io/github/license/seatgeek/buildkit-operator?style=flat-square)](LICENSE)
[![Build Status](https://img.shields.io/github/actions/workflow/status/seatgeek/buildkit-operator/ci.yml?branch=main&style=flat-square)](https://github.com/seatgeek/buildkit-operator/actions?query=workflow%3Aci+branch%3Amain)
[![Go Report Card](https://goreportcard.com/badge/github.com/seatgeek/buildkit-operator?style=flat-square)](https://goreportcard.com/report/github.com/seatgeek/buildkit-operator)
[![Codecov](https://img.shields.io/codecov/c/github/seatgeek/buildkit-operator?style=flat-square)](https://codecov.io/gh/seatgeek/buildkit-operator)

An operator for managing BuildKit instances on Kubernetes.

## How it Works

First, deploy one or more `BuildkitTemplate` resources that define a pod template and `buildkitd.toml` configuration for BuildKit instances:

```yaml
apiVersion: buildkit.seatgeek.io/v1alpha1
kind: BuildkitTemplate
metadata:
  name: buildkit-arm64
  namespace: some-ns
spec:
  buildkitdToml: |
    [log]
      format = "json"

    [worker.oci]
      enabled = true
      max-parallelism = 3
      cniPoolSize = 16

    [worker.containerd]
      enabled = false

  template:
    metadata:
      labels:
        app.kubernetes.io/instance: buildkit-arm64
    spec:
      containers:
        - name: buildkit
          securityContext:
            privileged: true
            runAsNonRoot: false
            readOnlyRootFilesystem: false
            allowPrivilegeEscalation: true
            seccompProfile:
              type: Unconfined
            appArmorProfile:
              type: Unconfined
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

The operator will then deploy a BuildKit pod for each `Buildkit` resource, setting the TCP connection URL into the resource's status:

```yaml
apiVersion: buildkit.seatgeek.io/v1alpha1
kind: Buildkit
metadata:
  name: buildkit-arm64-instance
  namespace: some-ns
spec: ~ # hidden for brevity
status:
  endpoint: tcp://10.1.2.3:1234
```

Use the `.status.endpoint` field to connect to the BuildKit instance. When you're done, delete the `Buildkit` resource and the associated pod will be cleaned up automatically.

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

Congratulations! You are now running the Buildkit Operator locally.
