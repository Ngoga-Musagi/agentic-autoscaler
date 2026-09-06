
# Image URL to use all building/pushing image targets
IMG ?= controller:latest
# ENVTEST_K8S_VERSION refers to the version of kubebuilder assets to be downloaded by envtest binary.
ENVTEST_K8S_VERSION = 1.29.0

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

# CONTAINER_TOOL defines the container tool to be used for building images.
# Be aware that the target commands are only tested with Docker which is
# scaffolded by default. However, you might want to replace it to use other
# tools. (i.e. podman)
CONTAINER_TOOL ?= docker

# Setting SHELL to bash allows bash commands to be executed by recipes.
# Options are set to exit when a recipe line exits non-zero or a piped command fails.
SHELL = /usr/bin/env bash -o pipefail
.SHELLFLAGS = -ec

.PHONY: all
all: build

##@ General

# The help target prints out all targets with their descriptions organized
# beneath their categories. The categories are represented by '##@' and the
# target descriptions by '##'. The awk command is responsible for reading the
# entire set of makefiles included in this invocation, looking for lines of the
# file as xyz: ## something, and then pretty-format the target and help. Then,
# if there's a line with ##@ something, that gets pretty-printed as a category.
# More info on the usage of ANSI control characters for terminal formatting:
# https://en.wikipedia.org/wiki/ANSI_escape_code#SGR_parameters
# More info on the awk command:
# http://linuxcommand.org/lc3_adv_awk.php

.PHONY: help
help: ## Display this help.
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

##@ Development

.PHONY: manifests
manifests: controller-gen ## Generate WebhookConfiguration, ClusterRole and CustomResourceDefinition objects.
	$(CONTROLLER_GEN) rbac:roleName=manager-role crd webhook paths="./..." output:crd:artifacts:config=config/crd/bases

.PHONY: generate
generate: controller-gen ## Generate code containing DeepCopy, DeepCopyInto, and DeepCopyObject method implementations.
	$(CONTROLLER_GEN) object:headerFile="hack/boilerplate.go.txt" paths="./..."

.PHONY: fmt
fmt: ## Run go fmt against code.
	go fmt ./...

.PHONY: vet
vet: ## Run go vet against code.
	go vet ./...

.PHONY: test
test: manifests generate fmt vet envtest ## Run tests.
	KUBEBUILDER_ASSETS="$(shell $(ENVTEST) use $(ENVTEST_K8S_VERSION) --bin-dir $(LOCALBIN) -p path)" go test $$(go list ./... | grep -v /e2e) -coverprofile cover.out

# Utilize Kind or modify the e2e tests to load the image locally, enabling compatibility with other vendors.
.PHONY: test-e2e  # Run the e2e tests against a Kind k8s instance that is spun up.
test-e2e:
	go test ./test/e2e/ -v -ginkgo.v

##@ Local development (kind)

HELM         ?= helm
KIND_CLUSTER ?= agentic-autoscaler-dev
DEV_IMG      ?= agentic-autoscaler:dev
DEV_NS       ?= agentic-autoscaler-system
LOADGEN_IMG  ?= agentic-autoscaler-loadgen:dev
FAULTGEN_IMG ?= agentic-autoscaler-faultgen:dev
# Load generator knobs (override on the command line, e.g. make load-up RPS=400).
LOAD_TARGET  ?= http://payment-service.production:9898
RPS          ?= 200
DURATION     ?= 600
ERRPCT       ?= 0
DELAYMS      ?= 0
CURL_IMG     ?= curlimages/curl:8.7.1

.PHONY: kind-create
kind-create: ## Create a local kind cluster (requires Docker).
	kind get clusters | grep -q $(KIND_CLUSTER) \
		&& echo "cluster $(KIND_CLUSTER) already exists" \
		|| kind create cluster --config hack/kind-config.yaml --name $(KIND_CLUSTER)
	kubectl config use-context kind-$(KIND_CLUSTER)
	@echo "✓ cluster ready: $$(kubectl get nodes -o wide --no-headers | awk '{print $$1, $$5}')"

