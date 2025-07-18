# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a Kubernetes operator that manages BuildKit instances on Kubernetes. The operator uses the Kubebuilder framework and implements two main CRDs:

- **Buildkit**: Represents a BuildKit instance that references a BuildkitTemplate
- **BuildkitTemplate**: Defines the pod template and configuration for BuildKit instances

The operator uses the Achilles SDK for finite state machine-based reconciliation and includes webhook validation/defaulting for both resources.

## Development Commands

### Build and Test
```bash
# Build the operator
make build

# Run all tests
make test

# Generate code, mocks, CRDs, etc.
make generate

# Lint and format
make lint
make lint-fix
```

### Kind Cluster Management

A Kind cluster can be used for local testing. The cluster is named `buildkit` with context `kind-buildkit`. Its kubeconfig is located at `./kind/kubeconfig`.

```bash
# (Re)create local kind cluster and run operator
make recreate
make run

# Or, for debugging with IDE
make recreate
make start_webhook_reverse_proxy  # Keep running in background
# Then run ./cmd/operator with args: --kubeconfig ./kind/kubeconfig --kubecontext kind-buildkit

# Interact with cluster
kubectl --kubeconfig ./kind/kubeconfig [command]
```

## Architecture

### Core Components

- **api/v1alpha1/**: Contains the v1alpha1 CRD definitions
  - `buildkit_types.go`: Buildkit resource spec and status
  - `buildkit_template_types.go`: BuildkitTemplate resource spec

- **internal/controllers/buildkit/**: Buildkit controller logic
  - `reconciler.go`: FSM-based reconciler that manages BuildKit pods
  - `builder.go`: Pod construction logic
  - `conditions.go`: Status condition constants
  - `resources/`: Resource manipulation utilities

- **internal/controllers/buildkit_template/**: BuildkitTemplate controller logic
  - `reconciler.go`: FSM-based reconciler for BuildkitTemplate resources
  - `builder.go`: Template construction logic
  - `conditions.go`: Status condition constants

- **internal/webhooks/**: Admission webhook handlers
  - `buildkit.go`: Validation webhook for Buildkit resources
  - `buildkit_template.go`: Validation and defaulting webhook for BuildkitTemplate resources

(Make sure to update the list above as core components are added, modified, or removed!)

### Key Patterns

- Uses Achilles SDK for finite state machine reconciliation
- Controller manages Pod lifecycle based on BuildkitTemplate configuration
- Webhook validation ensures proper resource configuration
- Status tracking includes endpoint information and managed resource references

#### Achilles SDK FSM Framework

Controllers use the Achilles SDK FSM (Finite State Machine) framework, which provides:

- **State-based reconciliation**: Controllers define explicit states with transitions
- **Built-in observability**: Automatic metrics and condition tracking
- **Idempotent operations**: All paths must be idempotent and dependent on externally persisted state
- **Resource management**: Changes to managed objects go through `OutputSet` abstraction

Documentation about the Achilles SDK can be found at these URLs:

- [FSM reconciler](https://raw.githubusercontent.com/reddit/achilles-sdk/refs/heads/main/docs/sdk-fsm-reconciler.md)
- [Applying objects via OutputSets](https://raw.githubusercontent.com/reddit/achilles-sdk/refs/heads/main/docs/sdk-apply-objects.md)
- [Writing finalizers](https://raw.githubusercontent.com/reddit/achilles-sdk/refs/heads/main/docs/sdk-finalizers.md)
- [Built-in metrics and monitoring](https://raw.githubusercontent.com/reddit/achilles-sdk/refs/heads/main/docs/sdk-metrics.md)

### Dependencies

- **Kubebuilder v4**: Framework for building Kubernetes operators
- **Achilles SDK**: Provides FSM reconciliation and utilities
- **controller-runtime**: Core controller mechanics

## Testing Guidelines

- Integration/UAT tests for controllers and webhooks use the Ginkgo/Gomega framework with `envtest`.
  - See https://raw.githubusercontent.com/reddit/achilles-sdk/refs/heads/main/docs/envtest.md for specifics about writing these kinds of tests.
- Unit tests use table-based tests, `t.Parallel()` for parallel execution, and `testify` for assertions.
