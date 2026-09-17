.PHONY: build test verify
build:
	go build -trimpath -o bin/runnerscout ./cmd/runnerscout
test:
	go test -race -count=1 ./...
verify:
	python3 tools/evaluate.py

.PHONY: integration
integration:
	python3 tools/integration_tests.py --package github.com/tsouza/runnerscout/internal/state --test TestRealKubernetesPersistenceAndCAS
	python3 tools/integration_tests.py --package github.com/tsouza/runnerscout/internal/configapi --test TestRealKubernetesCRDSchemasAndConfigurationSnapshot

.PHONY: qualify
qualify:
	python3 tools/qualify.py

.PHONY: tlc
tlc:
	python3 tools/tlc.py

.PHONY: emulators
emulators:
	python3 tools/emulators.py

.PHONY: chart
chart: build
	helm lint charts/runnerscout --strict --kube-version 1.37.0 -f charts/runnerscout/tests/values.json
	python3 tools/verify_chart.py

.PHONY: image
image:
	rm -rf linux dist
	mkdir -p linux/$$(go env GOARCH)
	goreleaser build --single-target --snapshot --clean --skip=before -o linux/$$(go env GOARCH)/runnerscout
	docker build --tag runnerscout:development .
	rm -rf linux dist

.PHONY: image-test
image-test:
	python3 tools/runtime_image.py

.PHONY: helm-integration
helm-integration:
	python3 tools/helm_integration.py

.PHONY: generate verify-generated crd-integration
generate:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 object crd paths=./api/... output:crd:artifacts:config=config/crd/bases
	mkdir -p charts/runnerscout/crds
	cp config/crd/bases/*.yaml charts/runnerscout/crds/
verify-generated: generate
	git diff --exit-code -- api/v1alpha1/zz_generated.deepcopy.go config/crd/bases charts/runnerscout/crds
	@test -z "$$(git ls-files --others --exclude-standard -- api/v1alpha1/zz_generated.deepcopy.go config/crd/bases charts/runnerscout/crds)"
crd-integration:
	python3 tools/crd_integration.py
