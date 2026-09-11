package forti

import (
	"crypto/rand"
	"encoding/binary"
)

// PPP protocol numbers and codes used when opening a session.
const (
	protocolLCP      = 0xc021
	codeConfigureReq = 0x01

	// optionMRU and optionMagicNumber are the two LCP options pppd offers
	// first (RFC 1661 §6.1 and §6.4).
	optionMRU         = 0x01
	optionMagicNumber = 0x05

	// defaultMRU matches what pppd negotiates over this transport.
	defaultMRU = 1492
)

// NewLCPConfigureRequest builds the packet that opens a PPP session.
//
// PPP is symmetric: both ends send a Configure-Request, and the gateway sends
// nothing until the client has spoken. Without this the tunnel is established
// but silent, and reads time out.
//
// The layout is:
//
//	ff 03        HDLC address and control
//	c0 21        protocol: LCP
//	01           code: Configure-Request
//	<id>         identifier, echoed in the reply
//	<len 2B>     length, from the code byte onwards
//	01 04 <mru>  option: Maximum-Receive-Unit
//	05 06 <mag>  option: Magic-Number
func NewLCPConfigureRequest(identifier byte) []byte {
	magic := make([]byte, 4)
	// A random magic number is how PPP detects a looped-back link. Failing to
	// read entropy is not worth propagating here: any value works for loop
	// detection, so fall back to a fixed one.
	if _, err := rand.Read(magic); err != nil {
		magic = []byte{0xde, 0xad, 0xbe, 0xef}
	}

	options := []byte{
		optionMRU, 4, 0, 0,
		optionMagicNumber, 6, magic[0], magic[1], magic[2], magic[3],
	}
	binary.BigEndian.PutUint16(options[2:4], defaultMRU)

	// The length covers the code, identifier, the length field itself and the
	// options — not the HDLC and protocol bytes in front of them.
	length := 4 + len(options)

	packet := make([]byte, 0, 4+length)
	packet = append(packet, 0xff, 0x03)
	packet = binary.BigEndian.AppendUint16(packet, protocolLCP)
	packet = append(packet, codeConfigureReq, identifier)
	// options is the fixed literal above, so length is a compile-time constant
	// of 14 and the conversion cannot truncate.
	packet = binary.BigEndian.AppendUint16(packet, uint16(length)) //nolint:gosec
	packet = append(packet, options...)

	return packet
}
