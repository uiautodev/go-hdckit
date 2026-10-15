GO ?= go
GOFMT ?= gofmt

.PHONY: all build test race vet fmt fmt-check e2e clean

all: build

build:
	$(GO) build ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...
	$(GO) vet -tags e2e ./...

fmt:
	$(GOFMT) -w .

fmt-check:
	@test -z "$$($(GOFMT) -l .)" || { echo "gofmt needed:"; $(GOFMT) -l .; exit 1; }

# End-to-end tests against a real hdc server/device.
# Set HDCKIT_E2E_UI=1 and/or HDCKIT_E2E_HILOG=1 to also exercise UiDriver and hilog.
e2e:
	$(GO) test -tags e2e -v ./hdc/

clean:
	$(GO) clean ./...