.PHONY: kind-delete
kind-delete: ## Delete the local kind cluster.
	kind delete cluster --name $(KIND_CLUSTER)

.PHONY: observability-stack
observability-stack: ## Install Prometheus + Loki + Grafana into the monitoring namespace.
	kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f -
	@echo "→ Adding Helm repos..."
	$(HELM) repo add prometheus-community https://prometheus-community.github.io/helm-charts 2>/dev/null || true
	$(HELM) repo add grafana https://grafana.github.io/helm-charts 2>/dev/null || true
	$(HELM) repo update
	@echo "→ Installing kube-prometheus-stack (Prometheus + Grafana + Alertmanager)..."
	$(HELM) upgrade --install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
		--namespace monitoring \
		--set grafana.adminPassword=admin \
		--set grafana.sidecar.dashboards.enabled=true \
		--set grafana.sidecar.dashboards.label=grafana_dashboard \
		--set prometheus.prometheusSpec.podMonitorSelectorNilUsesHelmValues=false \
		--set prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues=false \
		--wait --timeout 5m
	@echo "→ Installing Loki + Promtail..."
	$(HELM) upgrade --install loki grafana/loki-stack \
		--namespace monitoring \
		--set promtail.enabled=true \
		--set loki.persistence.enabled=false \
		--wait --timeout 3m
	@echo "→ Importing decision-audit dashboard..."
	kubectl create configmap agentic-autoscaler-dashboard \
		--from-file=agentic-autoscaler-decisions.json=deploy/grafana/dashboard.json \
		--namespace monitoring \
		--dry-run=client -o yaml | kubectl apply -f -
	kubectl label configmap agentic-autoscaler-dashboard \
		grafana_dashboard=1 -n monitoring --overwrite
	@echo "✓ Observability stack ready."

.PHONY: dev-secrets
dev-secrets: ## Create operator secrets. Reads ANTHROPIC_API_KEY from your shell env.
	kubectl create namespace $(DEV_NS) --dry-run=client -o yaml | kubectl apply -f -
	@if [ -n "$$ANTHROPIC_API_KEY" ]; then \
		echo "→ Creating AI secret from ANTHROPIC_API_KEY env var..."; \
	else \
		echo "⚠  ANTHROPIC_API_KEY not set — operator will use rule-based fallback agent."; \
		echo "   To use Claude: export ANTHROPIC_API_KEY=<key> && make dev-secrets"; \
	fi
	kubectl create secret generic ai-provider-secret \
		--from-literal=ANTHROPIC_API_KEY=$${ANTHROPIC_API_KEY:-not-configured} \
		--namespace $(DEV_NS) --dry-run=client -o yaml | kubectl apply -f -
	kubectl create secret generic grafana-api-secret \
		--from-literal=GRAFANA_TOKEN=$${GRAFANA_TOKEN:-not-configured} \
		--namespace $(DEV_NS) --dry-run=client -o yaml | kubectl apply -f -

.PHONY: dev-image
dev-image: ## Build the operator Docker image and load it into kind (no registry needed).
	docker build -t $(DEV_IMG) .
	kind load docker-image $(DEV_IMG) --name $(KIND_CLUSTER)
	@echo "✓ Image $(DEV_IMG) loaded into kind."

