PROJDIR=$(dir $(realpath $(firstword $(MAKEFILE_LIST))))

# change to project dir so we can express all as relative paths
$(shell cd $(PROJDIR))

REPO_PATH="github.com/paccolamano/svpn"

VERSION ?= $(shell scripts/git-version.sh)

LD_FLAGS="-w -X $(REPO_PATH)/cmd.Version=$(VERSION)"

$(shell mkdir -p bin )

# golangci-lint is a tool dependency in go.mod, so `go tool golangci-lint`
# below builds the pinned version out of the module cache. The version lives in
# exactly one place and CI runs the same one without being told which.
#
# goreleaser and actionlint are not tool dependencies: their module graphs are
# enormous — cloud SDKs for publishers this project does not use — and they are
# needed a handful of times rather than on every build, so they are fetched on
# demand instead of being carried in go.sum. They are still pinned here and
# nowhere else: the workflows call these targets rather than naming a version of
# their own, so a release cannot be built by a goreleaser that never ran here.
GORELEASER_VERSION ?= v2.18.1
ACTIONLINT_VERSION ?= v1.7.12

# GOTOOLCHAIN=auto because these are tools, not dependencies, and a tool may ask
# for a newer Go than the project targets. It is a fallback, not the main
# mechanism: go.mod names a minor version with no patch, so setup-go installs
# the runner's latest 1.27.x and a tool wanting a newer *patch* is already
# satisfied. This covers the case where one wants a newer *minor*, which would
# otherwise be a hard failure under the GOTOOLCHAIN=local that setup-go pins.
#
# Fetching a toolchain to run a tool does not change what svpn is built with.
GORELEASER = GOTOOLCHAIN=auto go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
ACTIONLINT = GOTOOLCHAIN=auto go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

.PHONY: all
all: build

.PHONY: build
build: svpn svpnd

# don't use existing file names and track go sources, let's do this to the go tool
.PHONY: svpn
svpn:
	GO111MODULE=on go build -ldflags $(LD_FLAGS) -o $(PROJDIR)/bin/svpn $(REPO_PATH)/cmd/svpn

.PHONY: svpnd
svpnd:
	GO111MODULE=on go build -ldflags $(LD_FLAGS) -o $(PROJDIR)/bin/svpnd $(REPO_PATH)/cmd/svpnd

.PHONY: test
test:
	GO111MODULE=on go test -race ./...

.PHONY: test-cover
test-cover:
	GO111MODULE=on go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: fmt
fmt:
	go tool golangci-lint fmt

# fmt --diff exits non-zero on unformatted code, which is what the old
# `gofmt -l . | tee /dev/stderr | (! read)` was for. go vet is inside
# golangci-lint's default set, so it is not run separately either.
.PHONY: lint
lint:
	go tool golangci-lint fmt --diff
	go tool golangci-lint run

# The protocol half is portable; the daemon's interface and routing work is
# not. This checks the former still builds everywhere it claims to.
#
# It is the only thing that looks at the //go:build !linux halves of netcfg,
# ipc and install: golangci-lint only ever analyses the host's GOOS, and go vet
# cannot run here because it compiles the tests too, and those are Linux-only.
.PHONY: crosscheck
crosscheck:
	GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
	GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
	GOOS=darwin  GOARCH=arm64 go build -o /dev/null ./...

# Everything CI runs, in one command.
.PHONY: check
check: lint test crosscheck check-config

# The files no Go test can reach: the workflows, the release config and the
# installer script. Without this they are exercised for the first time by
# pushing a tag, which is the worst possible moment to find out one of them is
# wrong — and the release path has never run.
#
# actionlint shellchecks the workflows' own run: blocks when shellcheck is
# present, which it is on the runners.
.PHONY: check-config
check-config:
	$(ACTIONLINT) .github/workflows/*.yml
	$(GORELEASER) check
	@if command -v shellcheck >/dev/null 2>&1; then \
		shellcheck install.sh; \
	else \
		echo "check-config: shellcheck is not installed here, skipping install.sh (CI runs it)"; \
	fi

# Installs from this working tree rather than from a release, which is what a
# developer wants after changing the daemon. svpnd install does the rest.
.PHONY: install
install: build
	sudo $(PROJDIR)/bin/svpnd install

# Builds the release archives without tagging or publishing anything.
.PHONY: snapshot
snapshot:
	$(GORELEASER) release --snapshot --clean

# What the release workflow runs on a tag. It lives here so that the version is
# pinned once and a local snapshot is built by the same goreleaser that will
# publish.
.PHONY: release
release:
	$(GORELEASER) release --clean

.PHONY: clean
clean:
	rm -rf $(PROJDIR)/bin $(PROJDIR)/dist $(PROJDIR)/coverage.out
