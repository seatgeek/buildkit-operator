#!/bin/bash

# Script to validate that Helm templates produce output matching the Kubebuilder-generated sources
set -e

WEBHOOK_SOURCE="config/webhook/manifests.yaml"
RBAC_SOURCE="config/rbac/role.yaml"
CRD_SOURCE_DIR="config/crd/bases"
CHART_PATH="charts/buildkit-operator"

# Check if required tools exist
if ! command -v helm &> /dev/null; then
    echo "Error: helm is required but not installed"
    exit 1
fi

if ! command -v yq &> /dev/null; then
    echo "Error: yq is required but not installed"
    exit 1
fi

# Check if source files and directories exist
if [[ ! -f "$WEBHOOK_SOURCE" ]]; then
    echo "Error: $WEBHOOK_SOURCE not found"
    exit 1
fi

if [[ ! -f "$RBAC_SOURCE" ]]; then
    echo "Error: $RBAC_SOURCE not found"
    exit 1
fi

if [[ ! -d "$CRD_SOURCE_DIR" ]]; then
    echo "Error: $CRD_SOURCE_DIR not found"
    exit 1
fi

if [[ ! -d "$CHART_PATH" ]]; then
    echo "Error: $CHART_PATH not found"
    exit 1
fi

# Create temporary directory for validation
TEMP_DIR=$(mktemp -d)
trap "rm -rf $TEMP_DIR" EXIT

echo "Validating Helm templates against Kubebuilder-generated sources..."

# Create a temporary namespace for testing
TEMP_NS="validate-$(date +%s)"

# Render the Helm chart templates
helm template buildkit-operator "$CHART_PATH" \
  --namespace "$TEMP_NS" \
  --set webhook.certManager.enabled=false > "$TEMP_DIR/helm-rendered.yaml"

echo ""
echo "🔍 Validating Webhooks..."

# Extract webhook configurations from rendered templates
yq eval 'select(.kind == "MutatingWebhookConfiguration" or .kind == "ValidatingWebhookConfiguration")' "$TEMP_DIR/helm-rendered.yaml" > "$TEMP_DIR/helm-webhooks.yaml"

# Extract webhook configurations from source (with namespace substitution for comparison)
sed "s/namespace: system/namespace: $TEMP_NS/g" "$WEBHOOK_SOURCE" | \
sed "s/name: webhook-service/name: buildkit-operator-webhook-service/g" | \
sed "s/name: mutating-webhook-configuration/name: buildkit-operator-mutating-webhook-configuration/g" | \
sed "s/name: validating-webhook-configuration/name: buildkit-operator-validating-webhook-configuration/g" > "$TEMP_DIR/source-webhooks.yaml"

# Compare the essential webhook configurations
echo "Comparing webhook paths, rules, and client configs..."

# Extract and compare webhook paths
yq eval '.webhooks[].clientConfig.service.path' "$TEMP_DIR/helm-webhooks.yaml" | sort > "$TEMP_DIR/helm-paths.txt"
yq eval '.webhooks[].clientConfig.service.path' "$TEMP_DIR/source-webhooks.yaml" | sort > "$TEMP_DIR/source-paths.txt"

if ! diff -u "$TEMP_DIR/source-paths.txt" "$TEMP_DIR/helm-paths.txt"; then
    echo "❌ Webhook paths don't match between source and Helm template!"
    echo "Source paths:"
    cat "$TEMP_DIR/source-paths.txt"
    echo "Helm template paths:"
    cat "$TEMP_DIR/helm-paths.txt"
    exit 1
fi

# Extract and compare webhook rules
yq eval '.webhooks[].rules' "$TEMP_DIR/helm-webhooks.yaml" | sort > "$TEMP_DIR/helm-rules.yaml"
yq eval '.webhooks[].rules' "$TEMP_DIR/source-webhooks.yaml" | sort > "$TEMP_DIR/source-rules.yaml"

