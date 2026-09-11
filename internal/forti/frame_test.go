package forti

import (
	"bytes"
	"io"
	"testing"

	"github.com/sorintlab/errors"
)

func TestWriteFrameThenReadFrameRoundTrips(t *testing.T) {
	payload := []byte{0xff, 0x03, 0xc0, 0x21, 0x01, 0x01, 0x00, 0x04}

	var buf bytes.Buffer
	if err := WriteFrame(&buf, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	// The header the gateway expects, checked byte by byte rather than only
	// through the round trip: the layout comes from the wire format, so an
	// encoder and decoder that agree with each other could still both be wrong.
	want := []byte{0x00, 0x0e, 0x50, 0x50, 0x00, 0x08}
	if got := buf.Bytes()[:frameHeaderSize]; !bytes.Equal(got, want) {
		t.Errorf("header = % x, want % x", got, want)
	}

	frame, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if !bytes.Equal(frame.Payload, payload) {
		t.Errorf("payload = % x, want % x", frame.Payload, payload)
	}
}

func TestReadFrameRejectsMalformedHeaders(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
	}{
		{
			name:  "bad magic",
			input: []byte{0x00, 0x08, 0x41, 0x41, 0x00, 0x02, 0xc0, 0x21},
		},
		{
			// total says 8 (2 bytes of payload) but the size field says 4.
			name:  "total disagrees with payload size",
			input: []byte{0x00, 0x08, 0x50, 0x50, 0x00, 0x04, 0xc0, 0x21},
		},
		{
			name:  "total shorter than the header",
			input: []byte{0x00, 0x04, 0x50, 0x50, 0x00, 0x00},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ReadFrame(bytes.NewReader(test.input)); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestReadFrameReportsTruncatedStream(t *testing.T) {
	// A header promising 8 bytes followed by only 3 of them.
	input := []byte{0x00, 0x0e, 0x50, 0x50, 0x00, 0x08, 0xff, 0x03, 0xc0}

	_, err := ReadFrame(bytes.NewReader(input))
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("error = %v, want it to wrap io.ErrUnexpectedEOF", err)
	}
}

func TestReadFrameReturnsEOFOnCleanClose(t *testing.T) {
	// A closed tunnel must be distinguishable from a corrupt one, so EOF is
	// passed through unwrapped.
	if _, err := ReadFrame(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Errorf("error = %v, want io.EOF", err)
	}
}
