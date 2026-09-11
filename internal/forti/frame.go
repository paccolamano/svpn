package forti

import (
	"encoding/binary"
	"io"

	"github.com/sorintlab/errors"
)

const (
	// frameHeaderSize is the fixed 6-byte header the gateway puts in front of
	// every PPP packet on the tunnel.
	frameHeaderSize = 6
	// frameMagic marks a valid header. It is ASCII "PP".
	frameMagic = 0x5050
	// maxFrameLen guards against a corrupt length field turning into a huge
	// allocation. The gateway's own buffer is 0x1000.
	maxFrameLen = 0x2000
)

// Frame is one PPP packet carried over the tunnel.
//
// The wire layout, all big-endian, is:
//
//	0..1  total length (header + payload)
//	2..3  magic 0x5050
//	4..5  payload length
//	6..   the PPP packet
type Frame struct {
	Payload []byte
}

// ReadFrame reads a single framed PPP packet from the tunnel.
func ReadFrame(r io.Reader) (Frame, error) {
	var header [frameHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		// Wrapped, not replaced: a caller distinguishes a clean io.EOF between
		// frames from an io.ErrUnexpectedEOF in the middle of one.
		return Frame{}, errors.Wrapf(err, "reading a frame header")
	}

	total := binary.BigEndian.Uint16(header[0:2])
	magic := binary.BigEndian.Uint16(header[2:4])
	size := binary.BigEndian.Uint16(header[4:6])

	// The same checks openfortivpn makes: a desynchronised stream is not
	// recoverable, so fail loudly instead of trying to resynchronise.
	if magic != frameMagic {
		return Frame{}, errors.Errorf("bad frame magic 0x%04x, expected 0x%04x", magic, frameMagic)
	}
	if total < frameHeaderSize+1 || int(total)-frameHeaderSize != int(size) {
		return Frame{}, errors.Errorf("inconsistent frame header: total=%d payload=%d", total, size)
	}
	if int(size) > maxFrameLen {
		return Frame{}, errors.Errorf("frame payload of %d bytes exceeds the %d byte limit", size, maxFrameLen)
	}

	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Frame{}, errors.Wrapf(err, "reading a %d byte frame payload", size)
	}

	return Frame{Payload: payload}, nil
}

// WriteFrame writes one PPP packet to the tunnel with the gateway's framing.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) > maxFrameLen {
		return errors.Errorf("payload of %d bytes exceeds the %d byte limit", len(payload), maxFrameLen)
	}

	// Both conversions are bounded by the maxFrameLen check above, which is
	// well under 2^16.
	buf := make([]byte, frameHeaderSize+len(payload))
	binary.BigEndian.PutUint16(buf[0:2], uint16(frameHeaderSize+len(payload))) //nolint:gosec
	binary.BigEndian.PutUint16(buf[2:4], frameMagic)
	binary.BigEndian.PutUint16(buf[4:6], uint16(len(payload))) //nolint:gosec
	copy(buf[frameHeaderSize:], payload)

	if _, err := w.Write(buf); err != nil {
		return errors.Wrapf(err, "writing frame")
	}

	return nil
}
