package forti

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestNewLCPConfigureRequest(t *testing.T) {
	packet := NewLCPConfigureRequest(7)

	// The fixed prefix: HDLC address and control, the LCP protocol number,
	// Configure-Request, the identifier, and the length.
	wantPrefix := []byte{0xff, 0x03, 0xc0, 0x21, 0x01, 0x07, 0x00, 0x0e}
	if got := packet[:len(wantPrefix)]; !bytes.Equal(got, wantPrefix) {
		t.Fatalf("prefix = % x, want % x", got, wantPrefix)
	}

	// The length field counts from the code byte onwards, so it excludes the
	// four bytes of HDLC and protocol in front of it.
	length := binary.BigEndian.Uint16(packet[6:8])
	if int(length) != len(packet)-4 {
		t.Errorf("length field = %d, want %d", length, len(packet)-4)
	}

	// MRU option: type 1, length 4, value 1492.
	if packet[8] != optionMRU || packet[9] != 4 {
		t.Errorf("MRU option header = % x, want 01 04", packet[8:10])
	}
	if mru := binary.BigEndian.Uint16(packet[10:12]); mru != defaultMRU {
		t.Errorf("MRU = %d, want %d", mru, defaultMRU)
	}

	// Magic-Number option: type 5, length 6.
	if packet[12] != optionMagicNumber || packet[13] != 6 {
		t.Errorf("magic option header = % x, want 05 06", packet[12:14])
	}
}

func TestNewLCPConfigureRequestDecodesAsItself(t *testing.T) {
	// The builder and the decoder are written from the same spec, so checking
	// they agree catches a mistake in either.
	decoded, err := DecodePacket(NewLCPConfigureRequest(42))
	if err != nil {
		t.Fatalf("DecodePacket: %v", err)
	}

	if decoded.ProtocolName != "LCP" {
		t.Errorf("protocol = %q, want LCP", decoded.ProtocolName)
	}
	if decoded.CodeName != "Configure-Request" {
		t.Errorf("code = %q, want Configure-Request", decoded.CodeName)
	}
	if decoded.Identifier != 42 {
		t.Errorf("identifier = %d, want 42", decoded.Identifier)
	}
}

func TestNewLCPConfigureRequestUsesFreshMagicNumbers(t *testing.T) {
	// A repeated magic number would make PPP mistake a fresh link for a loop.
	first := NewLCPConfigureRequest(1)[14:18]
	second := NewLCPConfigureRequest(1)[14:18]

	if bytes.Equal(first, second) {
		t.Errorf("magic number repeated across calls: % x", first)
	}
}

func TestLCPConfigureRequestSurvivesFraming(t *testing.T) {
	packet := NewLCPConfigureRequest(3)

	var buf bytes.Buffer
	if err := WriteFrame(&buf, packet); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(frame.Payload, packet) {
		t.Errorf("payload = % x, want % x", frame.Payload, packet)
	}
}
