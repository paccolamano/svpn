package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sorintlab/errors"

	"github.com/paccolamano/svpn/internal/forti"
	"github.com/paccolamano/svpn/internal/ppp"
)

// probe runs the post-authentication sequence, negotiates PPP and reports the
// network configuration the gateway assigned.
//
// The two steps before the tunnel are not optional: the gateway closes
// /remote/sslvpn-tunnel without a response unless a VPN has been allocated for
// the session first.
func probe(ctx context.Context, gw forti.Gateway, cookie string, options probeOptions) error {
	stepCtx, cancelSteps := context.WithTimeout(ctx, 60*time.Second)
	defer cancelSteps()

	fmt.Println("Requesting a VPN allocation ...")
	if err := forti.RequestVPNAllocation(stepCtx, gw, cookie); err != nil {
		return errors.Wrapf(err, "allocating the VPN")
	}

	fmt.Println("Fetching the tunnel configuration ...")
	config, err := forti.GetConfig(stepCtx, gw, cookie)
	if err != nil {
		// A configuration that cannot be parsed is worth reporting, but the
		// tunnel does not depend on it here, so carry on.
		fmt.Printf("  warning: %v\n", err)
	}
	printConfig(config, options.dumpConfig)

	fmt.Printf("Opening the tunnel to %s ...\n", gw.Addr())

	dialCtx, cancelDial := context.WithTimeout(ctx, 30*time.Second)
	defer cancelDial()

	// In passive mode nothing else will speak, so an opening packet is sent
	// here; when negotiating, the PPP session sends its own.
	var opening []byte
	if options.passive {
		opening = forti.NewLCPConfigureRequest(1)
	}

	tunnel, info, err := forti.OpenTunnel(dialCtx, gw, cookie, opening)
	if err != nil {
		return errors.Wrapf(err, "opening the tunnel")
	}
	defer func() { _ = tunnel.Close() }()

	fmt.Printf("Tunnel open over %s from %s\n", info.TLSVersion, tunnel.LocalAddr())
	if info.PeerCertSHA256 != "" {
		fmt.Printf("Gateway certificate SHA-256: %s\n", info.PeerCertSHA256)
	}
	fmt.Println()

	// Reads block until the deadline, so cancellation is turned into one.
	go func() {
		<-ctx.Done()
		_ = tunnel.SetReadDeadline(time.Now())
	}()

	if options.passive {
		return observe(ctx, tunnel, options.packets, options.idle)
	}

	return negotiate(ctx, tunnel, options.idle)
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
func negotiate(ctx context.Context, tunnel *forti.Tunnel, idle time.Duration) error {
	fmt.Println("Negotiating PPP:")

	session := ppp.NewSession(timedTransport{tunnel: tunnel, idle: idle})
	session.Logf = func(format string, args ...any) {
		fmt.Printf("  "+format+"\n", args...)
	}

	negotiated, err := session.Negotiate(ctx)
	if err != nil {
		return errors.Wrapf(err, "negotiating PPP")
	}

	fmt.Println()
	fmt.Println("Link established. The gateway assigned:")
	fmt.Printf("  address:        %s\n", negotiated.LocalIP)
	fmt.Printf("  gateway:        %s\n", negotiated.RemoteIP)
	if negotiated.PrimaryDNS != nil {
		fmt.Printf("  primary DNS:    %s\n", negotiated.PrimaryDNS)
	}
	if negotiated.SecondDNS != nil {
		fmt.Printf("  secondary DNS:  %s\n", negotiated.SecondDNS)
	}

	fmt.Println()
	fmt.Println("PPP is fully negotiated: the protocol works end to end.")
	fmt.Println("Traffic still cannot flow: there is no network interface and no routes,")
	fmt.Println("which is the privileged work svpnd exists to do.")

	return nil
}

// observe prints the packets the gateway sends without answering them.
func observe(ctx context.Context, tunnel *forti.Tunnel, packets int, idle time.Duration) error {
	fmt.Println("Passive mode: packets are printed, not answered, so the link stays down.")
	fmt.Println()

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
					fmt.Printf("\n  gateway quiet for %s, waiting for a reply it will not get\n", idle)
					break
				}
				return errors.Errorf("no packet within %s: the tunnel opened but the gateway never spoke", idle)
			}
			return errors.Wrapf(err, "reading packet %d", i+1)
		}

		packet, err := forti.DecodePacket(payload)
		if err != nil {
			fmt.Printf("  %3d  undecodable: %v\n", i+1, err)
			continue
		}

		fmt.Printf("  %3d  %s\n", i+1, packet)
	}

	return nil
}

func printConfig(config forti.TunnelConfig, dumpRaw bool) {
	if config.AssignedIPv4 != "" {
		fmt.Printf("  assigned address: %s\n", config.AssignedIPv4)
	}
	if len(config.DNSServers) > 0 {
		fmt.Printf("  DNS servers:      %s\n", strings.Join(config.DNSServers, ", "))
	}
	if config.DNSSuffix != "" {
		fmt.Printf("  DNS suffix:       %s\n", config.DNSSuffix)
	}
	if len(config.SplitInclude) > 0 {
		routes := make([]string, 0, len(config.SplitInclude))
		for _, route := range config.SplitInclude {
			routes = append(routes, route.String())
		}
		fmt.Printf("  split routes:     %s\n", strings.Join(routes, ", "))
	}

	if dumpRaw && len(config.Raw) > 0 {
		fmt.Println("  raw configuration:")
		fmt.Println(string(config.Raw))
	}
}
