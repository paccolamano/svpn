# svpn

Connect to the Sorint.LAB VPN with ease.

A FortiGate SSL VPN client in Go, with no external components: no
`openfortivpn`, no `pppd`, no NetworkManager plugin. A privileged daemon owns
the connection, and two unprivileged clients drive it — a command line and a
window. Either will do; they are the same client underneath.

```sh
svpn up       # authenticate in the browser, then connect
svpn status
svpn down
```

```sh
svpn-gui      # or the same thing from your applications menu
```

## Why a daemon

Connecting a VPN means creating a network interface, rewriting the routing
table and redirecting DNS. All three are global to the machine and gated behind
`CAP_NET_ADMIN`, and a desktop client has no terminal to type a sudo password
into. So the privilege is taken once, at install time, by a service — and
everything else runs as an ordinary user and asks that service to act.

```
svpn      ─┐
           ├──unix socket──> svpnd (daemon) ──> svpn0, routes, DNS
svpn-gui  ─┘   your uid          root
```

The client performs the SAML login, because it owns your browser session, and
hands the resulting cookie to the daemon. The daemon never needs a browser, a
display, or your identity provider: the cookie is all of your identity it ever
sees.

This is the split Mullvad, Tailscale and Docker all use, and it is the only one
that lets a GUI connect a VPN without asking for a password every time.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/paccolamano/svpn/main/install.sh | sh
newgrp svpn      # or log out and back in
svpn up
```

The script resolves the latest release, checks the archive against its
published SHA-256, unpacks it and runs `svpnd install`. It decides nothing
itself — everything about the installation lives in the binary.

If you would rather not pipe a script into a shell, do the same by hand:
download the archive and `SHA256SUMS` from the
[latest release](https://github.com/paccolamano/svpn/releases/latest), then

```sh
sha256sum -c --ignore-missing SHA256SUMS
tar -xzf svpn_*_linux_amd64.tar.gz
sudo ./svpnd install
```

Or from source:

```sh
make build
sudo ./bin/svpnd install
```

All three end in the same place. `svpnd install` copies the binaries to
`/usr/local/bin`, creates the `svpn` group, adds you to it, writes the systemd
unit and `/etc/svpn/svpnd.env`, and starts the service. `--dry-run` prints what
it would do; `--prefix`, `--unit-dir` and `--conf-dir` move any of it.

The desktop client comes with it when the archive has one, along with a
launcher entry so it appears in your applications menu. The **amd64** archive
has one; the **arm64** archive does not, because Fyne needs an OpenGL toolchain
built for the target and cross-compiling that is a different problem from
cross-compiling Go. On a machine with no display, `--no-gui` skips it:

```sh
curl -fsSL https://raw.githubusercontent.com/paccolamano/svpn/main/install.sh | sh -s -- --no-gui
```

`svpnd update` keeps whichever choice you made — it looks for an installed
`svpn-gui` before handing over, so a server does not acquire a window the first
time it updates.

**The `newgrp` is not optional, and opening a new terminal is not a substitute.**
`usermod -aG` does not reach sessions that already exist, because a process's
groups are fixed by `setgroups` at login and nothing can add one afterwards —
which is why `newgrp` starts a new shell rather than changing the one you ran
it in. A new terminal window is forked from your desktop session, which
predates the group, so it inherits the same list. Only `newgrp` (that shell) or
a fresh login (everything) works. Without one, the first `svpn up` cannot open
the socket — the client says exactly that when it happens.

The daemon needs no arguments: every default — gateway, socket, interface,
group — is the Sorint one, and they live in `internal/sorint`. The one thing
the installation adds is `--allow-uid` for your account, in
`/etc/svpn/svpnd.env`. Edit that file to change the daemon's flags; the next
install merges into it rather than overwriting it, so a second user is added
to the allowlist instead of replacing the first.

### Updating

```sh
sudo svpnd update           # download the latest release and install it
sudo svpnd update --check   # or just ask
svpn version                # what is installed, and what the daemon is running
```

There is no package manager in this picture, so this is the update channel.
`update` verifies the archive against its published checksum and then runs the
installer *from the version it just downloaded*, so an upgrade that needs a
different installation procedure brings that procedure with it.

The checksum proves the download arrived intact, not where it came from — the
archive and `SHA256SUMS` are published together. For provenance, every release
carries a build attestation:

```sh
gh attestation verify svpn_1.0.0_linux_amd64.tar.gz --repo paccolamano/svpn
```

A running daemon carrying a live tunnel is left alone rather than restarted:
the new binary takes effect after `svpn down`, or immediately with `--restart`.

### Uninstalling

```sh
sudo svpnd uninstall           # stop, disable, remove the unit and binaries
sudo svpnd uninstall --purge   # and the group and /etc/svpn/svpnd.env
```

Stopping the service sends a TERM, which is what lets the daemon close the
tunnel and put the routing table back before it exits.

## Usage

```sh
svpn up          # opens a browser, authenticates, connects
svpn status
svpn down
```

Never run these under `sudo`. That would make the client uid 0, which the
daemon's allowlist of ordinary users refuses — correctly. The client is meant
to be unprivileged; that is the whole point of the split.

`svpn login` does the browser half on its own and prints the cookie, for
scripting or for holding one across several attempts. `svpn up --cookie` then
reuses it and skips the browser.

Every command takes `--json` for a machine-readable answer, `--debug` for the
protocol exchange, and `--detailed-errors` for stack traces.

The answer goes to stdout and the diagnostics to stderr, so `svpn status --json
| jq` works while warnings still reach you. Diagnostics read like any other
command — `svpn: error: …`, plain text, no colour anywhere. The daemon writes
the same lines with a syslog priority prefix when systemd is reading them, so
`journalctl -p err -u svpnd` finds errors and nothing repeats the timestamp
journald already recorded.

## Access control

A daemon running as root creates a root-owned socket that no ordinary user can
open, so `--socket-group` hands it to a group instead; the systemd unit does
the same through `Group=svpn`.

The socket is created mode 0660, so filesystem permissions are the first gate.
Past that, the daemon reads the caller's identity from the kernel with
`SO_PEERCRED` — unforgeable, unlike anything a client could claim — and
`--allow-uid` restricts commands to specific users. Without it, anyone who can
open the socket can drive the VPN; the daemon warns at startup when that is
the case.

The group is why a fresh installation needs one login before it works, and that
cost is deliberate. Making the socket's *owner* the installing user would
remove it, because owner permissions are checked against the uid every session
already has — but chowning to another uid needs `CAP_CHOWN`, and the unit drops
every capability except `CAP_NET_ADMIN` and `CAP_NET_RAW`. Widening the
capability set of a root daemon to save one logout is the worse trade.

This is deliberately stricter than the `docker` group model, where socket
access is equivalent to root.

**`--debug` on the daemon traces the protocol exchange, and that includes the
session cookie.** The cookie is a live credential: anyone holding it can open
the VPN as you until it expires. The daemon warns when debug logging is on.

## Debugging a connection

The protocol probes speak to the gateway and report what it says, without
creating an interface, touching routes, or going near the daemon. They need no
privileges and change nothing, which makes them the right tool when a
connection fails and the question is where.

```sh
svpn debug connect                 # login, allocation, config, tunnel, PPP
svpn debug tunnel --cookie '…'     # the same, starting from an existing cookie
svpn debug connect --dump-config   # print the gateway's raw XML
svpn debug connect --passive       # do not answer, just watch what arrives
```

Reaching `Link established` proves the gateway accepted the cookie and the
protocol works end to end. Everything after that is the operating system's
side of the problem.

## What it does

```
1.  browser  ->  https://<host>:<port>/remote/saml/start?redirect=1[&realm=…]
2.  listener on 127.0.0.1:8020  <-  GET /?id=<session-id>
3.  GET /remote/saml/auth_id?id=<id>   ->  Set-Cookie: SVPNCOOKIE=…

