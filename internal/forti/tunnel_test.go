package forti

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"net"
	"strconv"
	"testing"
	"time"
)

// silentGateway accepts a TLS connection, reads the tunnel request and then
// says nothing at all, which is how a real FortiGate behaves: it waits for the
// client to speak first.
func silentGateway(t *testing.T) (Gateway, <-chan []byte) {
	t.Helper()

	certificate := selfSignedCert(t)

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	received := make(chan []byte, 1)

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// Accumulate for a short window rather than reading once: the request
		// and any packet after it arrive in separate TLS records.
		_ = conn.SetReadDeadline(time.Now().Add(800 * time.Millisecond))

		var all []byte
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			all = append(all, buf[:n]...)
			if err != nil {
				break
			}
		}
		received <- all

		// Hold the connection open so the client cannot mistake a close for a
		// response.
		time.Sleep(2 * time.Second)
	}()

	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatalf("splitting the listener address: %v", err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parsing the listener port: %v", err)
	}

	return Gateway{Host: host, Port: portNumber, Insecure: true}, received
}

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating a certificate: %v", err)
	}

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// TestOpenTunnelDoesNotWaitForTheGateway pins the property that made the
// tunnel hang twice: the gateway sends nothing until the client speaks, so
// inspecting the response while opening deadlocks the two sides. OpenTunnel
// must return without reading.
func TestOpenTunnelDoesNotWaitForTheGateway(t *testing.T) {
	gw, received := silentGateway(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)

		tunnel, _, err := OpenTunnel(ctx, gw, "SVPNCOOKIE=test", nil)
		if err != nil {
			t.Errorf("OpenTunnel: %v", err)
			return
		}
		_ = tunnel.Close()
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("OpenTunnel blocked waiting for a gateway that never speaks")
	}

	select {
	case request := <-received:
		if want := "GET /remote/sslvpn-tunnel"; len(request) < len(want) || string(request[:len(want)]) != want {
			t.Errorf("request began with %q, want %q", request, want)
		}
	case <-time.After(3 * time.Second):
		t.Error("the gateway received no request")
	}
}

// TestOpenTunnelSendsTheInitialPacket covers passive mode, where nothing else
// will speak first.
func TestOpenTunnelSendsTheInitialPacket(t *testing.T) {
	gw, received := silentGateway(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tunnel, _, err := OpenTunnel(ctx, gw, "SVPNCOOKIE=test", NewLCPConfigureRequest(1))
	if err != nil {
		t.Fatalf("OpenTunnel: %v", err)
	}
	defer func() { _ = tunnel.Close() }()

	select {
	case request := <-received:
		// The request and the framed packet may arrive together; what matters
		// is that the frame magic is on the wire.
		if !containsFrameMagic(request) {
			t.Errorf("no framed packet followed the request: % x", request)
		}
	case <-time.After(3 * time.Second):
		t.Error("the gateway received nothing")
	}
}

func containsFrameMagic(data []byte) bool {
	for i := 0; i+1 < len(data); i++ {
		if data[i] == 0x50 && data[i+1] == 0x50 {
			return true
		}
	}

	return false
}

var _ io.Reader = (*net.TCPConn)(nil)
