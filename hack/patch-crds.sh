#!/usr/bin/env bash

# Adapted from github.com/DataDog/extendeddaemonset
# (c) Datadog; licensed under the Apache License 2.0

set -e

ROOT_DIR=$(git rev-parse --show-toplevel)
YQ="$ROOT_DIR/bin/yq"

# Ensure yq is available
make -C "$ROOT_DIR" yq

# Update `metadata` attribute of v1.PodTemplateSpec to properly validate the
# resource's metadata, since the automatically generated validation is
# insufficient.
#
# For more context, see:
#   - https://github.com/kubernetes/kubernetes/issues/54579
#   - https://github.com/DataDog/extendeddaemonset/pull/21
#   - https://github.com/DataDog/extendeddaemonset/pull/108
echo "Patching metadata validation into BuildkitTemplate's spec.template.metadata..."
$YQ w -i "$ROOT_DIR/config/crd/bases/buildkit.seatgeek.io_buildkittemplates.yaml" "spec.versions[0].schema.openAPIV3Schema.properties.spec.properties.template.properties.metadata" -f "$ROOT_DIR/hack/patch-crd-metadata.yaml"
