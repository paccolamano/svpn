PROJDIR=$(dir $(realpath $(firstword $(MAKEFILE_LIST))))

# change to project dir so we can express all as relative paths
$(shell cd $(PROJDIR))

REPO_PATH="github.com/paccolamano/svpn"

VERSION ?= $(shell scripts/git-version.sh)

LD_FLAGS="-w -X $(REPO_PATH)/internal/build.Version=$(VERSION)"

$(shell mkdir -p bin )

GORELEASER_VERSION ?= v2.18.1
ACTIONLINT_VERSION ?= v1.7.12

GORELEASER = GOTOOLCHAIN=auto go run github.com/goreleaser/goreleaser/v2@$(GORELEASER_VERSION)
ACTIONLINT = GOTOOLCHAIN=auto go run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

.PHONY: all
all: build

.PHONY: build
build: svpn svpnd svpn-gui

# don't use existing file names and track go sources, let's do this to the go tool
.PHONY: svpn
svpn:
	GO111MODULE=on go build -ldflags $(LD_FLAGS) -o $(PROJDIR)/bin/svpn $(REPO_PATH)/cmd/svpn

.PHONY: svpnd
svpnd:
	GO111MODULE=on go build -ldflags $(LD_FLAGS) -o $(PROJDIR)/bin/svpnd $(REPO_PATH)/cmd/svpnd

# The desktop client. Same ldflags as the other two, which is what lets it
# report its own build — as a separate module it was linked with -w alone and
# had no version to show. It is the only binary here that needs cgo, so it is
# also the only one that cannot be cross-compiled; see crosscheck below.
.PHONY: svpn-gui
svpn-gui:
	GO111MODULE=on go build -ldflags $(LD_FLAGS) -o $(PROJDIR)/bin/svpn-gui $(REPO_PATH)/cmd/svpn-gui

# The system libraries Fyne links against, as Debian and Ubuntu name them.
#
# Normally only CI runs this: a desktop machine already has them, dragged in by
# whatever it runs its own session with — which is exactly how this list came
# to be wrong once. The GUI built here and the runner failed on a header no
# developer was missing, so the list lives in one place and the jobs call it
# rather than each carrying a copy to go stale.
#
# GLFW compiles *both* its X11 and its Wayland backend unless a build tag picks
# one (see go-gl's c_glfw_lin_*.go), so both sets of headers are required. The
# wayland-protocols ones are not: go-gl vendors them already generated.
GUI_DEPS = libgl1-mesa-dev xorg-dev libwayland-dev libxkbcommon-dev

.PHONY: gui-deps
gui-deps:
	sudo apt-get update && sudo apt-get install -y $(GUI_DEPS)

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
# Two packages are left out, and they are the only ones in the module that
# need cgo: the Fyne widgets and the main that starts them. Fyne wants an
# OpenGL toolchain for the *target*, which `go build` alone cannot
# cross-compile. Everything else is checked — including internal/client/state,
# the controller the GUI runs on, which is why that is a package of its own
# rather than part of the Fyne half.
#
# The list is built by the host's GOOS: a variable assignment in front of a
# command does not reach a command substitution in its arguments, so all three
# legs below see the same packages.
PORTABLE = $$(go list ./... | grep -v -e '/cmd/svpn-gui$$' -e '/internal/client/gui$$')

.PHONY: crosscheck
crosscheck:
	GOOS=linux   GOARCH=amd64 go build -o /dev/null $(PORTABLE)
	GOOS=linux   GOARCH=arm64 go build -o /dev/null $(PORTABLE)
	GOOS=windows GOARCH=amd64 go build -o /dev/null $(PORTABLE)
	GOOS=darwin  GOARCH=arm64 go build -o /dev/null $(PORTABLE)

# Everything CI runs, in one command.
#
# The GUI needs no target of its own here: it is a package of this module, so
# lint and test reach it the same way they reach everything else. The cost is
# that both now need cgo and the OpenGL and X11 headers Fyne links against.
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
