package ppp

import (
	"bytes"
	"testing"
)

func TestDecodeControlPacket(t *testing.T) {
	// LCP Configure-Request, id 7, length 8, carrying an MRU option.
	frame := []byte{0xff, 0x03, 0xc0, 0x21, 0x01, 0x07, 0x00, 0x08, 0x01, 0x04, 0x05, 0xd4}

	packet, err := Decode(frame)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if packet.Protocol != ProtocolLCP {
		t.Errorf("protocol = 0x%04x, want 0x%04x", packet.Protocol, ProtocolLCP)
	}
	if packet.Code != CodeConfigureRequest || packet.Identifier != 7 {
		t.Errorf("code/id = %d/%d, want %d/7", packet.Code, packet.Identifier, CodeConfigureRequest)
	}
	if want := []byte{0x01, 0x04, 0x05, 0xd4}; !bytes.Equal(packet.Data, want) {
		t.Errorf("data = % x, want % x", packet.Data, want)
	}
}

func TestDecodeAcceptsMissingAddressAndControlBytes(t *testing.T) {
	// The peer is asked not to compress these, but a frame without them must
	// still decode rather than being read as a different protocol.
	packet, err := Decode([]byte{0xc0, 0x21, 0x02, 0x01, 0x00, 0x04})
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if packet.Protocol != ProtocolLCP || packet.Code != CodeConfigureAck {
		t.Errorf("got protocol 0x%04x code %d, want LCP Configure-Ack", packet.Protocol, packet.Code)
	}
}

func TestDecodeIgnoresTrailingPadding(t *testing.T) {
	// The length field, not the frame size, bounds the options: a peer may pad
	// the frame and those bytes must not be read as an option.
	frame := []byte{0xff, 0x03, 0xc0, 0x21, 0x01, 0x01, 0x00, 0x06, 0x05, 0x02, 0xde, 0xad}

	packet, err := Decode(frame)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if want := []byte{0x05, 0x02}; !bytes.Equal(packet.Data, want) {
		t.Errorf("data = % x, want % x", packet.Data, want)
	}
}

func TestDecodeRejectsMalformedPackets(t *testing.T) {
	tests := map[string][]byte{
		"too short for a protocol": {0xff},
		"header missing":           {0xff, 0x03, 0xc0, 0x21, 0x01},
		"length below header":      {0xff, 0x03, 0xc0, 0x21, 0x01, 0x01, 0x00, 0x02},
		"length beyond frame":      {0xff, 0x03, 0xc0, 0x21, 0x01, 0x01, 0x00, 0x40},
	}

	for name, frame := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(frame); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	original := Packet{
		Protocol:   ProtocolIPCP,
		Code:       CodeConfigureRequest,
		Identifier: 3,
		Data:       EncodeOptions([]Option{{Type: IPCPOptionIPAddress, Value: []byte{10, 1, 2, 3}}}),
	}

	decoded, err := Decode(original.Encode())
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if decoded.Protocol != original.Protocol || decoded.Code != original.Code ||
		decoded.Identifier != original.Identifier || !bytes.Equal(decoded.Data, original.Data) {
		t.Errorf("round trip changed the packet: %+v -> %+v", original, decoded)
	}
}

func TestDecodeOptions(t *testing.T) {
	options, err := DecodeOptions([]byte{0x01, 0x04, 0x05, 0xd4, 0x05, 0x06, 0xde, 0xad, 0xbe, 0xef})
	if err != nil {
		t.Fatalf("DecodeOptions: %v", err)
	}

	if len(options) != 2 {
		t.Fatalf("got %d options, want 2", len(options))
	}
	if options[0].Type != LCPOptionMRU || !bytes.Equal(options[0].Value, []byte{0x05, 0xd4}) {
		t.Errorf("first option = %+v", options[0])
	}
	if options[1].Type != LCPOptionMagicNumber || len(options[1].Value) != 4 {
		t.Errorf("second option = %+v", options[1])
	}
}

func TestDecodeOptionsRejectsBadLengths(t *testing.T) {
	// A declared length below 2 would not advance the cursor and would spin
	// forever, so it must be refused rather than skipped.
	if _, err := DecodeOptions([]byte{0x01, 0x00, 0xff}); err == nil {
		t.Error("expected an error for a zero-length option, got nil")
	}
	if _, err := DecodeOptions([]byte{0x01, 0x40}); err == nil {
		t.Error("expected an error for a length past the buffer, got nil")
	}
	if _, err := DecodeOptions([]byte{0x01}); err == nil {
		t.Error("expected an error for a truncated option, got nil")
	}
}
