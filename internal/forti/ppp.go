package forti

import (
	"encoding/binary"
	"fmt"

	"github.com/sorintlab/errors"
)

// pppProtocols maps the PPP protocol field to a readable name. Only the
// protocols this tunnel is expected to carry are listed; anything else is
// reported by number.
var pppProtocols = map[uint16]string{
	0x0021: "IPv4",
	0x0057: "IPv6",
	0x8021: "IPCP",
	0x8057: "IPv6CP",
	0xc021: "LCP",
	0xc023: "PAP",
	0xc223: "CHAP",
}

// pppCodes maps the control-protocol code field, shared by LCP and the various
// NCPs (RFC 1661 §5).
var pppCodes = map[byte]string{
	1:  "Configure-Request",
	2:  "Configure-Ack",
	3:  "Configure-Nak",
	4:  "Configure-Reject",
	5:  "Terminate-Request",
	6:  "Terminate-Ack",
	7:  "Code-Reject",
	8:  "Protocol-Reject",
	9:  "Echo-Request",
	10: "Echo-Reply",
	11: "Discard-Request",
}

// Packet is a decoded view of a PPP packet, enough to tell what stage the
// negotiation has reached without implementing the protocol.
type Packet struct {
	Protocol     uint16
	ProtocolName string
	// Code and Identifier are only meaningful for control protocols such as
	// LCP and IPCP; HasCode reports whether they were present.
	HasCode    bool
	Code       byte
	CodeName   string
	Identifier byte
	PayloadLen int
}

// String renders the packet as a single log line.
func (p Packet) String() string {
	if p.HasCode {
		return fmt.Sprintf("%-6s %-18s id=%-3d %d bytes", p.ProtocolName, p.CodeName, p.Identifier, p.PayloadLen)
	}
	return fmt.Sprintf("%-6s %-18s %s %d bytes", p.ProtocolName, "", "", p.PayloadLen)
}

// DecodePacket inspects a PPP packet far enough to name it.
//
// This is deliberately not a PPP implementation: it reads the protocol field,
// and for control protocols the code and identifier, so the tunnel can be
// observed working before any negotiation logic exists.
func DecodePacket(payload []byte) (Packet, error) {
	body := payload

	// An HDLC-framed packet starts with the all-stations address and the
	// unnumbered-information control byte. They carry no information here, so
	// skip them when present.
	if len(body) >= 2 && body[0] == 0xff && body[1] == 0x03 {
		body = body[2:]
	}

	if len(body) < 2 {
		return Packet{}, errors.Errorf("packet of %d bytes is too short to hold a protocol field", len(payload))
	}

	protocol := binary.BigEndian.Uint16(body[0:2])
	name, known := pppProtocols[protocol]
	if !known {
		name = fmt.Sprintf("0x%04x", protocol)
	}

	packet := Packet{
		Protocol:     protocol,
		ProtocolName: name,
		PayloadLen:   len(payload),
	}

	// Control protocols (the 0x8000-0xffff range) carry a code and identifier;
	// network protocols such as IPv4 carry raw datagrams instead.
	if protocol >= 0x8000 && len(body) >= 4 {
		packet.HasCode = true
		packet.Code = body[2]
		packet.Identifier = body[3]
		if codeName, ok := pppCodes[packet.Code]; ok {
			packet.CodeName = codeName
		} else {
			packet.CodeName = fmt.Sprintf("code-%d", packet.Code)
		}
	}

	return packet, nil
}