.PHONY: dev-deploy
dev-deploy: install dev-secrets ## Install CRD + RBAC + operator Deployment.
	kubectl create namespace $(DEV_NS) --dry-run=client -o yaml | kubectl apply -f -
	$(HELM) upgrade --install agentic-autoscaler deploy/helm/ \
		--namespace $(DEV_NS) \
		--set image.repository=agentic-autoscaler \
		--set image.tag=dev \
		--set image.pullPolicy=Never \
		--set aiProvider.provider=anthropic \
		--set aiProvider.secretRef=ai-provider-secret \
		--set grafana.url=http://kube-prometheus-stack-grafana.monitoring \
		--set grafana.secretRef=grafana-api-secret \
		--set dryRun=true \
		--set networkPolicy.enabled=false \
		--wait --timeout 2m
	kubectl set env deployment/agentic-autoscaler-agentic-autoscaler \
		OPERATOR_NAMESPACE=$(DEV_NS) -n $(DEV_NS) 2>/dev/null || true
	@echo "✓ Operator deployed."
	@echo "  Logs: kubectl -n $(DEV_NS) logs -l app.kubernetes.io/name=agentic-autoscaler -f"

.PHONY: sample-app
sample-app: ## Deploy payment-service (podinfo) + HPA into the production namespace.
	kubectl apply -f config/samples/dev-payment-service.yaml
	@echo "✓ payment-service deployed (3 replicas, HPA min=2 max=20)."

.PHONY: create-autoscaler
create-autoscaler: ## Create the AgenticAutoscaler CR for payment-service.
	kubectl apply -f config/samples/payment-service-autoscaler.yaml
	@echo "✓ AgenticAutoscaler created. First reconcile in ~30s."
	@echo "  Watch: kubectl -n production get agenticautoscaler payment-service-autoscaler -w"

.PHONY: loadgen-image
loadgen-image: ## Build the load generator image and load it into kind.
	docker build -f Dockerfile.loadgen -t $(LOADGEN_IMG) .
	kind load docker-image $(LOADGEN_IMG) --name $(KIND_CLUSTER)
	@echo "✓ Image $(LOADGEN_IMG) loaded into kind."

.PHONY: loadgen-deploy
loadgen-deploy: ## Deploy the load generator (DEMO ONLY) into the loadgen namespace.
	kubectl apply -f deploy/loadgen/loadgen.yaml
	kubectl -n loadgen rollout status deploy/loadgen --timeout=60s
	@echo "✓ loadgen ready at http://loadgen.loadgen.svc.cluster.local:8080"

.PHONY: demo-load
demo-load: loadgen-image loadgen-deploy ## Deploy loadgen and enable the console Load tab + write mode.
	$(HELM) upgrade agentic-autoscaler deploy/helm/ --namespace $(DEV_NS) --reuse-values \
		--set console.writeEnabled=true \
		--set console.loadgenURL=http://loadgen.loadgen.svc.cluster.local:8080
	kubectl -n $(DEV_NS) rollout restart deploy/agentic-autoscaler-agentic-autoscaler
	@echo "✓ Console write mode ON, Load tab enabled. Reopen http://localhost:8090 (Load tab)."

.PHONY: load-up
load-up: ## Start load. Override RPS=/DURATION=/ERRPCT=/DELAYMS=/LOAD_TARGET=.
	kubectl run loadctl-$$(date +%s) --rm -i --restart=Never -n loadgen --image=$(CURL_IMG) -- \
		curl -s -X POST http://loadgen.loadgen:8080/start -H 'Content-Type: application/json' \
		-d '{"targetURL":"$(LOAD_TARGET)","rps":$(RPS),"durationSec":$(DURATION),"errorPct":$(ERRPCT),"delayMs":$(DELAYMS)}'
	@echo ""
	@echo "✓ load started (rps=$(RPS), $(DURATION)s). Watch: kubectl -n production get deploy payment-service -w"

.PHONY: load-down
load-down: ## Stop the load generator; replicas scale back down after cooldown.
	kubectl run loadctl-$$(date +%s) --rm -i --restart=Never -n loadgen --image=$(CURL_IMG) -- \
		curl -s -X POST http://loadgen.loadgen:8080/stop
	@echo ""
	@echo "✓ load stopped."

# ── Timeout-storm demo (T4.1): log-pattern-driven scaling ────────────────────

