# buidkit-operator

[![go.mod](https://img.shields.io/github/go-mod/go-version/seatgeek/buildkit-operator?style=flat-square)](go.mod)
[![LICENSE](https://img.shields.io/github/license/seatgeek/buildkit-operator?style=flat-square)](LICENSE)
[![Build Status](https://img.shields.io/github/actions/workflow/status/seatgeek/buildkit-operator/ci.yml?branch=main&style=flat-square)](https://github.com/seatgeek/buildkit-operator/actions?query=workflow%3Aci+branch%3Amain)
[![Go Report Card](https://goreportcard.com/badge/github.com/seatgeek/buildkit-operator?style=flat-square)](https://goreportcard.com/report/github.com/seatgeek/buildkit-operator)
[![Codecov](https://img.shields.io/codecov/c/github/seatgeek/buildkit-operator?style=flat-square)](https://codecov.io/gh/seatgeek/buildkit-operator)

An operator for managing BuildKit instances on Kubernetes.

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
