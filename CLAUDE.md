# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What this is

`svpn` is a FortiGate SSL VPN client in pure Go, written to replace
`sudo openfortivpn` for connecting to the Sorint.LAB corporate VPN. It has no
external runtime dependencies: no `openfortivpn`, no `pppd`, no NetworkManager
plugin. The whole protocol stack — SAML login, session allocation, PPP-over-TLS
framing, LCP/IPCP negotiation, tun device, routing, DNS — is implemented here.

Two binaries: `svpn` (unprivileged client) and `svpnd` (privileged daemon).
`README.md` explains the split and the protocol; do not duplicate it here.

## Commands

```sh
make build       # -> bin/svpn, bin/svpnd
make test        # go test -race ./...
make lint        # golangci-lint fmt --diff + golangci-lint run
make crosscheck  # linux/amd64, windows/amd64, darwin/arm64 must all build
make check       # all three of the above, plus check-config
make check-config # actionlint + goreleaser check + shellcheck install.sh
make snapshot    # goreleaser builds the release archives without tagging
make release     # what the release workflow runs on a tag
```

Run `make check` before declaring work finished. `make crosscheck` matters more
than it looks: `netcfg` and `ipc` have per-platform files, and it is easy to
break a build target you are not on.

`golangci-lint` is a **tool dependency** in `go.mod`, so `make lint` runs
`go tool golangci-lint` and the version lives in exactly one place. CI runs the
same one without naming it. To change it: `go get -tool
github.com/golangci/golangci-lint/v2/cmd/golangci-lint@vX.Y.Z`, never by hand.
The cost is that its whole module graph sits in go.mod and go.sum as `// indirect`
— which is why `goreleaser` is *not* a tool dependency: it is far larger, it is
only used by `make snapshot`, and the release workflow uses its own action. That
one is fetched on demand by `go run`, as is `actionlint`.

**go.mod names `go 1.27`, with no patch version, and that is deliberate.**
`setup-go` reads it and installs the runner's preinstalled latest 1.27.x, which
needs no download; pinning `1.27.0` would make it fetch that exact patch and
would leave the project one patch behind every tool that wants a current one —
which is how CI first broke here, on goreleaser needing 1.27.1. Do not add the
patch back. The `GOTOOLCHAIN=auto` on the tool invocations in the Makefile is
the remaining fallback, for a tool that wants a newer *minor*.

**The workflows pin nothing of their own.** `ci` runs `go tool golangci-lint`
and `make check-config`; `release` runs `make check` and `make release`. Every
version lives in `go.mod` or the Makefile, so there is no second place to
update and no way for CI to run a tool that was never run here. Keep it that
way — a `version:` input in a workflow is the drift this replaced.

`make check-config` is the only thing that looks at the workflows, the release
config and `install.sh`. Without it they are first exercised by pushing a tag,
which is the worst moment to find out one is wrong. It skips `shellcheck` when
it is not installed and says so; the runners have it, so `install.sh` is really
checked there.

To run the daemon locally without installing anything:

```sh
sudo ./bin/svpnd --socket-group "$(id -gn)" --allow-uid "$(id -u)"
./bin/svpn status
```

The default `--socket-group` is `svpn`, which only exists after
`sudo svpnd install` has created it. Keep unix socket paths under 107 bytes — that is all
that fits in `sockaddr_un.sun_path`, and `ipc.Listen` refuses up front rather
than letting `bind` fail with an unhelpful `EINVAL`.

## Conventions

- **Errors: `github.com/sorintlab/errors`, never `fmt.Errorf`.** Use
  `errors.Wrapf(err, "doing the thing")` to wrap and `errors.Errorf` to
  create. It re-exports `Is`, `As` and `Unwrap`, so it is a drop-in for the
  stdlib package in imports. This is what makes `--detailed-errors` produce a
  stack trace that reaches the real failure instead of stopping at the command
  layer. Both halves are enforced by `.golangci.yml`: `forbidigo` rejects
  `fmt.Errorf` and `depguard` rejects importing stdlib `errors`. `wrapcheck` is
  on too, so an error crossing a package boundary unwrapped is a lint failure —
  including a one-line delegation, where the wrap is what gives an otherwise
  stackless error a stack. `err113` is deliberately **not** enabled: it forbids
  dynamic errors, which is exactly the `errors.Errorf` idiom above.
- **CLI: cobra**, one file per command under `cmd/<binary>/cmd/`. Each command
  declares its options struct, registers flags in `init()`, and wraps its body
  with the local `run()` helper, which handles exit codes and treats a
  cancelled context as Ctrl-C rather than a failure.
