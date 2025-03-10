export GO15VENDOREXPERIMENT := 1

ifeq (${TIMESTAMP}, 1)
  $(info "Timestamp is enabled")
  SHELL = ./hack/timestamps.sh
endif

all:
	hack/dockerized "export BUILD_ARCH=${BUILD_ARCH} && DOCKER_PREFIX=${DOCKER_PREFIX} DOCKER_TAG=${DOCKER_TAG} IMAGE_PULL_POLICY=${IMAGE_PULL_POLICY} VERBOSITY=${VERBOSITY} ./hack/build-manifests.sh && \
	    hack/bazel-fmt.sh && hack/bazel-build.sh"

go-all:
	hack/dockerized "KUBEVIRT_VERSION=${KUBEVIRT_VERSION} ./hack/build-go.sh install ${WHAT} && ./hack/build-copy-artifacts.sh ${WHAT} && DOCKER_PREFIX=${DOCKER_PREFIX} DOCKER_TAG=${DOCKER_TAG} IMAGE_PULL_POLICY=${IMAGE_PULL_POLICY} VERBOSITY=${VERBOSITY} GO_BUILD=true ./hack/build-manifests.sh"

bazel-generate:
	SYNC_VENDOR=true hack/dockerized "./hack/bazel-generate.sh"

bazel-build:
	hack/dockerized "export BUILD_ARCH=${BUILD_ARCH} && hack/bazel-fmt.sh && hack/bazel-build.sh"

bazel-build-image-bundle:
	hack/dockerized "export BUILD_ARCH=${BUILD_ARCH} && hack/bazel-fmt.sh && DOCKER_PREFIX=${DOCKER_PREFIX} DOCKER_TAG=${DOCKER_TAG} IMAGE_PREFIX=${IMAGE_PREFIX} hack/bazel-build-image-bundle.sh"

bazel-build-verify: bazel-build
	./hack/dockerized "hack/bazel-fmt.sh"
	./hack/verify-generate.sh
	./hack/build-verify.sh
	./hack/dockerized "hack/bazel-test.sh"

bazel-build-images:
	hack/dockerized "export BUILD_ARCH=${BUILD_ARCH} && DOCKER_PREFIX=${DOCKER_PREFIX} DOCKER_TAG=${DOCKER_TAG} DOCKER_TAG_ALT=${DOCKER_TAG_ALT} IMAGE_PREFIX=${IMAGE_PREFIX} IMAGE_PREFIX_ALT=${IMAGE_PREFIX_ALT} ./hack/bazel-build-images.sh"

bazel-push-images:
	hack/dockerized "export BUILD_ARCH=${BUILD_ARCH} && hack/bazel-fmt.sh && DOCKER_PREFIX=${DOCKER_PREFIX} DOCKER_TAG=${DOCKER_TAG} DOCKER_TAG_ALT=${DOCKER_TAG_ALT} IMAGE_PREFIX=${IMAGE_PREFIX} IMAGE_PREFIX_ALT=${IMAGE_PREFIX_ALT} KUBEVIRT_PROVIDER=${KUBEVIRT_PROVIDER} PUSH_TARGETS='${PUSH_TARGETS}' ./hack/bazel-push-images.sh"

push: bazel-push-images

bazel-test:
	hack/dockerized "hack/bazel-fmt.sh && CI=${CI} ARTIFACTS=${ARTIFACTS} hack/bazel-test.sh"

gen-proto:
	hack/dockerized "DOCKER_PREFIX=${DOCKER_PREFIX} DOCKER_TAG=${DOCKER_TAG} IMAGE_PULL_POLICY=${IMAGE_PULL_POLICY} VERBOSITY=${VERBOSITY} ./hack/gen-proto.sh"

generate:
	hack/dockerized "DOCKER_PREFIX=${DOCKER_PREFIX} DOCKER_TAG=${DOCKER_TAG} IMAGE_PULL_POLICY=${IMAGE_PULL_POLICY} VERBOSITY=${VERBOSITY} ./hack/generate.sh"
	SYNC_VENDOR=true hack/dockerized "./hack/bazel-generate.sh && hack/bazel-fmt.sh"
	hack/dockerized hack/sync-kubevirtci.sh

generate-verify: generate
	./hack/verify-generate.sh
	./hack/check-for-binaries.sh

apidocs:
	hack/dockerized "./hack/gen-swagger-doc/gen-swagger-docs.sh v1 html"

client-python:
	hack/dockerized "DOCKER_TAG=${DOCKER_TAG} ./hack/gen-client-python/generate.sh"

go-build:
	hack/dockerized "KUBEVIRT_VERSION=${KUBEVIRT_VERSION} ./hack/build-go.sh install ${WHAT}" && ./hack/build-copy-artifacts.sh ${WHAT}

gosec:
	hack/dockerized "GOSEC=${GOSEC} ARTIFACTS=${ARTIFACTS} ./hack/gosec.sh"

coverage:
	hack/dockerized "./hack/coverage.sh ${WHAT}"

goveralls:
	SYNC_OUT=false hack/dockerized "COVERALLS_TOKEN_FILE=${COVERALLS_TOKEN_FILE} COVERALLS_TOKEN=${COVERALLS_TOKEN} CI_NAME=prow CI_BRANCH=${PULL_BASE_REF} CI_PR_NUMBER=${PULL_NUMBER} GIT_ID=${PULL_PULL_SHA} PROW_JOB_ID=${PROW_JOB_ID} ./hack/bazel-goveralls.sh"

go-test: go-build
	SYNC_OUT=false hack/dockerized "./hack/build-go.sh test ${WHAT}"

test: bazel-test

