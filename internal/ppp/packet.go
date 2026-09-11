// Package ppp implements the client half of the PPP negotiation needed to
// bring up a link: LCP (RFC 1661) and IPCP (RFC 1332).
//
// It is deliberately independent of how frames are carried, so the FortiGate
// tunnel is just one possible Transport.
package ppp

import (
	"encoding/binary"

	"github.com/sorintlab/errors"
)

// Protocol numbers carried in the PPP header.
const (
	ProtocolIPv4 = 0x0021
	ProtocolIPCP = 0x8021
	ProtocolLCP  = 0xc021
)

// Control protocol codes, shared by LCP and IPCP (RFC 1661 section 5).
const (
	CodeConfigureRequest = 1
	CodeConfigureAck     = 2
	CodeConfigureNak     = 3
	CodeConfigureReject  = 4
	CodeTerminateRequest = 5
	CodeTerminateAck     = 6
	CodeCodeReject       = 7
	CodeProtocolReject   = 8
	CodeEchoRequest      = 9
	CodeEchoReply        = 10
	CodeDiscardRequest   = 11
)

// headerLen is the size of a control packet header: code, identifier and the
// two-byte length.
const headerLen = 4

// Packet is a PPP control packet.
type Packet struct {
	Protocol   uint16
	Code       byte
	Identifier byte
	// Data is everything after the header: options for Configure-* packets,
	// opaque payload otherwise.
	Data []byte
}

// Decode parses a PPP control packet.
//
// The address and control bytes are optional on this link — the peer is asked
// not to compress them, but accepting either form costs nothing.
func Decode(frame []byte) (Packet, error) {
	body := frame
	if len(body) >= 2 && body[0] == 0xff && body[1] == 0x03 {
		body = body[2:]
	}

	if len(body) < 2 {
		return Packet{}, errors.Errorf("frame of %d bytes is too short for a protocol field", len(frame))
	}

	packet := Packet{Protocol: binary.BigEndian.Uint16(body[:2])}
	body = body[2:]

	// Network protocols carry datagrams rather than control packets, so there
	// is no header to read.
	if packet.Protocol < 0x8000 {
		packet.Data = body
		return packet, nil
	}

	if len(body) < headerLen {
		return Packet{}, errors.Errorf("control packet of %d bytes is shorter than its header", len(body))
	}

	packet.Code = body[0]
	packet.Identifier = body[1]

	length := int(binary.BigEndian.Uint16(body[2:4]))
	if length < headerLen {
		return Packet{}, errors.Errorf("declared length %d is shorter than the header", length)
	}
	if length > len(body) {
		return Packet{}, errors.Errorf("declared length %d exceeds the %d bytes available", length, len(body))
	}

	packet.Data = body[headerLen:length]

	return packet, nil
}

// Encode renders the packet as a frame, including the address and control
// bytes the peer expects when compression is disabled.
func (p Packet) Encode() []byte {
	frame := make([]byte, 0, 8+len(p.Data))
	frame = append(frame, 0xff, 0x03)
	frame = binary.BigEndian.AppendUint16(frame, p.Protocol)

	if p.Protocol < 0x8000 {
		return append(frame, p.Data...)
	}

	frame = append(frame, p.Code, p.Identifier)
	// A PPP length field is 16 bits, so Data can never legitimately be larger:
	// decoded packets were bounded by that same field, and the ones built here
	// carry a handful of options.
	frame = binary.BigEndian.AppendUint16(frame, uint16(headerLen+len(p.Data))) //nolint:gosec

	return append(frame, p.Data...)
}

// Option is one Configure-Request option: a type byte and its value.
type Option struct {
	Type  byte
	Value []byte
}

// DecodeOptions parses the option list of a Configure-* packet.
func DecodeOptions(data []byte) ([]Option, error) {
	var options []Option

	for len(data) > 0 {
		if len(data) < 2 {
			return nil, errors.Errorf("option list ends after %d stray byte(s)", len(data))
		}

		// The length covers the type and length bytes as well as the value, so
		// anything below 2 would not advance and would loop forever.
		length := int(data[1])
		if length < 2 {
			return nil, errors.Errorf("option %d declares length %d", data[0], length)
		}
		if length > len(data) {
			return nil, errors.Errorf("option %d declares length %d but only %d bytes remain", data[0], length, len(data))
		}

		options = append(options, Option{Type: data[0], Value: data[2:length]})
		data = data[length:]
	}

	return options, nil
}

// EncodeOptions renders an option list.
func EncodeOptions(options []Option) []byte {
	var data []byte
	for _, option := range options {
		// A PPP option length is one byte, so a value cannot exceed 253. Every
		// option reaching here respects that: the locally built ones are a
		// handful of bytes, and the ones echoed back in a Configure-Ack or
		// Configure-Reject came through DecodeOptions, which read their length
		// from that same single byte.
		data = append(data, option.Type, byte(2+len(option.Value))) //nolint:gosec
		data = append(data, option.Value...)
	}

	return data
}
