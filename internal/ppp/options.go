package ppp

import (
	"encoding/binary"
	"net"

	"github.com/sorintlab/errors"
)

// LCP option types (RFC 1661 section 6).
const (
	LCPOptionMRU            = 1
	LCPOptionACCM           = 2
	LCPOptionAuthProtocol   = 3
	LCPOptionQualityProto   = 4
	LCPOptionMagicNumber    = 5
	LCPOptionProtoFieldComp = 7
	LCPOptionAddrFieldComp  = 8
)

// IPCP option types (RFC 1332 and the widely implemented DNS extensions from
// RFC 1877).
const (
	IPCPOptionIPAddresses = 1
	IPCPOptionIPCompress  = 2
	IPCPOptionIPAddress   = 3
	IPCPOptionPrimaryDNS  = 129
	IPCPOptionPrimaryNBNS = 130
	IPCPOptionSecondDNS   = 131
	IPCPOptionSecondNBNS  = 132
)

// DefaultMRU is what pppd negotiates over this transport.
const DefaultMRU = 1492

// acceptableLCPOptions are the options this client will acknowledge when the
// peer requests them.
//
// Authentication is absent on purpose: the tunnel is already authenticated by
// the session cookie, and openfortivpn runs pppd with `noauth`. Compression is
// absent for the same reason it passes `noaccomp` and `nopcomp` — the framing
// this code reads and writes keeps both fields intact.
var acceptableLCPOptions = map[byte]bool{
	LCPOptionMRU:         true,
	LCPOptionACCM:        true,
	LCPOptionMagicNumber: true,
}

// reviewLCPOptions splits the peer's requested options into those to
// acknowledge and those to reject.
func reviewLCPOptions(options []Option) (accepted, rejected []Option) {
	for _, option := range options {
		if acceptableLCPOptions[option.Type] {
			accepted = append(accepted, option)
			continue
		}
		rejected = append(rejected, option)
	}

	return accepted, rejected
}

// mruOption builds a Maximum-Receive-Unit option.
func mruOption(mru uint16) Option {
	value := make([]byte, 2)
	binary.BigEndian.PutUint16(value, mru)

	return Option{Type: LCPOptionMRU, Value: value}
}

// magicNumberOption builds a Magic-Number option.
func magicNumberOption(magic uint32) Option {
	value := make([]byte, 4)
	binary.BigEndian.PutUint32(value, magic)

	return Option{Type: LCPOptionMagicNumber, Value: value}
}

// ipOption builds an option holding a single IPv4 address.
func ipOption(optionType byte, ip net.IP) Option {
	value := make([]byte, 4)
	if v4 := ip.To4(); v4 != nil {
		copy(value, v4)
	}

	return Option{Type: optionType, Value: value}
}

// optionIP reads the IPv4 address out of an option value.
func optionIP(option Option) (net.IP, error) {
	if len(option.Value) != 4 {
		return nil, errors.Errorf("option %d carries %d bytes, expected 4", option.Type, len(option.Value))
	}

	return net.IPv4(option.Value[0], option.Value[1], option.Value[2], option.Value[3]), nil
}

// isUnspecified reports whether an address is absent or all zeroes, which is
// how a peer is asked to supply one.
func isUnspecified(ip net.IP) bool {
	return ip == nil || ip.Equal(net.IPv4zero)
}
