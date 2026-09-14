package cli

import (
	"fmt"
	"log/slog"
	"math"
	"net"
	"strings"

	"github.com/sorintlab/errors"
	"github.com/spf13/cobra"

	"github.com/paccolamano/svpn/internal/build"
	"github.com/paccolamano/svpn/internal/forti"
	"github.com/paccolamano/svpn/internal/ipc"
	"github.com/paccolamano/svpn/internal/service/daemon"
	"github.com/paccolamano/svpn/internal/sorint"
)

type serveOptions struct {
	socket      string
	socketGroup string
	allowUIDs   []uint
	host        string
	port        int
	iface       string
	routes      []net.IPNet
	dnsDomains  []string
	fullTunnel  bool
}

var serveOpts serveOptions

func init() {
	flags := cmdSVPND.Flags()

	flags.StringVar(&serveOpts.socket, "socket", ipc.DefaultSocket, "path to listen on")
	flags.StringVar(&serveOpts.socketGroup, "socket-group", sorint.SocketGroup, "group given access to the socket; without it a root daemon's socket is unreachable by ordinary users")
	flags.UintSliceVar(&serveOpts.allowUIDs, "allow-uid", nil, "uid allowed to issue commands; repeatable. Without it, anyone who can open the socket can drive the VPN")
	flags.StringVar(&serveOpts.host, "host", sorint.Host, "default gateway hostname; clients may override it")
	flags.IntVar(&serveOpts.port, "port", sorint.Port, "default gateway port")
	flags.StringVar(&serveOpts.iface, "interface", sorint.Interface, "name to give the tunnel interface")
	flags.IPNetSliceVar(&serveOpts.routes, "route", nil, "network to send through the tunnel, e.g. 10.0.0.0/8; repeatable, overrides what the gateway publishes")
	flags.StringSliceVar(&serveOpts.dnsDomains, "dns-domain", nil, "domain to resolve through the tunnel's DNS, e.g. sorint.it; repeatable")
	flags.BoolVar(&serveOpts.fullTunnel, "full-tunnel", false, "send all traffic through the tunnel when no routes are known; only works if the gateway routes to the internet")
}

func serve(c *cobra.Command, args []string) error {
	ctx := c.Context()

	service := daemon.New(daemon.Options{
		DefaultGateway: forti.Gateway{
			Host:  serveOpts.host,
			Port:  serveOpts.port,
			Debug: svpndOpts.debug,
		},
		InterfaceName: serveOpts.iface,
		Routes:        serveOpts.routes,
		DNSDomains:    serveOpts.dnsDomains,
		FullTunnel:    serveOpts.fullTunnel,
		Version:       build.Version,
		Logf: func(format string, args ...any) {
			slog.Debug(strings.TrimSpace(fmt.Sprintf(format, args...)))
		},
	})

	listener, err := ipc.Listen(serveOpts.socket, serveOpts.socketGroup)
	if err != nil {
		return errors.Wrapf(err, "opening the control socket")
	}

	server := ipc.NewServer(service)
	server.Logf = func(format string, args ...any) {
		slog.Info(fmt.Sprintf(format, args...))
	}
	if len(serveOpts.allowUIDs) > 0 {
		authorize, err := authorizeUIDs(serveOpts.allowUIDs)
		if err != nil {
			return err
		}
		server.Authorize = authorize
	}

	slog.Info("listening", "socket", serveOpts.socket, "gateway", serveOpts.host)
	if len(serveOpts.allowUIDs) == 0 {
		slog.Warn("no uid allowlist configured; access is limited only by the socket's permissions")
	}
	if svpndOpts.debug {
		slog.Warn("debug logging traces the protocol exchange, which includes the session cookie")
	}

	serveErr := server.Serve(ctx, listener)

	// A tunnel outlives the socket, so it has to be torn down explicitly or
	// the machine is left with routes pointing at a dead interface.
	if err := service.Shutdown(); err != nil {
		slog.Error("could not tear the connection down", "error", err)
	}

	slog.Info("stopped")

	return errors.Wrapf(serveErr, "serving the control socket")
}

// authorizeUIDs restricts commands to a set of user ids.
//
// The uid comes from the kernel via SO_PEERCRED, not from anything the client
// says, so it cannot be forged by a process that reaches the socket.
func authorizeUIDs(allowed []uint) (func(ipc.Peer) error, error) {
	set := make(map[uint32]bool, len(allowed))
	for _, uid := range allowed {
		// SO_PEERCRED reports a uint32, so a larger value can never match any
		// caller. Truncating it silently would be worse than refusing: on a
		// 64-bit host it would wrap into a uid that does exist and would then
		// be allowed through.
		if uid > math.MaxUint32 {
			return nil, errors.Errorf("--allow-uid %d is not a valid user id", uid)
		}
		set[uint32(uid)] = true
	}

	return func(peer ipc.Peer) error {
		// Without credentials from the kernel there is nothing to check, and
		// guessing would defeat the point of having an allowlist.
		if !peer.Known {
			return errors.New("the caller's identity is unavailable on this platform, refusing")
		}
		if !set[peer.UID] {
			return errors.Errorf("uid %d is not allowed to control the VPN", peer.UID)
		}

		return nil
	}, nil
}
