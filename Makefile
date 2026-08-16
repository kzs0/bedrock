GO ?= go
GOLANGCI_LINT ?= go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
GOVULNCHECK ?= go run golang.org/x/vuln/cmd/govulncheck@v1.7.0
COVERAGE_MIN ?= 80.0

.PHONY: ci format-check vet lint test race coverage bench-tidy bench-test vuln

ci: format-check vet lint test race coverage bench-test vuln

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
	./scripts/coverage.sh "$(COVERAGE_MIN)" coverage.out

bench-tidy:
	GO="$(GO)" ./scripts/check-bench-tidy.sh

bench-test: bench-tidy
	cd bench && $(GO) test -count=1 ./... && $(GO) vet ./...

vuln:
	GO="$(GO)" ./scripts/require-go-version.sh 1.25
	GOTOOLCHAIN=local $(GOVULNCHECK) ./...
	cd bench && GOTOOLCHAIN=local $(GOVULNCHECK) ./...