build-functests:
	hack/dockerized "hack/bazel-fmt.sh && hack/build-func-tests.sh"

functest: build-functests
	hack/functests.sh

dump: bazel-build
	hack/dump.sh

functest-image-build: manifests build-functests
	hack/func-tests-image.sh build

functest-image-push: functest-image-build
	hack/func-tests-image.sh push

conformance:
	hack/dockerized "export SKIP_OUTSIDE_CONN_TESTS=${SKIP_OUTSIDE_CONN_TESTS} && hack/conformance.sh"

perftest: build-functests
	hack/perftests.sh

clean:
	hack/dockerized "./hack/build-go.sh clean ${WHAT} && rm _out/* -rf"
	hack/dockerized "bazel clean --expunge"
	rm -f tools/openapispec/openapispec tools/resource-generator/resource-generator tools/manifest-templator/manifest-templator tools/vms-generator/vms-generator tools/marketplace/marketplace

distclean: clean
	hack/dockerized "rm -rf vendor/ && rm -f go.sum && GO111MODULE=on go clean -modcache"
	rm -rf vendor/

cluster-patch:
	hack/dockerized "export BUILD_ARCH=${BUILD_ARCH} && hack/bazel-fmt.sh && DOCKER_PREFIX=${DOCKER_PREFIX} DOCKER_TAG=${DOCKER_TAG} DOCKER_TAG_ALT=${DOCKER_TAG_ALT} IMAGE_PREFIX=${IMAGE_PREFIX} IMAGE_PREFIX_ALT=${IMAGE_PREFIX_ALT} KUBEVIRT_PROVIDER=${KUBEVIRT_PROVIDER} PUSH_TARGETS='virt-api virt-controller virt-handler virt-launcher' ./hack/bazel-push-images.sh"
	hack/cluster-patch.sh

deps-update-patch:
	SYNC_VENDOR=true hack/dockerized " ./hack/dep-update.sh -- -u=patch && ./hack/dep-prune.sh && ./hack/bazel-generate.sh"

deps-update:
	SYNC_VENDOR=true hack/dockerized " ./hack/dep-update.sh && ./hack/dep-prune.sh && ./hack/bazel-generate.sh"

deps-sync:
	SYNC_VENDOR=true hack/dockerized " ./hack/dep-update.sh --sync-only && ./hack/dep-prune.sh && ./hack/bazel-generate.sh"

rpm-deps:
	SYNC_VENDOR=true hack/dockerized " ./hack/rpm-deps.sh"

verify-rpm-deps:
	SYNC_VENDOR=true hack/dockerized " ./hack/verify-rpm-deps.sh"

build-verify:
	hack/build-verify.sh

manifests:
	hack/dockerized "CSV_VERSION=${CSV_VERSION} QUAY_REPOSITORY=${QUAY_REPOSITORY} \
	  DOCKER_PREFIX=${DOCKER_PREFIX} DOCKER_TAG=${DOCKER_TAG} KUBEVIRT_PROVIDER=${KUBEVIRT_PROVIDER} KUBEVIRT_ONLY_USE_TAGS=${KUBEVIRT_ONLY_USE_TAGS} \
	  IMAGE_PULL_POLICY=${IMAGE_PULL_POLICY} VERBOSITY=${VERBOSITY} PACKAGE_NAME=${PACKAGE_NAME} \
	  KUBEVIRT_INSTALLED_NAMESPACE=${KUBEVIRT_INSTALLED_NAMESPACE} ./hack/build-manifests.sh"

cluster-up:
	./cluster-up/up.sh

cluster-down:
	./cluster-up/down.sh

cluster-build:
	./hack/cluster-build.sh

cluster-clean:
	./hack/cluster-clean.sh

cluster-deploy: cluster-clean
	./hack/cluster-deploy.sh

cluster-sync:
	$(MAKE) cluster-build
	$(MAKE) cluster-deploy

builder-build:
	./hack/builder/build.sh

builder-publish:
	./hack/builder/publish.sh

olm-verify:
	hack/dockerized "./hack/olm.sh verify"

current-dir := $(realpath .)

build-prom-spec-dumper:
	hack/dockerized "go build -o rule-spec-dumper ./hack/prom-rule-ci/rule-spec-dumper.go"

prom-rules-verify: build-prom-spec-dumper
	./hack/prom-rule-ci/verify-rules.sh \
		"${current-dir}/rule-spec-dumper" \
		"${current-dir}/hack/prom-rule-ci/prom-rules-tests.yaml"

olm-push:
	hack/dockerized "DOCKER_TAG=${DOCKER_TAG} CSV_VERSION=${CSV_VERSION} QUAY_USERNAME=${QUAY_USERNAME} \
	    QUAY_PASSWORD=${QUAY_PASSWORD} QUAY_REPOSITORY=${QUAY_REPOSITORY} PACKAGE_NAME=${PACKAGE_NAME} ./hack/olm.sh push"

bump-kubevirtci:
	./hack/bump-kubevirtci.sh

fossa:
	hack/dockerized "FOSSA_TOKEN_FILE=${FOSSA_TOKEN_FILE} ./hack/fossa.sh"

.PHONY: \
	build-verify \
	conformance \
	go-build \
	go-test \
	go-all \
	bazel-generate \
	bazel-build \
	bazel-build-image-bundle \
	bazel-build-images \
	bazel-push-images \
	bazel-test \
	functest-image-build \
	functest-image-push \
	test \
	clean \
	distclean \
	deps-sync \
	sync \
	manifests \
	functest \
	cluster-up \
	cluster-down \
	cluster-clean \
	cluster-deploy \
	cluster-sync \
	olm-verify \
	olm-push \
	coverage \
	goveralls \
	build-functests \
	fossa
