package ppp

import (
	"fmt"
	"net"

	"github.com/sorintlab/errors"
)

// sendIPCPConfigureRequest asks the peer for an address and DNS servers.
//
// The addresses are sent unspecified on the first attempt, which is how a peer
// is invited to assign them: it answers with a Configure-Nak carrying the real
// values, and those are then requested verbatim. This mirrors pppd's
// `noipdefault`, `ipcp-accept-local` and `usepeerdns`.
func (s *Session) sendIPCPConfigureRequest() error {
	options := []Option{
		ipOption(IPCPOptionIPAddress, s.requested.LocalIP),
		ipOption(IPCPOptionPrimaryDNS, s.requested.PrimaryDNS),
		ipOption(IPCPOptionSecondDNS, s.requested.SecondDNS),
	}

	packet := Packet{
		Protocol:   ProtocolIPCP,
		Code:       CodeConfigureRequest,
		Identifier: s.nextID(),
		Data:       EncodeOptions(options),
	}

	s.logf("-> IPCP Configure-Request id=%d (address %s)", packet.Identifier, describeIP(s.requested.LocalIP))
	s.ipcpRequestSent = true

	return errors.Wrapf(s.transport.WritePacket(packet.Encode()), "sending IPCP Configure-Request")
}

func (s *Session) handleIPCP(packet Packet) error {
	switch packet.Code {
	case CodeConfigureRequest:
		return s.answerIPCPConfigureRequest(packet)

	case CodeConfigureAck:
		options, err := DecodeOptions(packet.Data)
		if err != nil {
			return errors.Wrapf(err, "parsing the acknowledged IPCP options")
		}

		// The acknowledgement echoes the options, so read the final values
		// from it rather than assuming the peer agreed to exactly what was
		// asked for.
		if err := s.adoptIPCPOptions(options, &s.negotiated); err != nil {
			return err
		}

		s.logf("<- IPCP Configure-Ack id=%d (address %s)", packet.Identifier, describeIP(s.negotiated.LocalIP))
		s.ipcpLocalDone = true

		return nil

	case CodeConfigureNak:
		options, err := DecodeOptions(packet.Data)
		if err != nil {
			return errors.Wrapf(err, "parsing the suggested IPCP options")
		}

		// A Nak carries the values the peer wants us to use, so take them and
		// ask again.
		if err := s.adoptIPCPOptions(options, &s.requested); err != nil {
			return err
		}

		s.logf("<- IPCP Configure-Nak id=%d, the gateway suggests %s", packet.Identifier, describeIP(s.requested.LocalIP))

		return s.sendIPCPConfigureRequest()

	case CodeConfigureReject:
		options, err := DecodeOptions(packet.Data)
		if err != nil {
			return errors.Wrapf(err, "parsing the rejected IPCP options")
		}

		// Rejected options must not be offered again. Only the DNS options are
		// droppable; without an address there is nothing to negotiate.
		for _, option := range options {
			switch option.Type {
			case IPCPOptionPrimaryDNS:
				s.requested.PrimaryDNS = nil
			case IPCPOptionSecondDNS:
				s.requested.SecondDNS = nil
			case IPCPOptionIPAddress:
				return errors.Errorf("the gateway rejected the IP-Address option, leaving no address to negotiate")
			}
		}

		s.logf("<- IPCP Configure-Reject id=%d (%d option(s))", packet.Identifier, len(options))

		return s.sendIPCPConfigureRequest()

	case CodeTerminateRequest:
		s.logf("<- IPCP Terminate-Request id=%d", packet.Identifier)
		ack := Packet{Protocol: ProtocolIPCP, Code: CodeTerminateAck, Identifier: packet.Identifier}
		return errors.Wrapf(s.transport.WritePacket(ack.Encode()), "sending IPCP Terminate-Ack")

	default:
		s.logf("<- IPCP %s id=%d, ignored", codeName(packet.Code), packet.Identifier)
		return nil
	}
}

// answerIPCPConfigureRequest acknowledges the peer's own address.
func (s *Session) answerIPCPConfigureRequest(packet Packet) error {
	options, err := DecodeOptions(packet.Data)
	if err != nil {
		return errors.Wrapf(err, "parsing the peer's IPCP options")
	}

	for _, option := range options {
		if option.Type != IPCPOptionIPAddress {
			continue
		}
		ip, err := optionIP(option)
		if err != nil {
			return err
		}
		s.negotiated.RemoteIP = ip
	}

	s.logf("<- IPCP Configure-Request id=%d (gateway address %s)", packet.Identifier, describeIP(s.negotiated.RemoteIP))

	reply := Packet{
		Protocol:   ProtocolIPCP,
		Code:       CodeConfigureAck,
		Identifier: packet.Identifier,
		Data:       packet.Data,
	}
	s.logf("-> IPCP Configure-Ack id=%d", packet.Identifier)

	if err := s.transport.WritePacket(reply.Encode()); err != nil {
		return errors.Wrapf(err, "sending IPCP Configure-Ack")
	}

	s.ipcpRemoteDone = true

	return nil
}

// adoptIPCPOptions copies the addresses carried by an option list into target.
func (s *Session) adoptIPCPOptions(options []Option, target *Config) error {
	for _, option := range options {
		switch option.Type {
		case IPCPOptionIPAddress:
			ip, err := optionIP(option)
			if err != nil {
				return err
			}
			target.LocalIP = ip
		case IPCPOptionPrimaryDNS:
			ip, err := optionIP(option)
			if err != nil {
				return err
			}
			target.PrimaryDNS = ip
		case IPCPOptionSecondDNS:
			ip, err := optionIP(option)
			if err != nil {
				return err
			}
			target.SecondDNS = ip
		}
	}

	return nil
}

// describeIP renders an address for logging, naming the unspecified case.
func describeIP(ip net.IP) string {
	if isUnspecified(ip) {
		return "unassigned"
	}

	return ip.String()
}

// codeName renders a control-protocol code for logging.
func codeName(code byte) string {
	names := map[byte]string{
		CodeConfigureRequest: "Configure-Request",
		CodeConfigureAck:     "Configure-Ack",
		CodeConfigureNak:     "Nak",
		CodeConfigureReject:  "Reject",
		CodeTerminateRequest: "Terminate-Request",
		CodeTerminateAck:     "Terminate-Ack",
		CodeCodeReject:       "Code-Reject",
		CodeProtocolReject:   "Protocol-Reject",
		CodeEchoRequest:      "Echo-Request",
		CodeEchoReply:        "Echo-Reply",
		CodeDiscardRequest:   "Discard-Request",
	}

	if name, ok := names[code]; ok {
		return name
	}

	return fmt.Sprintf("code-%d", code)
}
