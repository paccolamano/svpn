// Package probe speaks to the FortiGate directly and reports what it says.
//
// It is the one thing here that reaches past the daemon deliberately. "svpn
// debug" exists to answer whether a failure is the protocol or the machine,
// and a client of svpnd cannot answer that by asking svpnd. Nothing in this
// package is privileged and nothing it does persists: no interface, no
// routes, no DNS, which is what makes it safe to run while trying to work out
// why the real thing will not come up.
//
// Gateway mirrors the fields of forti.Gateway rather than taking one, for the
// same reason auth.Options does: the FortiGate protocol stays an
// implementation detail of the packages that must speak it, and a caller
// naming a gateway does not have to import it.
package probe

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/internal/forti"
	"github.com/paccolamano/svpn/internal/ppp"
)

// printer writes the report and remembers the first failure.
//
// Output went to fmt.Printf while this was a command; against an arbitrary
// io.Writer a write can fail, and checking thirty of them in line would bury
// the report in error handling. The first failure stops the rest and is
// returned once, at the end.
type printer struct {
	w   io.Writer
	err error
}

func (p *printer) printf(format string, args ...any) {
	if p.err != nil {
		return
	}

	_, p.err = fmt.Fprintf(p.w, format, args...)
}

func (p *printer) println(args ...any) {
	if p.err != nil {
		return
	}

	_, p.err = fmt.Fprintln(p.w, args...)
}

// Gateway identifies the gateway to probe and how to reach it.
type Gateway struct {
	// Host and Port identify the gateway.
	Host string
	Port int
	// Realm is optional, and only some gateways use one.
	Realm string
	// UserAgent overrides what is presented to the gateway. Empty is the
	// default, which mirrors what openfortivpn sends.
	UserAgent string
	// Insecure disables TLS verification. It exists for probing a gateway
	// whose certificate is not trusted locally, and must not be used for
	// anything carrying real traffic.
	Insecure bool
	// Debug traces every request and response, which includes the session
	// cookie — a live credential.
	Debug bool
}

// Addr is the gateway as host:port.
func (g Gateway) Addr() string { return g.gateway().Addr() }

func (g Gateway) gateway() forti.Gateway {
	return forti.Gateway{
		Host:      g.Host,
		Port:      g.Port,
		Realm:     g.Realm,
		UserAgent: g.UserAgent,
		Insecure:  g.Insecure,
		Debug:     g.Debug,
	}
}

// Options tune one probe.
type Options struct {
	// Packets stops a passive probe after this many. Zero reads until the
	// context is cancelled.
	Packets int
	// Idle is how long to wait for a packet before giving up.
	Idle time.Duration
	// DumpConfig prints the raw XML the gateway returned.
	DumpConfig bool
	// Passive sends no opening LCP packet, so the link never comes up and the
	// gateway's own first move is what gets printed.
	Passive bool
}

// Run performs the post-authentication sequence, negotiates PPP and reports
// the network configuration the gateway assigned.
//
// The two steps before the tunnel are not optional: the gateway closes
// /remote/sslvpn-tunnel without a response unless a VPN has been allocated for
// the session first.
func Run(ctx context.Context, w io.Writer, gateway Gateway, cookie string, options Options) error {
	gw := gateway.gateway()
	out := &printer{w: w}

	stepCtx, cancelSteps := context.WithTimeout(ctx, 60*time.Second)
	defer cancelSteps()

	out.println("Requesting a VPN allocation ...")
	if err := forti.RequestVPNAllocation(stepCtx, gw, cookie); err != nil {
		return errors.Wrapf(err, "allocating the VPN")
	}

	out.println("Fetching the tunnel configuration ...")
	config, err := forti.GetConfig(stepCtx, gw, cookie)
	if err != nil {
		// A configuration that cannot be parsed is worth reporting, but the
		// tunnel does not depend on it here, so carry on.
		out.printf("  warning: %v\n", err)
	}
	printConfig(out, config, options.DumpConfig)

	out.printf("Opening the tunnel to %s ...\n", gw.Addr())

	dialCtx, cancelDial := context.WithTimeout(ctx, 30*time.Second)
	defer cancelDial()

	// In passive mode nothing else will speak, so an opening packet is sent
	// here; when negotiating, the PPP session sends its own.
	var opening []byte
	if options.Passive {
		opening = forti.NewLCPConfigureRequest(1)
	}

	tunnel, info, err := forti.OpenTunnel(dialCtx, gw, cookie, opening)
	if err != nil {
		return errors.Wrapf(err, "opening the tunnel")
	}
	defer func() { _ = tunnel.Close() }()

	out.printf("Tunnel open over %s from %s\n", info.TLSVersion, tunnel.LocalAddr())
	if info.PeerCertSHA256 != "" {
		out.printf("Gateway certificate SHA-256: %s\n", info.PeerCertSHA256)
	}
	out.println()

	// Reads block until the deadline, so cancellation is turned into one.
	go func() {
		<-ctx.Done()
		_ = tunnel.SetReadDeadline(time.Now())
	}()

	if options.Passive {
		if err := observe(ctx, out, tunnel, options.Packets, options.Idle); err != nil {
			return err
		}
	} else if err := negotiate(ctx, out, tunnel, options.Idle); err != nil {
		return err
	}

	return errors.Wrapf(out.err, "writing the report")
}