.PHONY: faultgen-image
faultgen-image: ## Build the faultgen demo image and load it into kind.
	docker build -f Dockerfile.faultgen -t $(FAULTGEN_IMG) .
	kind load docker-image $(FAULTGEN_IMG) --name $(KIND_CLUSTER)
	@echo "✓ Image $(FAULTGEN_IMG) loaded into kind."

.PHONY: faultgen-deploy
faultgen-deploy: ## Deploy the faultgen workload + its AgenticAutoscaler (DEMO ONLY).
	kubectl apply -f deploy/faultgen/faultgen.yaml
	kubectl apply -f config/samples/dev-faultgen-autoscaler.yaml
	kubectl -n production rollout status deploy/faultgen --timeout=90s
	@echo "✓ faultgen ready at http://faultgen.production.svc.cluster.local:8080"

.PHONY: demo-timeout-storm
demo-timeout-storm: faultgen-image faultgen-deploy loadgen-image loadgen-deploy ## Set up the full log-pattern-driven demo.
	@echo ""
	@echo "✓ Timeout-storm demo ready. Then:"
	@echo "    make storm-up      # inject the storm: >2s latency + pool-exhausted logs"
	@echo "    kubectl -n production get agenticautoscaler faultgen-autoscaler -o yaml   # see the decision"
	@echo "    make storm-down    # clear the storm"
	@echo "  Scaling is dry-run by default; run 'make storm-live' to let it scale for real."

.PHONY: storm-up
storm-up: ## Inject the timeout storm (all replicas) and drive traffic at faultgen.
	# Set the fault via env + rollout so EVERY replica faults from boot — including
	# any the operator scales up (a per-pod API toggle would miss new pods).
	kubectl -n production set env deploy/faultgen FAULT_ENABLED=true
	kubectl -n production rollout status deploy/faultgen --timeout=90s
	kubectl run loadctl-$$(date +%s) --rm -i --restart=Never -n loadgen --image=$(CURL_IMG) -- \
		curl -s -X POST http://loadgen.loadgen:8080/start -H 'Content-Type: application/json' \
		-d '{"targetURL":"http://faultgen.production:8080","rps":$(RPS),"durationSec":$(DURATION),"errorPct":0,"delayMs":0}'
	@echo ""
	@echo "✓ storm injected. p99 climbs over ~2 min, then rule 2 fires. Watch:"
	@echo "    kubectl -n production get agenticautoscaler faultgen-autoscaler -o jsonpath='{.status.lastDecisionReason}'"

.PHONY: storm-down
storm-down: ## Clear the timeout storm and stop traffic.
	kubectl -n production set env deploy/faultgen FAULT_ENABLED=false
	kubectl run loadctl-$$(date +%s) --rm -i --restart=Never -n loadgen --image=$(CURL_IMG) -- \
		curl -s -X POST http://loadgen.loadgen:8080/stop
	@echo ""
	@echo "✓ storm cleared, load stopped."

.PHONY: storm-live
storm-live: ## Switch the faultgen autoscaler out of dry-run so it scales for real.
	kubectl -n production patch agenticautoscaler faultgen-autoscaler \
		--type merge -p '{"spec":{"dryRun":false}}'
	@echo "✓ faultgen-autoscaler is now LIVE (dryRun=false). Run 'make storm-up' to scale it."

.PHONY: load-status
load-status: ## Print the current load generator status.
	kubectl run loadctl-$$(date +%s) --rm -i --restart=Never -n loadgen --image=$(CURL_IMG) -- \
		curl -s http://loadgen.loadgen:8080/status
	@echo ""

.PHONY: dev-setup
dev-setup: kind-create observability-stack dev-image dev-deploy sample-app create-autoscaler ## Bootstrap the full local environment end-to-end.
	@echo ""
	@echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
	@echo "  Full stack is running. Run 'make port-forward' in a new    "
	@echo "  terminal, then open the URLs below.                        "
	@echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
	@make open

