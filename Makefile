.PHONY: build test verify
build:
	go build -trimpath -o bin/runnerscout ./cmd/runnerscout
test:
	go test -race -count=1 ./...
verify:
	python3 tools/evaluate.py

.PHONY: integration
integration:
	go test -tags integration -run TestRealKubernetesPersistenceAndCAS -count=1 -v ./internal/state

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