if ! diff -u "$TEMP_DIR/source-rules.yaml" "$TEMP_DIR/helm-rules.yaml"; then
    echo "❌ Webhook rules don't match between source and Helm template!"
    echo "Source rules:"
    cat "$TEMP_DIR/source-rules.yaml"
    echo "Helm template rules:"
    cat "$TEMP_DIR/helm-rules.yaml"
    exit 1
fi

# Extract and compare webhook names
yq eval '.webhooks[].name' "$TEMP_DIR/helm-webhooks.yaml" | sort > "$TEMP_DIR/helm-names.txt"
yq eval '.webhooks[].name' "$TEMP_DIR/source-webhooks.yaml" | sort > "$TEMP_DIR/source-names.txt"

if ! diff -u "$TEMP_DIR/source-names.txt" "$TEMP_DIR/helm-names.txt"; then
    echo "❌ Webhook names don't match between source and Helm template!"
    echo "Source names:"
    cat "$TEMP_DIR/source-names.txt"
    echo "Helm template names:"
    cat "$TEMP_DIR/helm-names.txt"
    exit 1
fi

echo "✅  Webhook templates correctly match the source webhook definitions!"

echo ""
echo "🔍 Validating RBAC..."

# Extract ClusterRole from rendered templates
yq eval 'select(.kind == "ClusterRole" and .metadata.name == "buildkit-operator-manager")' "$TEMP_DIR/helm-rendered.yaml" > "$TEMP_DIR/helm-rbac.yaml"

# Handle any implicit vs explicit apiGroups difference consistently
yq eval '.rules | map(select(.apiGroups == null) |= . + {"apiGroups": [""]} | .)' "$RBAC_SOURCE" > "$TEMP_DIR/source-rbac-normalized.yaml"
yq eval '.rules | map(select(.apiGroups == null) |= . + {"apiGroups": [""]} | .)' "$TEMP_DIR/helm-rbac.yaml" > "$TEMP_DIR/helm-rbac-normalized.yaml"

if ! diff -u "$TEMP_DIR/source-rbac-normalized.yaml" "$TEMP_DIR/helm-rbac-normalized.yaml"; then
    echo "❌ RBAC rules don't match between source and Helm template!"
    echo ""
    echo "Source rules (normalized):"
    cat "$TEMP_DIR/source-rbac-normalized.yaml"
    echo ""
    echo "Helm template rules (normalized):"
    cat "$TEMP_DIR/helm-rbac-normalized.yaml"
    exit 1
fi

echo "✅  RBAC templates correctly match the source ClusterRole definition!"

echo ""
echo "🔍 Validating CRDs..."

# Compare each CRD file
CRD_CHART_DIR="$CHART_PATH/crds"
for source_crd in "$CRD_SOURCE_DIR"/*.yaml; do
    crd_filename=$(basename "$source_crd")
    chart_crd="$CRD_CHART_DIR/$crd_filename"

    if [[ ! -f "$chart_crd" ]]; then
        echo "❌ CRD file $crd_filename not found in chart directory!"
        exit 1
    fi

    echo "Comparing $crd_filename..."

    # Compare CRD content (should be identical since we copy them directly in make generate)
    if ! diff -u "$source_crd" "$chart_crd"; then
        echo "❌ CRD $crd_filename doesn't match between source and chart!"
        exit 1
    fi
done

# Check for extra CRD files in chart directory
for chart_crd in "$CRD_CHART_DIR"/*.yaml; do
    crd_filename=$(basename "$chart_crd")
    source_crd="$CRD_SOURCE_DIR/$crd_filename"

    if [[ ! -f "$source_crd" ]]; then
        echo "❌ Extra CRD file $crd_filename found in chart directory but not in source!"
        exit 1
    fi
done

echo "✅  CRD files correctly match between source and chart directories!"

echo ""
echo "🎉 All Helm templates correctly match their Kubebuilder-generated sources!"