4.  GET /remote/index            \  the gateway allocates a VPN for the session
5.  GET /remote/fortisslvpn      /
6.  GET /remote/fortisslvpn_xml     ->  assigned address, DNS, split routes

7.  GET /remote/sslvpn-tunnel       ->  framed PPP packets
        Host: sslvpn
        Cookie: SVPNCOOKIE=…
8.  ->  LCP Configure-Request           the client speaks first
9.  LCP + IPCP negotiation           ->  assigned address, gateway, DNS
10. frame layout, big-endian:
        [total len 2B][magic 0x5050][payload len 2B][PPP packet]

11. tun device, addresses, routes, DNS                       (svpnd only)
```

Steps 4 and 5 are not optional. A cookie alone is not enough: until a VPN has
been allocated for the session, the gateway closes `/remote/sslvpn-tunnel`
without sending a response, which surfaces as an immediate EOF. They commonly
answer **403** to a non-browser client, and that is not a failure — the
allocation happens as a side effect of the request being processed.

Step 8 is not optional either. PPP is symmetric and the gateway stays silent
until the client has spoken — `openfortivpn` gets this for free because `pppd`
starts talking as soon as it is spawned. Without an opening packet the tunnel
is established but idle, and reads time out. `--passive` skips it, which
reproduces exactly that.

Step 6 decides which traffic goes where. FortiOS publishes its split-tunnel
list under either `<split-tunnel-include>` or `<split-tunnel-info><addr>`
depending on the version; reading only one of them silently yields no routes
at all, which is indistinguishable from a gateway that wants a full tunnel.
Both are read. The list repeats addresses and may collide with networks the
machine already has — a `docker0` bridge, the local LAN — so entries are
deduplicated, and a collision leaves the existing route alone and is reported
rather than aborting the connection.

The protocol is not publicly specified. Every step above follows the behaviour
of [openfortivpn](https://github.com/adrienverge/openfortivpn) (GPL-3.0), the
de-facto reference implementation. No code was taken from it.

## Layout

There are three binaries and two of them are clients of the third. The tree
says so:

```
cmd/svpn/        the CLI            \
cmd/svpn-gui/    the desktop client  |  three mains, nothing else in them
cmd/svpnd/       the daemon         /

