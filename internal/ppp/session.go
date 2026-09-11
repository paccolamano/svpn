package ppp

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"

	"github.com/sorintlab/errors"
)

// Transport carries PPP frames. The FortiGate tunnel is one implementation.
type Transport interface {
	ReadPacket() ([]byte, error)
	WritePacket([]byte) error
}

// Config is the network configuration a completed negotiation yields.
type Config struct {
	LocalIP    net.IP
	RemoteIP   net.IP
	PrimaryDNS net.IP
	SecondDNS  net.IP
}

// Session negotiates a PPP link over a Transport.
//
// It implements the client side of the RFC 1661 option-negotiation exchange
// rather than the full state machine: enough to bring LCP and then IPCP to
// Opened and learn the assigned addresses, which is what a tunnel client
// needs.
type Session struct {
	transport Transport
	// Logf traces the exchange; nil silences it.
	Logf func(format string, args ...any)

	magic      uint32
	identifier byte

	// lcpLocalDone is set when the peer acknowledges our configuration;
	// lcpRemoteDone when we have acknowledged theirs. The link is up only once
	// both directions agree.
	lcpLocalDone  bool
	lcpRemoteDone bool

	ipcpLocalDone   bool
	ipcpRemoteDone  bool
	ipcpRequestSent bool

	// requested holds the addresses asked for in our IPCP Configure-Request.
	// They start unspecified and are filled in from the peer's Configure-Nak.
	requested Config
	// negotiated is what the peer finally acknowledged.
	negotiated Config
}

// NewSession returns a session ready to negotiate over transport.
func NewSession(transport Transport) *Session {
	var magic uint32
	buf := make([]byte, 4)
	// The magic number only has to differ from the peer's for loop detection,
	// so a failed read is not worth propagating.
	if _, err := rand.Read(buf); err == nil {
		magic = binary.BigEndian.Uint32(buf)
	} else {
		magic = 0xdeadbeef
	}

	return &Session{transport: transport, magic: magic}
}

func (s *Session) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// nextID returns the identifier for the next request we originate.
func (s *Session) nextID() byte {
	s.identifier++
	return s.identifier
}

// Negotiate brings up LCP and then IPCP, returning the assigned configuration.
//
// It reads until both protocols are open or ctx is done. The caller is
// responsible for bounding how long a single read may block.
func (s *Session) Negotiate(ctx context.Context) (Config, error) {
	if err := s.sendLCPConfigureRequest(); err != nil {
		return Config{}, err
	}

	for {
		if err := ctx.Err(); err != nil {
			return Config{}, errors.Wrapf(err, "negotiating the link")
		}

		frame, err := s.transport.ReadPacket()
		if err != nil {
			return Config{}, errors.Wrapf(err, "reading from the link")
		}

		packet, err := Decode(frame)
		if err != nil {
			// A frame that cannot be parsed is not fatal on its own; the peer
			// may send padding or a protocol we do not model.
			s.logf("  ignoring an undecodable frame: %v", err)
			continue
		}

		switch packet.Protocol {
		case ProtocolLCP:
			if err := s.handleLCP(packet); err != nil {
				return Config{}, err
			}
		case ProtocolIPCP:
			if err := s.handleIPCP(packet); err != nil {
				return Config{}, err
			}
		default:
			s.logf("  ignoring protocol 0x%04x", packet.Protocol)
			continue
		}

		if s.lcpLocalDone && s.lcpRemoteDone && !s.ipcpStarted() {
			s.logf("LCP is open, starting IPCP")
			if err := s.sendIPCPConfigureRequest(); err != nil {
				return Config{}, err
			}
		}

		if s.ipcpLocalDone && s.ipcpRemoteDone {
			return s.negotiated, nil
		}
	}
}

// ipcpStarted reports whether the IPCP exchange has been opened already.
func (s *Session) ipcpStarted() bool {
	return s.ipcpRequestSent
}

// --- LCP ---

// sendLCPConfigureRequest offers the options this client wants.
func (s *Session) sendLCPConfigureRequest() error {
	options := []Option{
		mruOption(DefaultMRU),
		magicNumberOption(s.magic),
	}

	packet := Packet{
		Protocol:   ProtocolLCP,
		Code:       CodeConfigureRequest,
		Identifier: s.nextID(),
		Data:       EncodeOptions(options),
	}

	s.logf("-> LCP Configure-Request id=%d (MRU %d, magic 0x%08x)", packet.Identifier, DefaultMRU, s.magic)

	return errors.Wrapf(s.transport.WritePacket(packet.Encode()), "sending LCP Configure-Request")
}