// timedTransport bounds how long each read may block, so a silent gateway
// surfaces as a timeout rather than hanging the negotiation.
type timedTransport struct {
	tunnel *forti.Tunnel
	idle   time.Duration
}

func (t timedTransport) ReadPacket() ([]byte, error) {
	if err := t.tunnel.SetReadDeadline(time.Now().Add(t.idle)); err != nil {
		return nil, errors.Wrapf(err, "setting the read deadline")
	}

	payload, err := t.tunnel.ReadPacket()

	return payload, errors.Wrapf(err, "reading from the tunnel")
}

func (t timedTransport) WritePacket(packet []byte) error {
	return errors.Wrapf(t.tunnel.WritePacket(packet), "writing to the tunnel")
}

// negotiate brings the PPP link up and prints what was assigned.
func negotiate(ctx context.Context, out *printer, tunnel *forti.Tunnel, idle time.Duration) error {
	out.println("Negotiating PPP:")

	session := ppp.NewSession(timedTransport{tunnel: tunnel, idle: idle})
	session.Logf = func(format string, args ...any) {
		out.printf("  "+format+"\n", args...)
	}

	negotiated, err := session.Negotiate(ctx)
	if err != nil {
		return errors.Wrapf(err, "negotiating PPP")
	}

	out.println()
	out.println("Link established. The gateway assigned:")
	out.printf("  address:        %s\n", negotiated.LocalIP)
	out.printf("  gateway:        %s\n", negotiated.RemoteIP)
	if negotiated.PrimaryDNS != nil {
		out.printf("  primary DNS:    %s\n", negotiated.PrimaryDNS)
	}
	if negotiated.SecondDNS != nil {
		out.printf("  secondary DNS:  %s\n", negotiated.SecondDNS)
	}

	out.println()
	out.println("PPP is fully negotiated: the protocol works end to end.")
	out.println("Traffic still cannot flow: there is no network interface and no routes,")
	out.println("which is the privileged work svpnd exists to do.")

	return nil
}

// observe prints the packets the gateway sends without answering them.
func observe(ctx context.Context, out *printer, tunnel *forti.Tunnel, packets int, idle time.Duration) error {
	out.println("Passive mode: packets are printed, not answered, so the link stays down.")
	out.println()

	for i := 0; packets == 0 || i < packets; i++ {
		if err := tunnel.SetReadDeadline(time.Now().Add(idle)); err != nil {
			return errors.Wrapf(err, "setting the read deadline")
		}

		payload, err := tunnel.ReadPacket()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return errors.Wrapf(ctxErr, "watching the tunnel")
			}
			if errors.Is(err, os.ErrDeadlineExceeded) {
				if i > 0 {
					out.printf("\n  gateway quiet for %s, waiting for a reply it will not get\n", idle)
					break
				}
				return errors.Errorf("no packet within %s: the tunnel opened but the gateway never spoke", idle)
			}
			return errors.Wrapf(err, "reading packet %d", i+1)
		}

		packet, err := forti.DecodePacket(payload)
		if err != nil {
			out.printf("  %3d  undecodable: %v\n", i+1, err)
			continue
		}

		out.printf("  %3d  %s\n", i+1, packet)
	}

	return nil
}

func printConfig(out *printer, config forti.TunnelConfig, dumpRaw bool) {
	if config.AssignedIPv4 != "" {
		out.printf("  assigned address: %s\n", config.AssignedIPv4)
	}
	if len(config.DNSServers) > 0 {
		out.printf("  DNS servers:      %s\n", strings.Join(config.DNSServers, ", "))
	}
	if config.DNSSuffix != "" {
		out.printf("  DNS suffix:       %s\n", config.DNSSuffix)
	}
	if len(config.SplitInclude) > 0 {
		routes := make([]string, 0, len(config.SplitInclude))
		for _, route := range config.SplitInclude {
			routes = append(routes, route.String())
		}
		out.printf("  split routes:     %s\n", strings.Join(routes, ", "))
	}

	if dumpRaw && len(config.Raw) > 0 {
		out.println("  raw configuration:")
		out.println(string(config.Raw))
	}
}
