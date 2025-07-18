# buidkit-operator

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