func (s *Session) handleLCP(packet Packet) error {
	switch packet.Code {
	case CodeConfigureRequest:
		return s.answerLCPConfigureRequest(packet)

	case CodeConfigureAck:
		s.logf("<- LCP Configure-Ack id=%d", packet.Identifier)
		s.lcpLocalDone = true
		return nil

	case CodeConfigureNak, CodeConfigureReject:
		// The peer refused part of our offer. Everything offered is optional,
		// so drop the disputed options and retry with a bare request rather
		// than insisting.
		s.logf("<- LCP Configure-%s id=%d, retrying without the disputed options",
			codeName(packet.Code), packet.Identifier)
		return s.resendLCPWithout(packet)

	case CodeEchoRequest:
		// Keepalive. The reply must carry our own magic number.
		s.logf("<- LCP Echo-Request id=%d", packet.Identifier)
		magic := make([]byte, 4)
		binary.BigEndian.PutUint32(magic, s.magic)
		reply := Packet{
			Protocol:   ProtocolLCP,
			Code:       CodeEchoReply,
			Identifier: packet.Identifier,
			Data:       magic,
		}
		s.logf("-> LCP Echo-Reply id=%d", packet.Identifier)
		return errors.Wrapf(s.transport.WritePacket(reply.Encode()), "sending LCP Echo-Reply")

	case CodeTerminateRequest:
		s.logf("<- LCP Terminate-Request id=%d", packet.Identifier)
		ack := Packet{Protocol: ProtocolLCP, Code: CodeTerminateAck, Identifier: packet.Identifier}
		_ = s.transport.WritePacket(ack.Encode())
		return errors.New("the gateway terminated the link")

	default:
		s.logf("<- LCP %s id=%d, ignored", codeName(packet.Code), packet.Identifier)
		return nil
	}
}

// answerLCPConfigureRequest acknowledges the peer's options, rejecting the
// ones this client will not support.
func (s *Session) answerLCPConfigureRequest(packet Packet) error {
	options, err := DecodeOptions(packet.Data)
	if err != nil {
		return errors.Wrapf(err, "parsing the peer's LCP options")
	}

	accepted, rejected := reviewLCPOptions(options)
	s.logf("<- LCP Configure-Request id=%d (%d option(s))", packet.Identifier, len(options))

	// A rejection has to come first: acknowledging a request while refusing
	// part of it would be contradictory, and the peer will ask again without
	// the rejected options.
	if len(rejected) > 0 {
		reply := Packet{
			Protocol:   ProtocolLCP,
			Code:       CodeConfigureReject,
			Identifier: packet.Identifier,
			Data:       EncodeOptions(rejected),
		}
		s.logf("-> LCP Configure-Reject id=%d (%d option(s))", packet.Identifier, len(rejected))
		return errors.Wrapf(s.transport.WritePacket(reply.Encode()), "sending LCP Configure-Reject")
	}

	reply := Packet{
		Protocol:   ProtocolLCP,
		Code:       CodeConfigureAck,
		Identifier: packet.Identifier,
		Data:       EncodeOptions(accepted),
	}
	s.logf("-> LCP Configure-Ack id=%d", packet.Identifier)

	if err := s.transport.WritePacket(reply.Encode()); err != nil {
		return errors.Wrapf(err, "sending LCP Configure-Ack")
	}

	s.lcpRemoteDone = true

	return nil
}

// resendLCPWithout retries our configuration request without whatever the peer
// objected to.
func (s *Session) resendLCPWithout(packet Packet) error {
	disputed, err := DecodeOptions(packet.Data)
	if err != nil {
		return errors.Wrapf(err, "parsing the peer's LCP response")
	}

	refused := make(map[byte]bool, len(disputed))
	for _, option := range disputed {
		refused[option.Type] = true
	}

	var options []Option
	if !refused[LCPOptionMRU] {
		options = append(options, mruOption(DefaultMRU))
	}
	if !refused[LCPOptionMagicNumber] {
		options = append(options, magicNumberOption(s.magic))
	}

	retry := Packet{
		Protocol:   ProtocolLCP,
		Code:       CodeConfigureRequest,
		Identifier: s.nextID(),
		Data:       EncodeOptions(options),
	}
	s.logf("-> LCP Configure-Request id=%d (%d option(s))", retry.Identifier, len(options))

	return errors.Wrapf(s.transport.WritePacket(retry.Encode()), "resending LCP Configure-Request")
}

// HandleControl processes a control packet received after negotiation.
//
// The link does not go quiet once it is up: the gateway sends Echo-Requests as
// a keepalive and drops the session if they go unanswered, and it may ask to
// terminate. Callers running a data plane feed everything that is not a
// network protocol through here.
func (s *Session) HandleControl(packet Packet) error {
	switch packet.Protocol {
	case ProtocolLCP:
		return s.handleLCP(packet)
	case ProtocolIPCP:
		return s.handleIPCP(packet)
	default:
		s.logf("  ignoring protocol 0x%04x", packet.Protocol)
		return nil
	}
}