.PHONY: port-forward
port-forward: ## Forward Grafana :3000, Prometheus :9090, and Query UI :8090 to localhost.
	@echo "Starting port-forwards (Ctrl-C to stop all)..."
	@echo "  Grafana   → http://localhost:3000  (admin / admin)"
	@echo "  Prometheus→ http://localhost:9090"
	@echo "  Query UI  → http://localhost:8090"
	@trap 'kill %1 %2 %3 2>/dev/null' INT; \
	kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80 & \
	kubectl -n monitoring port-forward svc/kube-prometheus-stack-prometheus 9090:9090 & \
	kubectl -n $(DEV_NS) port-forward svc/agentic-autoscaler-agentic-autoscaler 8090:8090 & \
	wait

.PHONY: open
open: ## Print all service URLs.
	@echo ""
	@echo "  Service          URL                         Notes"
	@echo "  ───────────────  ──────────────────────────  ──────────────────────────────────"
	@echo "  Grafana          http://localhost:3000        admin / admin"
	@echo "  Prometheus       http://localhost:9090"
	@echo "  Query UI         http://localhost:8090        Ask questions about scale decisions"
	@echo ""
	@echo "  Run 'make port-forward' first to activate the local URLs."
	@echo ""

.PHONY: lint
lint: golangci-lint ## Run golangci-lint linter & yamllint
	$(GOLANGCI_LINT) run

.PHONY: lint-fix
lint-fix: golangci-lint ## Run golangci-lint linter and perform fixes
	$(GOLANGCI_LINT) run --fix

##@ Build

.PHONY: build
build: manifests generate fmt vet ## Build manager binary.
	go build -o bin/manager cmd/main.go

.PHONY: run
run: manifests generate fmt vet ## Run a controller from your host.
	go run ./cmd/main.go

# If you wish to build the manager image targeting other platforms you can use the --platform flag.
# (i.e. docker build --platform linux/arm64). However, you must enable docker buildKit for it.
# More info: https://docs.docker.com/develop/develop-images/build_enhancements/
.PHONY: docker-build
docker-build: ## Build docker image with the manager.
	$(CONTAINER_TOOL) build -t ${IMG} .

.PHONY: docker-push
docker-push: ## Push docker image with the manager.
	$(CONTAINER_TOOL) push ${IMG}

# PLATFORMS defines the target platforms for the manager image be built to provide support to multiple
# architectures. (i.e. make docker-buildx IMG=myregistry/mypoperator:0.0.1). To use this option you need to:
# - be able to use docker buildx. More info: https://docs.docker.com/build/buildx/
# - have enabled BuildKit. More info: https://docs.docker.com/develop/develop-images/build_enhancements/
# - be able to push the image to your registry (i.e. if you do not set a valid value via IMG=<myregistry/image:<tag>> then the export will fail)
# To adequately provide solutions that are compatible with multiple platforms, you should consider using this option.
PLATFORMS ?= linux/arm64,linux/amd64,linux/s390x,linux/ppc64le
.PHONY: docker-buildx
docker-buildx: ## Build and push docker image for the manager for cross-platform support
	# copy existing Dockerfile and insert --platform=${BUILDPLATFORM} into Dockerfile.cross, and preserve the original Dockerfile
	sed -e '1 s/\(^FROM\)/FROM --platform=\$$\{BUILDPLATFORM\}/; t' -e ' 1,// s//FROM --platform=\$$\{BUILDPLATFORM\}/' Dockerfile > Dockerfile.cross
	- $(CONTAINER_TOOL) buildx create --name project-v3-builder
	$(CONTAINER_TOOL) buildx use project-v3-builder
	- $(CONTAINER_TOOL) buildx build --push --platform=$(PLATFORMS) --tag ${IMG} -f Dockerfile.cross .
	- $(CONTAINER_TOOL) buildx rm project-v3-builder
	rm Dockerfile.cross

