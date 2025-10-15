REPORTS_DIR=build/reports

.PHONY: all
all: clean generate lint test validate-helm-templates build

.PHONY: clean
clean:
	@rm -rf $(REPORTS_DIR)

.PHONY: lint
lint: golangci-lint
	$(GOLANGCI_LINT) run ./... ./api/...

.PHONY: lint-fix
lint-fix: golangci-lint goimports-reviser
	$(GOIMPORTS_REVISER) -rm-unused -set-alias -format -company-prefixes github.com/seatgeek/buildkit-operator ./... ./api/...
	$(GOLANGCI_LINT) run --fix ./... ./api/...
	make tidy

.PHONY: tidy
tidy:
	go mod tidy
	cd api && go mod tidy
	go work sync
	go mod download

.PHONY: generate
generate: controller-gen client-gen yq
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="{./api/..., ./internal/webhooks/...}" output:crd:artifacts:config=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=manager-role paths="{./internal/controllers/...}"
	$(CONTROLLER_GEN) object paths="{./api/...}"
	cp config/webhook/manifests.yaml kind/webhook/manifests.yaml
	rm charts/buildkit-operator/crds/*
	cp config/crd/bases/*.yaml charts/buildkit-operator/crds/
	rm -rf api/client
	$(CLIENT_GEN) \
 		--output-dir=api/client \
 		--output-pkg=github.com/seatgeek/buildkit-operator/api/client \
		--input-base= \
		--input github.com/seatgeek/buildkit-operator/api/v1alpha1 \
 		--clientset-name=versioned
	curl -sL https://raw.githubusercontent.com/seatgeek/buildkit-prestop-script/$(BUILDKIT_PRESTOP_VERSION)/buildkit-prestop.sh -o internal/prestop/buildkit-prestop.sh

.PHONY: validate-helm-templates
validate-helm-templates: generate yq
	./hack/validate-helm-templates.sh

.PHONY: test
test: generate envtest
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -v ./...

.PHONY: test-fix
test-fix: generate envtest
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test -v ./... -update -clean

.PHONY: test-with-coverage
test-with-coverage: gotestsum ensure-reports-dir generate envtest
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" $(GOTESTSUM) --junitfile $(REPORTS_DIR)/unit-tests.xml -- -race -coverprofile=$(REPORTS_DIR)/coverage.out -covermode=atomic -v -cover ./...
	make report-coverage

.PHONY: ensure-reports-dir
ensure-reports-dir:
	@mkdir -p $(REPORTS_DIR)

.PHONY: report-coverage
report-coverage: gocover-cobertura
	@sed -i.bak '/\/mock_.*\.go/d' $(REPORTS_DIR)/coverage.out
	@sed -i.bak '/internal\/example\.go/d' $(REPORTS_DIR)/coverage.out
	@go tool cover -func=$(REPORTS_DIR)/coverage.out
	$(GOCOVER_COBERTURA) < $(REPORTS_DIR)/coverage.out > $(REPORTS_DIR)/coverage.xml

.PHONY: build
build: generate
	go build cmd/operator/main.go

.PHONY: build-docker
build-docker: generate
	docker build -t buildkit-operator:latest -f Dockerfile .

.PHONY: start_webhook_reverse_proxy
start_webhook_reverse_proxy:
	killall frpc || true
	frpc -c ./kind/webhook/frpc.toml

##@ Kind cluster
KUBECONFIG=kind/kubeconfig
CLUSTER_NAME=buildkit
KUBECONTEXT=kind-$(CLUSTER_NAME)
TMPDIR_VAR=$(shell echo $${TMPDIR:-/tmp})
BUILDKIT_IMAGE=moby/buildkit:latest

.PHONY: run
run: generate
	echo "Starting webhook reverse proxy..."; \
	make start_webhook_reverse_proxy & \
	echo "Starting operator..."; \
	go run cmd/operator/main.go --kubeconfig $(KUBECONFIG) --kubecontext $(KUBECONTEXT) & \
	wait

.PHONY: create
create: check_cluster create_cluster create_namespace pull_images apply_crds create_local_webhook_cert apply_webhook_config
	@echo "Your cluster is ready! You can now run 'make run' to start your operator."

.PHONY: delete
delete:
	kind delete cluster --name $(CLUSTER_NAME)

.PHONY: recreate
recreate: delete create

.PHONY: check_cluster
check_cluster:
	@if kind get clusters | grep -q $(CLUSTER_NAME); then \
		echo "Cluster '$(CLUSTER_NAME)' already exists. Use 'make delete' or 'make recreate' to proceed."; \
		exit 1; \
	fi

.PHONY: create_cluster
create_cluster:
	kind create cluster --kubeconfig $(KUBECONFIG) --config kind/kind-config.yaml --name $(CLUSTER_NAME)

.PHONY: create_namespace
create_namespace:
	kubectl --kubeconfig $(KUBECONFIG) create ns buildkit-system

.PHONY: pull_images
pull_images:
	docker pull $(BUILDKIT_IMAGE)
	kind load docker-image $(BUILDKIT_IMAGE) --name $(CLUSTER_NAME)

.PHONY: apply_crds
apply_crds:
	kubectl --kubeconfig $(KUBECONFIG) apply --server-side -f config/crd/bases

.PHONY: apply_webhook_config
apply_webhook_config:
	kubectl --kubeconfig $(KUBECONFIG) apply -k kind/webhook

	kubectl --kubeconfig $(KUBECONFIG) patch mutatingwebhookconfiguration mutating-webhook-configuration \
		--type='json' \
		-p="[ \
				{ \
					\"op\": \"replace\", \
					\"path\": \"/webhooks/0/clientConfig/caBundle\", \
					\"value\": \"$$(cat $(TMPDIR_VAR)/k8s-webhook-server/serving-certs/tls.crt | base64 | tr -d '\n')\" \
				} \
			]"
	kubectl --kubeconfig $(KUBECONFIG) patch validatingwebhookconfiguration validating-webhook-configuration \
		--type='json' \
		-p="[ \
				{ \
					\"op\": \"replace\", \
					\"path\": \"/webhooks/0/clientConfig/caBundle\", \
					\"value\": \"$$(cat $(TMPDIR_VAR)/k8s-webhook-server/serving-certs/tls.crt | base64 | tr -d '\n')\" \
				}, \
				{ \
					\"op\": \"replace\", \
					\"path\": \"/webhooks/1/clientConfig/caBundle\", \
					\"value\": \"$$(cat $(TMPDIR_VAR)/k8s-webhook-server/serving-certs/tls.crt | base64 | tr -d '\n')\" \
				} \
			]"

.PHONY: create_local_webhook_cert
create_local_webhook_cert:
	mkdir -p $(TMPDIR_VAR)/k8s-webhook-server/serving-certs

	openssl req -x509 -nodes -days 365 \
  -newkey rsa:2048 \
  -keyout $(TMPDIR_VAR)/k8s-webhook-server/serving-certs/tls.key \
  -out $(TMPDIR_VAR)/k8s-webhook-server/serving-certs/tls.crt \
  -subj "/CN=webhook-service.default.svc" \
	-addext "subjectAltName = DNS:webhook-service.default.svc"

##@ Build Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

# go-install-tool will 'go install' any package with custom target and name of binary, if it doesn't exist
# $1 - target path with name of binary (ideally with version)
# $2 - package url which can be installed
# $3 - specific version of package
define go-install-tool
@[ -f $(1) ] || { \
set -e; \
package=$(2)@$(3) ;\
echo "Downloading $${package}" ;\
GOBIN=$(LOCALBIN) go install $${package} ;\
src="$$(echo '$(1)' | sed 's/-$(3)$$//')" ;\
dest='$(1)' ;\
if [ "$$src" != "$$dest" ]; then \
	mv "$$src" "$$dest" ; \
fi ; \
}
endef

## Tool Binaries and Versions
# renovate: datasource=go depName=github.com/kubernetes-sigs/kustomize/kustomize
KUSTOMIZE_VERSION ?= v5.6.0
KUSTOMIZE ?= $(LOCALBIN)/kustomize-$(KUSTOMIZE_VERSION)
# renovate: datasource=go depName=sigs.k8s.io/controller-tools
CONTROLLER_TOOLS_VERSION ?= v0.16.5
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)
# renovate: datasource=go depName=k8s.io/code-generator
CLIENT_GEN_VERSION ?= v0.32.7
CLIENT_GEN ?= $(LOCALBIN)/client-gen-$(CLIENT_GEN_VERSION)
# renovate: datasource=go depName=sigs.k8s.io/controller-runtime
ENVTEST_VERSION ?= release-0.20
ENVTEST ?= $(LOCALBIN)/setup-envtest-$(ENVTEST_VERSION)
ENVTEST_K8S_VERSION = 1.32
# renovate: datasource=go depName=github.com/incu6us/goimports-reviser/v3
GOIMPORTS_REVISER_VERSION ?= v3.10.0
GOIMPORTS_REVISER ?= $(LOCALBIN)/goimports-reviser-$(GOIMPORTS_REVISER_VERSION)
# renovate: datasource=go depName=github.com/golangci/golangci-lint
GOLANGCI_LINT_VERSION ?= v2.5.0
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)
# renovate: datasource=go depName=gotest.tools/gotestsum
GOTESTSUM_VERSION ?= v1.13.0
GOTESTSUM = $(LOCALBIN)/gotestsum-$(GOTESTSUM_VERSION)
# renovate: datasource=go depName=github.com/boumenot/gocover-cobertura
GOCOVER_COBERTURA_VERSION ?= v1.4.0
GOCOVER_COBERTURA = $(LOCALBIN)/gocover-cobertura-$(GOCOVER_COBERTURA_VERSION)
# renovate: datasource=go depName=github.com/mikefarah/yq/v4
YQ_VERSION ?= v4.48.1
YQ ?= $(LOCALBIN)/yq # no version suffix, as we need to reference this outside of this Makefile
# renovate: datasource=github-releases depName=seatgeek/buildkit-prestop-script
BUILDKIT_PRESTOP_VERSION ?= v1.2.0

.PHONY: kustomize
kustomize: $(KUSTOMIZE)
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN)
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: client-gen
client-gen: $(CLIENT_GEN)
$(CLIENT_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CLIENT_GEN),k8s.io/code-generator/cmd/client-gen,$(CLIENT_GEN_VERSION))

.PHONY: envtest
envtest: $(ENVTEST)
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: goimports-reviser
goimports-reviser: $(GOIMPORTS_REVISER)
$(GOIMPORTS_REVISER): $(LOCALBIN)
	$(call go-install-tool,$(GOIMPORTS_REVISER),github.com/incu6us/goimports-reviser/v3,$(GOIMPORTS_REVISER_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT)
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/v2/cmd/golangci-lint,${GOLANGCI_LINT_VERSION})

.PHONY: gotestsum
gotestsum: $(GOTESTSUM)
$(GOTESTSUM): $(LOCALBIN)
	$(call go-install-tool,$(GOTESTSUM),gotest.tools/gotestsum,$(GOTESTSUM_VERSION))

.PHONY: gocover-cobertura
gocover-cobertura: $(GOCOVER_COBERTURA)
$(GOCOVER_COBERTURA): $(LOCALBIN)
	$(call go-install-tool,$(GOCOVER_COBERTURA),github.com/boumenot/gocover-cobertura,$(GOCOVER_COBERTURA_VERSION))

.PHONY: yq
yq: $(YQ)
$(YQ): $(LOCALBIN)
	$(call go-install-tool,$(YQ),github.com/mikefarah/yq/v4,$(YQ_VERSION))
