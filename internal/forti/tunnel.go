package forti

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/sorintlab/errors"
)

// Tunnel is an open PPP-over-TLS session with the gateway.
type Tunnel struct {
	conn   *tls.Conn
	reader *bufio.Reader
	gw     Gateway

	// headerChecked guards the one-shot inspection of the response head. It
	// cannot happen while opening: reading there would block until the peer
	// speaks, and the peer waits for the client.
	headerChecked bool
}

// TunnelInfo describes the connection that was established.
type TunnelInfo struct {
	// PeerCertSHA256 is the digest of the gateway's leaf certificate, in the
	// same form as openfortivpn's `trusted-cert` option.
	PeerCertSHA256 string
	TLSVersion     string
}

// OpenTunnel authenticates with cookie and opens the packet stream.
//
// cookie is the full "SVPNCOOKIE=<value>" pair as returned by SAMLLogin.
//
// initialPacket, when not empty, is sent as soon as the tunnel request is on
// the wire. The gateway stays silent until the client speaks, so opening
// without one leaves the connection established but idle.
func OpenTunnel(ctx context.Context, gw Gateway, cookie string, initialPacket []byte) (*Tunnel, TunnelInfo, error) {
	dialer := &tls.Dialer{Config: gw.tlsConfig()}

	conn, err := dialer.DialContext(ctx, "tcp", gw.Addr())
	if err != nil {
		return nil, TunnelInfo{}, errors.Wrapf(err, "connecting to %s", gw.Addr())
	}

	tlsConn, ok := conn.(*tls.Conn)
	if !ok {
		_ = conn.Close()
		return nil, TunnelInfo{}, errors.Errorf("expected a TLS connection, got %T", conn)
	}

	info := TunnelInfo{TLSVersion: tlsVersionName(tlsConn.ConnectionState().Version)}
	if certs := tlsConn.ConnectionState().PeerCertificates; len(certs) > 0 {
		digest := sha256.Sum256(certs[0].Raw)
		info.PeerCertSHA256 = hex.EncodeToString(digest[:])
	}

	// The Host header is the literal "sslvpn" rather than the gateway name:
	// that is what the gateway expects here, and what openfortivpn sends.
	request := fmt.Sprintf(
		"GET /remote/sslvpn-tunnel HTTP/1.1\r\nHost: sslvpn\r\nCookie: %s\r\n\r\n",
		cookie,
	)

	if deadline, ok := ctx.Deadline(); ok {
		_ = tlsConn.SetDeadline(deadline)
	}

	if _, err := io.WriteString(tlsConn, request); err != nil {
		_ = tlsConn.Close()
		return nil, info, errors.Wrapf(err, "sending the tunnel request")
	}

	// Sent before reading anything: the gateway will not talk first, so
	// waiting for it here would simply time out.
	if len(initialPacket) > 0 {
		if err := WriteFrame(tlsConn, initialPacket); err != nil {
			_ = tlsConn.Close()
			return nil, info, errors.Wrapf(err, "sending the initial packet")
		}
		gw.debugf("sent initial packet, %d bytes", len(initialPacket))
	}

	_ = tlsConn.SetDeadline(time.Time{})

	// The response head is inspected on the first read, not here. This gateway
	// sends nothing until the client has spoken, so reading now would block
	// until the deadline expired.
	return &Tunnel{
		conn:   tlsConn,
		reader: bufio.NewReaderSize(tlsConn, 8192),
		gw:     gw,
	}, info, nil
}

// consumeHTTPResponse reads and discards an HTTP response head if one is
// present, returning its status line. It returns an empty status when the
// stream begins with framed packets instead.
func consumeHTTPResponse(reader *bufio.Reader) (string, error) {
	prefix, err := reader.Peek(5)
	if err != nil {
		return "", errors.Wrapf(err, "reading the tunnel response")
	}

	if string(prefix) != "HTTP/" {
		return "", nil
	}

	var status string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return "", errors.Wrapf(err, "reading the tunnel response header")
		}

		line = strings.TrimRight(line, "\r\n")
		if status == "" {
			status = line
		}
		if line == "" {
			break
		}
	}

	if !strings.Contains(status, " 200") {
		return status, errors.Errorf("gateway refused the tunnel: %s", status)
	}

	return status, nil
}

// ReadPacket returns the next PPP packet from the tunnel.
func (t *Tunnel) ReadPacket() ([]byte, error) {
	if !t.headerChecked {
		// Some gateways answer with an HTTP status line before switching to
		// the packet stream and some start framing straight away, so peek
		// rather than assume: consuming a head that is not there would eat the
		// first frame.
		status, err := consumeHTTPResponse(t.reader)
		if err != nil {
			return nil, err
		}

		t.headerChecked = true

		if status != "" {
			t.gw.debugf("tunnel response: %s", status)
		} else {
			t.gw.debugf("tunnel began framing with no HTTP response")
		}
	}

	frame, err := ReadFrame(t.reader)
	if err != nil {
		return nil, err
	}

	return frame.Payload, nil
}

// WritePacket sends a PPP packet through the tunnel.
func (t *Tunnel) WritePacket(payload []byte) error {
	return WriteFrame(t.conn, payload)
}

// SetReadDeadline bounds how long ReadPacket will block.
func (t *Tunnel) SetReadDeadline(deadline time.Time) error {
	return errors.Wrapf(t.conn.SetReadDeadline(deadline), "setting the tunnel read deadline")
}

// LocalAddr reports the local endpoint of the tunnel.
func (t *Tunnel) LocalAddr() net.Addr { return t.conn.LocalAddr() }

// Close shuts the tunnel down.
func (t *Tunnel) Close() error { return errors.Wrapf(t.conn.Close(), "closing the tunnel") }

func tlsVersionName(version uint16) string {
	switch version {
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("0x%04x", version)
	}
}
