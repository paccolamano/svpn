package ppp

import (
	"context"
	"net"
	"testing"
	"time"
)

// fakePeer is a gateway that negotiates the way a FortiGate does: it answers
// LCP, then Naks the unspecified IPCP addresses with real ones.
type fakePeer struct {
	t *testing.T

	assignIP  net.IP
	gatewayIP net.IP
	dns1      net.IP
	dns2      net.IP

	// rejectMagic makes the peer refuse the Magic-Number option, exercising
	// the retry path.
	rejectMagic bool

	toClient chan []byte
	sentLCP  bool
	sentIPCP bool
}

func newFakePeer(t *testing.T) *fakePeer {
	return &fakePeer{
		t:         t,
		assignIP:  net.IPv4(10, 110, 248, 51),
		gatewayIP: net.IPv4(10, 110, 248, 1),
		dns1:      net.IPv4(10, 140, 9, 4),
		dns2:      net.IPv4(10, 0, 16, 26),
		toClient:  make(chan []byte, 32),
	}
}

func (p *fakePeer) ReadPacket() ([]byte, error) {
	select {
	case frame := <-p.toClient:
		return frame, nil
	case <-time.After(2 * time.Second):
		p.t.Fatal("the client is waiting for a packet the peer never sent")
		return nil, nil
	}
}

func (p *fakePeer) queue(packet Packet) {
	p.toClient <- packet.Encode()
}

// WritePacket receives what the client sends and replies as a gateway would.
func (p *fakePeer) WritePacket(frame []byte) error {
	packet, err := Decode(frame)
	if err != nil {
		p.t.Fatalf("the client sent an undecodable frame: %v", err)
	}

	switch packet.Protocol {
	case ProtocolLCP:
		p.handleLCP(packet)
	case ProtocolIPCP:
		p.handleIPCP(packet)
	}

	return nil
}

func (p *fakePeer) handleLCP(packet Packet) {
	if packet.Code != CodeConfigureRequest {
		return
	}

	options, err := DecodeOptions(packet.Data)
	if err != nil {
		p.t.Fatalf("undecodable LCP options: %v", err)
	}

	if p.rejectMagic {
		for _, option := range options {
			if option.Type == LCPOptionMagicNumber {
				p.rejectMagic = false
				p.queue(Packet{
					Protocol:   ProtocolLCP,
					Code:       CodeConfigureReject,
					Identifier: packet.Identifier,
					Data:       EncodeOptions([]Option{option}),
				})
				return
			}
		}
	}

	p.queue(Packet{
		Protocol:   ProtocolLCP,
		Code:       CodeConfigureAck,
		Identifier: packet.Identifier,
		Data:       packet.Data,
	})

	// The gateway also has to configure its own direction.
	if !p.sentLCP {
		p.sentLCP = true
		p.queue(Packet{
			Protocol:   ProtocolLCP,
			Code:       CodeConfigureRequest,
			Identifier: 100,
			Data:       EncodeOptions([]Option{mruOption(DefaultMRU)}),
		})
	}
}

func (p *fakePeer) handleIPCP(packet Packet) {
	if packet.Code != CodeConfigureRequest {
		return
	}

	options, err := DecodeOptions(packet.Data)
	if err != nil {
		p.t.Fatalf("undecodable IPCP options: %v", err)
	}

	var requestedIP net.IP
	for _, option := range options {
		if option.Type == IPCPOptionIPAddress {
			requestedIP, _ = optionIP(option)
		}
	}

	// An unspecified address is a request to be assigned one.
	if isUnspecified(requestedIP) {
		p.queue(Packet{
			Protocol:   ProtocolIPCP,
			Code:       CodeConfigureNak,
			Identifier: packet.Identifier,
			Data: EncodeOptions([]Option{
				ipOption(IPCPOptionIPAddress, p.assignIP),
				ipOption(IPCPOptionPrimaryDNS, p.dns1),
				ipOption(IPCPOptionSecondDNS, p.dns2),
			}),
		})
		return
	}

	p.queue(Packet{
		Protocol:   ProtocolIPCP,
		Code:       CodeConfigureAck,
		Identifier: packet.Identifier,
		Data:       packet.Data,
	})

	if !p.sentIPCP {
		p.sentIPCP = true
		p.queue(Packet{
			Protocol:   ProtocolIPCP,
			Code:       CodeConfigureRequest,
			Identifier: 200,
			Data:       EncodeOptions([]Option{ipOption(IPCPOptionIPAddress, p.gatewayIP)}),
		})
	}
}

func TestNegotiateReachesAConfiguredLink(t *testing.T) {
	peer := newFakePeer(t)
	session := NewSession(peer)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	config, err := session.Negotiate(ctx)
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}

	if !config.LocalIP.Equal(peer.assignIP) {
		t.Errorf("local address = %v, want %v", config.LocalIP, peer.assignIP)
	}
	if !config.RemoteIP.Equal(peer.gatewayIP) {
		t.Errorf("gateway address = %v, want %v", config.RemoteIP, peer.gatewayIP)
	}
	if !config.PrimaryDNS.Equal(peer.dns1) {
		t.Errorf("primary DNS = %v, want %v", config.PrimaryDNS, peer.dns1)
	}
	if !config.SecondDNS.Equal(peer.dns2) {
		t.Errorf("secondary DNS = %v, want %v", config.SecondDNS, peer.dns2)
	}
}

func TestNegotiateRetriesAfterARejectedOption(t *testing.T) {
	peer := newFakePeer(t)
	peer.rejectMagic = true

	session := NewSession(peer)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	config, err := session.Negotiate(ctx)
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if !config.LocalIP.Equal(peer.assignIP) {
		t.Errorf("local address = %v, want %v", config.LocalIP, peer.assignIP)
	}
}

func TestNegotiateFailsWhenTheGatewayTerminates(t *testing.T) {
	peer := newFakePeer(t)
	session := NewSession(peer)

	peer.queue(Packet{Protocol: ProtocolLCP, Code: CodeTerminateRequest, Identifier: 1})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := session.Negotiate(ctx); err == nil {
		t.Fatal("expected an error when the gateway terminates, got nil")
	}
}