.PHONY: build-installer
build-installer: manifests generate kustomize ## Generate a consolidated YAML with CRDs and deployment.
	mkdir -p dist
	@if [ -d "config/crd" ]; then \
		$(KUSTOMIZE) build config/crd > dist/install.yaml; \
	fi
	echo "---" >> dist/install.yaml  # Add a document separator before appending
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default >> dist/install.yaml

##@ Deployment

ifndef ignore-not-found
  ignore-not-found = false
endif

.PHONY: install
install: manifests kustomize ## Install CRDs into the K8s cluster specified in ~/.kube/config.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) apply -f -

.PHONY: uninstall
uninstall: manifests kustomize ## Uninstall CRDs from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/crd | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

.PHONY: deploy
deploy: manifests kustomize ## Deploy controller to the K8s cluster specified in ~/.kube/config.
	cd config/manager && $(KUSTOMIZE) edit set image controller=${IMG}
	$(KUSTOMIZE) build config/default | $(KUBECTL) apply -f -

.PHONY: undeploy
undeploy: kustomize ## Undeploy controller from the K8s cluster specified in ~/.kube/config. Call with ignore-not-found=true to ignore resource not found errors during deletion.
	$(KUSTOMIZE) build config/default | $(KUBECTL) delete --ignore-not-found=$(ignore-not-found) -f -

##@ Dependencies

## Location to install dependencies to
LOCALBIN ?= $(shell pwd)/bin
$(LOCALBIN):
	mkdir -p $(LOCALBIN)

## Tool Binaries
KUBECTL ?= kubectl
KUSTOMIZE ?= $(LOCALBIN)/kustomize-$(KUSTOMIZE_VERSION)
CONTROLLER_GEN ?= $(LOCALBIN)/controller-gen-$(CONTROLLER_TOOLS_VERSION)
ENVTEST ?= $(LOCALBIN)/setup-envtest-$(ENVTEST_VERSION)
GOLANGCI_LINT = $(LOCALBIN)/golangci-lint-$(GOLANGCI_LINT_VERSION)

## Tool Versions
KUSTOMIZE_VERSION ?= v5.3.0
CONTROLLER_TOOLS_VERSION ?= v0.14.0
ENVTEST_VERSION ?= latest
GOLANGCI_LINT_VERSION ?= v1.54.2

.PHONY: kustomize
kustomize: $(KUSTOMIZE) ## Download kustomize locally if necessary.
$(KUSTOMIZE): $(LOCALBIN)
	$(call go-install-tool,$(KUSTOMIZE),sigs.k8s.io/kustomize/kustomize/v5,$(KUSTOMIZE_VERSION))

.PHONY: controller-gen
controller-gen: $(CONTROLLER_GEN) ## Download controller-gen locally if necessary.
$(CONTROLLER_GEN): $(LOCALBIN)
	$(call go-install-tool,$(CONTROLLER_GEN),sigs.k8s.io/controller-tools/cmd/controller-gen,$(CONTROLLER_TOOLS_VERSION))

.PHONY: envtest
envtest: $(ENVTEST) ## Download setup-envtest locally if necessary.
$(ENVTEST): $(LOCALBIN)
	$(call go-install-tool,$(ENVTEST),sigs.k8s.io/controller-runtime/tools/setup-envtest,$(ENVTEST_VERSION))

.PHONY: golangci-lint
golangci-lint: $(GOLANGCI_LINT) ## Download golangci-lint locally if necessary.
$(GOLANGCI_LINT): $(LOCALBIN)
	$(call go-install-tool,$(GOLANGCI_LINT),github.com/golangci/golangci-lint/cmd/golangci-lint,${GOLANGCI_LINT_VERSION})

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
mv "$$(echo "$(1)" | sed "s/-$(3)$$//")" $(1) ;\
}
endef