- **Logging: `svpn: error: …` for a person, `<3>message key=value` for
  journald.** `internal/log` picks by destination, not by binary — `svpnd`
  under systemd and `svpnd install` in a terminal are different audiences for
  the same program. So: no timestamp (the reader is watching, or journald
  already stamped it), no source location, no level word on an ordinary line,
  no colour and no icons, and structured `key=value` only in the daemon.
  `--debug` is what brings the timestamp back. Two things are easy to
  undo by accident: the `<N>` prefix is the *only* way a level reaches
  `journalctl -p`, and `JOURNAL_STREAM` must be compared against stderr's
  device and inode rather than merely being present — it is inherited by every
  descendant of a unit, and reading it alone made the client print `<3>` at an
  interactive prompt.
- **Comments explain why, not what.** The convention throughout this codebase
  is that a comment names the failure that motivated the code — see
  `netcfg/linux.go` on why the address is a `/32` with no peer, or
  `tundev/device.go` on the headroom constant. Match that. A comment
  restating the line below it is noise.
- **Sorint-specific values live in `internal/sorint`, nowhere else.**
  `internal/forti` is a generic FortiGate client and must not learn the
  company's hostname. This is what lets the unit `svpnd install` writes carry
  no gateway arguments at all.
- **`pkg/ipc` is public on purpose, and it is the only thing in `pkg/`.** A
  Wails desktop GUI (the sibling project at `../sorint`, a separate module)
  will be another client of the same daemon, and Go would not let it import
  the protocol from `internal/`. Do not move it — and do not put anything else
  next to it: `pkg/` means "the wire protocol", and a second package with a
  weaker reason erodes that. `browser` used to live there and was moved to
  `internal/` for exactly this reason; Wails opens its own URLs, so nothing
  outside this module ever wanted it.
- **Installation is code, not a README.** `internal/install` owns the group,
  the systemd unit and the uid allowlist, and `svpnd install` is the single
  privileged entry point: `install.sh`, `make install` and — eventually — a GUI
  under `pkexec` all reach the system through it. Nothing about installing may
  be duplicated in `install.sh`, which only downloads, verifies and hands over;
  two copies of that knowledge drift. The unit is a `go:embed` template rather
  than a static file because `ExecStart` has to name the prefix the install
  chose, and `svpnd install` reads `PKEXEC_UID` as well as `SUDO_USER` because
  that is how a GUI will ask for the privilege.
- **`install.Plan` decides, `install.Apply` acts**, the same split as `netcfg`.
  Plan only reads, which is what makes `--dry-run` trustworthy and what lets
  the installer be exercised against a temp directory with `--prefix`,
  `--unit-dir`, `--conf-dir` and `--no-service` — no root, no `CAP_NET_ADMIN`.
  Put new work on the Plan side of that line wherever it can go there.
- **A GUI logs in by running `svpn login`, not by importing the SAML flow.**
  That is why `forti.SAMLLogin` can stay in `internal/`. The flow has details
  that are easy to get subtly wrong — the loopback callback, the validated
  session id, the cookie taken verbatim because `net/http`'s parser would
  rewrite it — and one implementation of it is the point. If this is ever
  revisited, the decision to change is "does the login flow get a public
  home", not "should this one helper be exported".

## Protocol facts that are not guessable

The FortiGate SSL VPN protocol is not publicly specified. This implementation
follows the observable behaviour of `openfortivpn` (GPL-3.0; no code was
taken from it). These cost real debugging time — do not "simplify" them away:

- **`/remote/index` and `/remote/fortisslvpn` are mandatory** before opening
  the tunnel. A valid cookie is not enough: until a VPN is allocated for the
  session, `/remote/sslvpn-tunnel` closes with no response, surfacing as an
  immediate `EOF`.
- **HTTP 403 on those two is not a failure.** The allocation happens as a side
  effect of the request being processed. `openfortivpn` ignores the status
  here too. Do not add a status check.
- **The client must speak first.** PPP is symmetric; the gateway stays silent
  until it receives an LCP Configure-Request. Without one the tunnel is open
  but idle and reads time out. `openfortivpn` gets this free because `pppd`
  talks as soon as it is spawned.
- **The split-tunnel list appears under two different XML element names**,
  `<split-tunnel-include>` or `<split-tunnel-info><addr>`, depending on FortiOS
  version. Reading only one yields an empty list, which is indistinguishable
  from "the gateway wants a full tunnel" — and silently sends all traffic into
  the VPN. Both are parsed; see `forti/testdata/sslvpn_tunnel.xml`, a capture
  of the real gateway.
