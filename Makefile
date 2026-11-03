GO ?= go
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
GOVULNCHECK ?= go run golang.org/x/vuln/cmd/govulncheck@v1.7.0
COVERAGE_MIN ?= 90.0
E2E_COVERAGE_MIN ?= 90.0
E2E_TEST_FLAGS ?=

.PHONY: ci format-check vet lint test race coverage bench-tidy bench-test compat-test compat-tidy vuln e2e e2e-coverage e2e-race fuzz-smoke

ci: format-check vet lint test race coverage bench-test compat-test e2e-coverage vuln

format-check:
	./scripts/check-gofmt.sh

vet:
	$(GO) vet ./...

lint:
	$(GOLANGCI_LINT) run --timeout=5m

test:
	$(GO) test -count=1 ./...

race:
	$(GO) test -race -count=1 ./...

coverage:
	GO="$(GO)" ./scripts/coverage.sh "$(COVERAGE_MIN)" coverage.out

bench-tidy:
	GO="$(GO)" ./scripts/check-bench-tidy.sh

bench-test: bench-tidy
	cd bench && $(GO) test -count=1 ./... && $(GO) vet ./...

vuln:
	GO="$(GO)" ./scripts/require-go-version.sh 1.25
	GOTOOLCHAIN=local $(GOVULNCHECK) ./...
	cd bench && GOTOOLCHAIN=local $(GOVULNCHECK) ./...
	cd compat/otlp && GOTOOLCHAIN=local $(GOVULNCHECK) ./...

compat-tidy:
	cd compat/otlp && $(GO) mod tidy -diff

compat-test: compat-tidy
	cd compat/otlp && $(GO) test -count=1 ./... && $(GO) vet ./...

e2e:
	cd compat/otlp && $(GO) test -count=1 -timeout=5m ./e2e

e2e-race:
	cd compat/otlp && E2E_RACE=1 $(GO) test -race -count=1 -timeout=10m ./e2e

e2e-coverage:
	rm -f coverage-e2e.out coverage-e2e-inventory.md
	rm -rf .coverage-e2e
	mkdir -p .coverage-e2e
	cd compat/otlp && E2E_COVER_DIR="$(CURDIR)/.coverage-e2e" $(GO) test $(E2E_TEST_FLAGS) -count=1 -timeout=10m -v ./e2e
	$(GO) tool covdata textfmt -pkg="$$( $(GO) list ./... | awk '$$0 !~ /\/example(\/|$$)/' | paste -sd, -)" -i="$$(find .coverage-e2e -type f -name 'covmeta.*' -exec dirname {} \; | sort -u | paste -sd, -)" -o=coverage-e2e.out
	GO="$(GO)" python3 scripts/coverage-inventory.py coverage-e2e.out > coverage-e2e-inventory.md
	GO="$(GO)" ./scripts/enforce-coverage.sh coverage-e2e.out "$(E2E_COVERAGE_MIN)"

fuzz-smoke:
	$(GO) test -run='^$$' -fuzz='^FuzzParseUncompressed$$' -fuzztime=10s -parallel=2 -timeout=1m ./internal/pprof
	$(GO) test -run='^$$' -fuzz='^FuzzPolicyNestedValues$$' -fuzztime=10s -parallel=2 -timeout=1m ./privacy
