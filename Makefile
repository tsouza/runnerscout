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

.PHONY: emulators
emulators:
	python3 tools/emulators.py

.PHONY: chart
chart: build
	helm lint charts/runnerscout --strict --kube-version 1.37.0 -f charts/runnerscout/tests/values.json
	python3 tools/verify_chart.py

.PHONY: image
image:
	docker build --tag runnerscout:development .

.PHONY: image-test
image-test:
	python3 tools/runtime_image.py

.PHONY: helm-integration
helm-integration:
	python3 tools/helm_integration.py

.PHONY: generate verify-generated crd-integration
generate:
	go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.22.0 object crd paths=./api/... output:crd:artifacts:config=config/crd/bases
verify-generated: generate
	git diff --exit-code -- api/v1alpha1/zz_generated.deepcopy.go config/crd/bases
	@test -z "$$(git ls-files --others --exclude-standard -- api/v1alpha1/zz_generated.deepcopy.go config/crd/bases)"
crd-integration:
	python3 tools/crd_integration.py
