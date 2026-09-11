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
#
# The GUI is not here and cannot be: Fyne needs cgo and an OpenGL toolchain for
# the target, which `go build` alone cannot cross-compile. It is also not
# needed — a directory with its own go.mod is pruned from this module's ./...
# patterns, so gui/ is invisible to every target above whether or not it builds.
.PHONY: crosscheck
crosscheck:
	GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
	GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
	GOOS=darwin  GOARCH=arm64 go build -o /dev/null ./...

# Everything CI runs, in one command.
#
# gui-check is in here rather than left to the developer to remember precisely
# because gui/ is a separate module: nothing else in this file can see it, so
# without naming it explicitly the desktop client would be the one part of the
# repository that is never checked. The cost is that `make check` now needs cgo
# and the OpenGL and X11 headers Fyne links against.
.PHONY: check
check: lint test crosscheck check-config gui-check

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

# The desktop client. It lives in gui/ as a module of its own because Fyne
# needs cgo, and the two binaries above are built with CGO_ENABLED=0 and
# cross-compiled to three targets: one go.mod holding both would give up the
# second to get the first. The split also means the GUI cannot import anything
# under internal/, which is the rule that keeps it a client of the daemon
# rather than a second copy of the protocol.
GUI_PATH = $(PROJDIR)/gui

.PHONY: gui
gui:
	cd $(GUI_PATH) && GO111MODULE=on go build -ldflags "-w" -o $(PROJDIR)/bin/svpn-gui .

.PHONY: gui-test
gui-test:
	cd $(GUI_PATH) && GO111MODULE=on go test -race ./...

# golangci-lint is a tool dependency of *this* module, and `go tool` resolves
# tools from the module in the working directory — gui/go.mod declares none, and
# giving it one would put the linter's whole module graph in a second go.sum.
# Building the pinned binary once and running it with gui/ as its cwd keeps the
# version in the one place it already lives.
$(PROJDIR)/bin/golangci-lint: go.mod go.sum
	GO111MODULE=on go build -o $@ github.com/golangci/golangci-lint/v2/cmd/golangci-lint

.PHONY: gui-lint
gui-lint: $(PROJDIR)/bin/golangci-lint
	cd $(GUI_PATH) && $(PROJDIR)/bin/golangci-lint fmt --diff --config $(PROJDIR)/.golangci.yml
	cd $(GUI_PATH) && $(PROJDIR)/bin/golangci-lint run --config $(PROJDIR)/.golangci.yml

.PHONY: gui-check
gui-check: gui-lint gui-test

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
