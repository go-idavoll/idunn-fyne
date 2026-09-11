# Copyright 2026 The idunn Authors
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

GO ?= go
LICENSE_YEAR ?= 2026
LICENSE_HOLDER ?= The idunn Authors

# The library packages carry no Fyne driver, so they build and test with no
# OpenGL and no display. Only ./cmd/... links the desktop driver.
LIB_PKGS ?= ./fyneui/... ./internal/...

.PHONY: all build build-lib test test-lib cover vet fmt fmt-check lint license \
        license-fix vuln tidy deps-linux clean

all: build

build:
	$(GO) build ./...

## build-lib builds only what needs no native toolchain.
build-lib:
	$(GO) build $(LIB_PKGS)

test:
	$(GO) test -race ./...

## test-lib runs the whole meaningful test suite without OpenGL or a display.
test-lib:
	$(GO) test -race $(LIB_PKGS)

# Scoped the same way idunn scopes its own coverage job: ./... would count the
# cmd/ wiring, which is build plumbing and carries no tests by design.
cover:
	$(GO) test -covermode=atomic -coverpkg=./fyneui/...,./internal/... \
		-coverprofile=coverage.out $(LIB_PKGS)
	$(GO) tool cover -func=coverage.out | tail -1

vet:
	$(GO) vet ./...

fmt:
	gofmt -l -w .

fmt-check:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint:
	golangci-lint run

license:
	addlicense -check -ignore '.idea/**' -ignore '.gotmp/**' \
		-l apache -c "$(LICENSE_HOLDER)" -y $(LICENSE_YEAR) .

license-fix:
	addlicense -ignore '.idea/**' -ignore '.gotmp/**' \
		-l apache -c "$(LICENSE_HOLDER)" -y $(LICENSE_YEAR) .

vuln:
	govulncheck ./...

tidy:
	$(GO) mod tidy

## deps-linux installs what cgo needs to link the Fyne desktop driver. macOS and
## Windows need nothing beyond their own toolchain.
deps-linux:
	sudo apt-get update
	sudo apt-get install -y --no-install-recommends \
		libgl1-mesa-dev libxcursor-dev libxrandr-dev libxinerama-dev \
		libxi-dev libxxf86vm-dev libwayland-dev libxkbcommon-dev wayland-protocols

clean:
	rm -rf bin dist coverage.out