- **The list repeats entries and collides with local networks.** The real
  gateway publishes ~180 routes including duplicates and `172.17.0.0/16`,
  which is `docker0` on a typical developer machine. Entries are deduplicated,
  and a route that already exists is left alone and reported as a conflict
  rather than aborting the connection.
- **`ip addr add X peer Y` builds a routing loop.** The peer a FortiGate
  reports is its own public address, so naming it installs a route to the
  gateway *through the tunnel* — which the tunnel is carried over. Hence
  `/32` with no peer, the gateway route pinned first, and a verification step
  after the routes are installed.
- **wireguard-go's `tun` package has three sharp edges.** `Read`/`Write` need
  an offset of at least `virtioNetHdrLen` (10) — `tundev` uses 16. A read
  must supply exactly `device.BatchSize()` buffers (128), or it fails with
  `ErrTooManySegments`, which the library documents as *recoverable*: do not
  treat it as fatal. And there is **no read deadline**: a `Read` parks until a
  packet arrives, and only `Close` releases it. So `vpn.Connection.Close` tears
  the device down *before* waiting for the data-plane goroutines. Waiting first
  deadlocks on any interface that happens to be quiet — which is every
  interface with no traffic on it — and `svpn down` hangs until the client
  gives up, leaving systemd to SIGKILL a daemon that never reverted anything.

## Development environment constraints

**There is no `CAP_NET_ADMIN` here.** Verify with `capsh --print` if in doubt.
This means the tun device, routing and DNS paths cannot be exercised locally.
Design around it: `vpn.Connect` takes `Dial`, `NewTUN` and `Configurator` as
injectable seams so the orchestration can be driven with fakes.

Do not claim a change to `netcfg` has been tested when it has not. Say what
was verified and what was not.

## Current state

Working end to end against the real `vpn.sorint.it` gateway on Linux: SAML
login, allocation, PPP negotiation, tun device, split routes, DNS.

Known gaps, all deliberate and all honest in the README:

- **macOS and Windows daemon.** The protocol half builds for all three
  targets and `svpn debug connect` is expected to work there, though it has
  only ever been run on Linux. `netcfg` and `ipc` return
  explicit "not implemented" errors. macOS needs `scutil`/`route` and
  `LOCAL_PEERCRED`; Windows needs named pipes (go-winio or syscalls) with a
  security descriptor.
- **`internal/vpn` is covered through its seams** (~74%), against a fake
  gateway that negotiates PPP; see `internal/vpn/connection_test.go` for the
  fakes. What no test here reaches is a real tun device, so the teardown's
  reliance on `Close` releasing a blocked `Read` is unexercised.
- **`internal/netcfg/linux.go` is ~4% covered.** What it decides before
  running a command is factored out and tested; the commands are not. This is
  now the largest untested surface.
- **No distro packaging** — no .deb/.rpm/AUR, deliberately. Distribution is
  GitHub Releases plus `svpnd install`; `.goreleaser.yml` builds linux
  amd64/arm64 only, because the daemon does not work anywhere else and shipping
  a binary that cannot install itself would promise something untrue.
- **The release and CI workflows have never run.** `make check-config` validates
  them with `actionlint` and `goreleaser check`, and `make snapshot` proves the
  goreleaser half locally, but nothing in `.github/workflows/` executes until a
  push and a tag. `svpnd update` is in the same position: the download, checksum
  and extraction are tested against an `httptest.Server`, the hand-over to the
  new binary's installer is not.
- **`install.sh` has never been executed.** It needs a published release to
  point at. `sh -n` and — in CI only — `shellcheck` are all it gets.
- **A real installation is untested.** `svpnd install` is exercised end to end
  against a temp prefix with `--no-service`, which covers everything except the
  three steps that need root — `groupadd`, `usermod` and `systemctl`.
- **Wails GUI integration** not started.

## Security

- **The session cookie is a live credential.** Anyone holding an `SVPNCOOKIE`
  can open the VPN as that user until it expires. Never write one into a
  commit, a test fixture, or a log at default level. If one appears in
  conversation, say so and recommend closing the session from the portal.
- `--debug` on `svpnd` traces the protocol exchange, **which includes the
  cookie**. The daemon warns at startup when this is on. Keep that warning.
- The daemon identifies callers with `SO_PEERCRED`, taken from the kernel and
  unforgeable, not from anything the client claims. The socket is mode 0660
  with a group as the first gate; `--allow-uid` is the second. Do not weaken
  either without being asked.
- Clients are meant to run unprivileged. `sudo svpn up` makes the client uid 0,
  which an allowlist of ordinary users correctly refuses.
