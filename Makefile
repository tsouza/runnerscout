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
	helm lint charts/runnerscout --strict --kube-version 1.35.0 -f charts/runnerscout/tests/values.json
	python3 tools/test_chart.py
