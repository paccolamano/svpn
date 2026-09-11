package forti

import "testing"

func TestDecodePacket(t *testing.T) {
	tests := []struct {
		name         string
		payload      []byte
		wantProtocol string
		wantCode     string
		wantID       byte
		wantHasCode  bool
	}{
		{
			name:         "LCP configure-request with HDLC framing",
			payload:      []byte{0xff, 0x03, 0xc0, 0x21, 0x01, 0x07, 0x00, 0x04},
			wantProtocol: "LCP",
			wantCode:     "Configure-Request",
			wantID:       7,
			wantHasCode:  true,
		},
		{
			// The address and control bytes are optional on this transport, so
			// a packet that omits them must decode the same way.
			name:         "LCP configure-ack without HDLC framing",
			payload:      []byte{0xc0, 0x21, 0x02, 0x07, 0x00, 0x04},
			wantProtocol: "LCP",
			wantCode:     "Configure-Ack",
			wantID:       7,
			wantHasCode:  true,
		},
		{
			name:         "IPCP configure-nak",
			payload:      []byte{0xff, 0x03, 0x80, 0x21, 0x03, 0x01, 0x00, 0x04},
			wantProtocol: "IPCP",
			wantCode:     "Configure-Nak",
			wantID:       1,
			wantHasCode:  true,
		},
		{
			// Network protocols carry datagrams, so there is no code field to
			// read; treating byte 2 as one would report nonsense.
			name:         "IPv4 datagram carries no code",
			payload:      []byte{0xff, 0x03, 0x00, 0x21, 0x45, 0x00, 0x00, 0x28},
			wantProtocol: "IPv4",
			wantHasCode:  false,
		},
		{
			name:         "unknown protocol is reported by number",
			payload:      []byte{0xff, 0x03, 0x12, 0x34, 0x01, 0x01, 0x00, 0x04},
			wantProtocol: "0x1234",
			wantHasCode:  false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			packet, err := DecodePacket(test.payload)
			if err != nil {
				t.Fatalf("DecodePacket: %v", err)
			}

			if packet.ProtocolName != test.wantProtocol {
				t.Errorf("protocol = %q, want %q", packet.ProtocolName, test.wantProtocol)
			}
			if packet.HasCode != test.wantHasCode {
				t.Fatalf("HasCode = %v, want %v", packet.HasCode, test.wantHasCode)
			}
			if test.wantHasCode {
				if packet.CodeName != test.wantCode {
					t.Errorf("code = %q, want %q", packet.CodeName, test.wantCode)
				}
				if packet.Identifier != test.wantID {
					t.Errorf("identifier = %d, want %d", packet.Identifier, test.wantID)
				}
			}
			if packet.PayloadLen != len(test.payload) {
				t.Errorf("PayloadLen = %d, want %d", packet.PayloadLen, len(test.payload))
			}
		})
	}
}

func TestDecodePacketRejectsShortPacket(t *testing.T) {
	if _, err := DecodePacket([]byte{0xff}); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestValidateSessionID(t *testing.T) {
	valid := []string{"abc123", "abc-123-XYZ", "0"}
	for _, id := range valid {
		if err := validateSessionID(id); err != nil {
			t.Errorf("validateSessionID(%q) = %v, want nil", id, err)
		}
	}

	// The id is interpolated into a URL, so anything that could change its
	// meaning has to be refused rather than escaped away.
	invalid := []string{"", "abc 123", "abc&id=x", "../etc", "a?b", "a#b"}
	for _, id := range invalid {
		if err := validateSessionID(id); err == nil {
			t.Errorf("validateSessionID(%q) = nil, want an error", id)
		}
	}
}

func TestExtractSVPNCookie(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		want    string
	}{
		{
			name:    "plain cookie with attributes",
			headers: []string{"SVPNCOOKIE=abc123; path=/; secure; httponly"},
			want:    "SVPNCOOKIE=abc123",
		},
		{
			// net/http's parser rejects values holding bytes like these and
			// would hand back nothing; the gateway must get its own value back
			// unchanged.
			name:    "value with bytes net/http would reject",
			headers: []string{`SVPNCOOKIE=a"b c,d; path=/`},
			want:    `SVPNCOOKIE=a"b c,d`,
		},
		{
			name: "a later header supersedes an earlier one",
			headers: []string{
				"SVPNCOOKIE=first; path=/",
				"OTHER=x; path=/",
				"SVPNCOOKIE=second; path=/",
			},
			want: "SVPNCOOKIE=second",
		},
		{
			// Gateways clear the cookie by sending an empty value; that is a
			// deletion, not the session cookie.
			name: "deletion does not override a real value",
			headers: []string{
				"SVPNCOOKIE=real; path=/",
				"SVPNCOOKIE=; path=/; expires=Thu, 01 Jan 1970 00:00:00 GMT",
			},
			want: "SVPNCOOKIE=real",
		},
		{
			name:    "no cookie at all",
			headers: []string{"OTHER=x; path=/"},
			want:    "",
		},
		{
			name:    "no headers",
			headers: nil,
			want:    "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := extractSVPNCookie(test.headers); got != test.want {
				t.Errorf("extractSVPNCookie() = %q, want %q", got, test.want)
			}
		})
	}
}
