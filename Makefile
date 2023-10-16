VERSION          ?= $(shell git describe --abbrev=0 --tags --match "v*")
ifndef VERSION
VERSION          = $(GIT_HEAD_COMMIT)
endif

# Defaults
REGISTRY        ?= ghcr.io
REPOSITORY      ?= oliverbaehler/cloudflare-tunnel-ingress-controller
GIT_HEAD_COMMIT ?= $(shell git rev-parse --short HEAD)
GIT_TAG_COMMIT  ?= $(shell git rev-parse --short $(VERSION))
GIT_MODIFIED_1  ?= $(shell git diff $(GIT_HEAD_COMMIT) $(GIT_TAG_COMMIT) --quiet && echo "" || echo ".dev")
GIT_MODIFIED_2  ?= $(shell git diff --quiet && echo "" || echo ".dirty")
GIT_MODIFIED    ?= $(shell echo "$(GIT_MODIFIED_1)$(GIT_MODIFIED_2)")
GIT_REPO        ?= $(shell git config --get remote.origin.url)
BUILD_DATE      ?= $(shell git log -1 --format="%at" | xargs -I{} sh -c 'if [ "$(shell uname)" = "Darwin" ]; then date -r {} +%Y-%m-%dT%H:%M:%S; else date -d @{} +%Y-%m-%dT%H:%M:%S; fi')
IMG_BASE        ?= $(REPOSITORY)/cloudflare-tunnel-ingress-controller
IMG             ?= $(REGISTRY)/$(IMG_BASE)


.PHONY: dev
dev:
	skaffold dev --namespace cloudflare-tunnel-ingress-controller-dev

.PHONY: unit-test
unit-test:
	CGO_ENABLED=1 go test -race ./pkg/... -coverprofile ./cover.out

.PHONY: integration-test
integration-test: setup-envtest
	KUBEBUILDER_ASSETS="$(shell setup-envtest use $(ENVTEST_K8S_VERSION) -p path)" CGO_ENABLED=1 go test -race -v -coverpkg=./... -coverprofile ./test/integration/cover.out ./test/integration/...

.PHONY: setup-envtest
setup-envtest:
	bash ./hack/install-setup-envtest.sh

# Docker Image
KOCACHE             ?= /tmp/ko-cache
KO_REGISTRY         := ko.local
KO_TAGS             ?= "latest,$(VERSION)"

.PHONY: ko-build
LOCAL_CAPSULE_IMG := $(KO_REGISTRY)/github.com/$(IMG)
ko-build: ko
	@LD_FLAGS=$(LD_FLAGS) KOCACHE=$(KOCACHE) KO_DOCKER_REPO=$(KO_REGISTRY) \
		$(KO) build ./cmd/cloudflare-tunnel-ingress-controller/ --preserve-import-paths --tags=$(KO_TAGS) --push=false

.PHONY: docker-build-all
docker-build: ko-build

REGISTRY_PASSWORD   ?= dummy
REGISTRY_USERNAME   ?= dummy

.PHONY: ko-login
ko-login: ko
	@$(KO) login $(REGISTRY) --username $(REGISTRY_USERNAME) --password $(REGISTRY_PASSWORD)

.PHONY: ko-publish
ko-publish: ko-login ## Build and publish kyvernopre image (with ko)
	@LD_FLAGS=$(LD_FLAGS) KOCACHE=$(KOCACHE) KO_DOCKER_REPO=$(IMG) \
		$(KO) build ./cmd/cloudflare-tunnel-ingress-controller/ --bare --tags=$(KO_TAGS)

.PHONY: docker-publish
docker-publish: ko-publish

# Helm Chart
SRC_ROOT = $(shell git rev-parse --show-toplevel)

helm-docs: HELMDOCS_VERSION := v1.11.0
helm-docs:
	@docker run -v "$(SRC_ROOT):/helm-docs" jnorwood/helm-docs:$(HELMDOCS_VERSION) --chart-search-root /helm-docs

helm-lint: CT_VERSION := v3.3.1
helm-lint:
	@docker run -v "$(SRC_ROOT):/workdir" --entrypoint /bin/sh quay.io/helmpack/chart-testing:$(CT_VERSION) -c "cd /workdir; ct lint --config ct.yaml --lint-conf lintconf.yaml --all --debug"


KO = $(shell pwd)/bin/ko
KO_VERSION = v0.14.1
ko:
	$(call go-install-tool,$(KO),github.com/google/ko@v0.14.1)

# go-install-tool will 'go install' any package $2 and install it to $1.
PROJECT_DIR := $(shell dirname $(abspath $(lastword $(MAKEFILE_LIST))))
define go-install-tool
@[ -f $(1) ] || { \
set -e ;\
echo "Installing $(2)" ;\
GOBIN=$(PROJECT_DIR)/bin go install $(2) ;\
}
endef