internal/ipc     the protocol between the clients and the daemon
internal/forti   the FortiGate protocol: SAML, session, framing, tunnel
internal/ppp     LCP and IPCP, the client half of the RFC 1661 exchange

internal/client        dialling svpnd, diagnosing it, comparing versions
internal/client/auth   the browser login: the one place a client meets the gateway
internal/client/state  the controller: polling, connect and disconnect, no Fyne
internal/client/cli    svpn's commands
internal/client/gui    svpn-gui's widgets — the only package that needs cgo

internal/service/daemon  the state machine behind the socket
internal/service/vpn     orchestration and the data plane
internal/service/tundev  the tun device, over wireguard-go's platform layer
internal/service/netcfg  addresses, routes and DNS

internal/probe   speaking to the gateway with no daemon: svpn probe, svpn debug
internal/install the system service: group, unit, uid allowlist, launcher entry
internal/desktop the .desktop format, for the launcher and for autostart
internal/release resolving, verifying and unpacking a published build
internal/sorint  the Sorint-specific values — gateway, group, artwork
internal/log     the logger all three binaries share: levels, --detailed-errors
internal/build   the version the linker stamps in
```

There are four kinds of package here. Three are protocols — `ipc` is ours,
`forti` and `ppp` are the gateway's. Then `client/` is everything that *asks*
for a tunnel and `service/` is everything that *is* one; the first runs as you,
the second as root, and `ipc` is all that crosses between them.

**`client/` speaks only `ipc`.** The single exception is `client/auth`, and it
is why `auth` exists as a named boundary: the login needs a browser, and a
system service has neither a browser nor a display, so a client has to do it
and hand the cookie over. Past `auth`, nothing in `client/` sees a gateway.

`internal/probe` is what makes that hold rather than nearly hold. `svpn probe`
and `svpn debug connect` build a tunnel *without* the daemon — that is their
whole purpose, answering "is it the protocol or is it this machine" — so they
belong to neither side, and putting them in their own package is what keeps
`forti` out of the command layer.

`internal/client/gui` is the only package in the module that needs cgo, because
Fyne links against OpenGL. That is why the controller is `client/state` and not
part of it: the state machine stays in the half that `make crosscheck` and
`golangci-lint` reach, and only the widgets sit outside.

`internal/ppp` is kept independent of the FortiGate transport, so the same code
works over anything that carries PPP frames. The options it offers and accepts
mirror how `openfortivpn` invokes `pppd`: `noauth` (the cookie already
authenticated the session), `noaccomp` and `nopcomp` (both header fields stay
intact), `noipdefault` with `ipcp-accept-local` (the gateway assigns the
address) and `usepeerdns`.

## Platform support

| | protocol | daemon | desktop client |
|---|---|---|---|
| Linux amd64 | yes | yes | yes |
| Linux arm64 | yes | yes | not built |
| macOS | yes | not implemented | not built |
| Windows | yes | not implemented | not built |

The protocol half is pure Go with no cgo and builds for all four, so
`svpn debug connect` should work on each — though it has only been exercised on
Linux. The daemon does not build a working configuration elsewhere: macOS needs
`scutil` and `route` in `netcfg` plus `LOCAL_PEERCRED` in `ipc`, and Windows
needs named pipes with a security descriptor. Both return an explicit "not
implemented" error rather than pretending.

The desktop client is a different kind of gap. Its code is as portable as Fyne
is; what does not travel is the build. Fyne links against OpenGL, so it needs
cgo and a C toolchain **for the target**, which `go build` cannot produce on its
own. amd64 is native on the release runner and needs only the `-dev` headers;
arm64 would need a cross compiler plus arm64 X11 and GL headers, or a second
native runner. Until then the arm64 archive carries the daemon and the CLI, and
`svpnd install` says so rather than quietly doing less.

```sh
make check        # everything below, in one command
make test         # 140 tests, with -race
make lint         # golangci-lint: formatting, vet, and the rest
make crosscheck   # every target builds, minus the two cgo packages
make check-config # the workflows, the release config and install.sh
make snapshot     # build the release archives without tagging anything
```

`golangci-lint` is a tool dependency in `go.mod`, so there is nothing to
install: `go tool` builds the pinned version from the module cache, and CI runs
that same one.

The lint configuration enforces two conventions this file describes in prose:
`fmt.Errorf` is forbidden and so is importing the standard `errors`, because
`--detailed-errors` can only reach the real failure if every error carries a
stack. Both are `github.com/sorintlab/errors` instead.

Two gaps are worth naming, both from the same cause: the privileged half cannot
run under test.

`internal/netcfg/linux.go` shells out to `ip` and `resolvectl`, so exercising it
needs `CAP_NET_ADMIN`. Everything it decides *before* running a command — which
routes, which addresses, which DNS domains — is factored out and tested, but the
commands themselves are not.

`internal/vpn` is driven through its injected seams (`Dial`, `NewTUN`,
`Configurator`): the tests bring a connection up against a fake gateway that
negotiates PPP, and check that the teardown puts everything back. What they
cannot reach is a real tun device, so one assumption the shutdown rests on —
that closing the device releases a read blocked in it — holds by construction in
wireguard-go rather than by test here.
