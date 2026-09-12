package auth

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/paccolamano/svpn/internal/sorint"
)

// TestLoginReturnsTheGatewayWithTheCookie drives the whole flow against a fake
// gateway. The pairing is the point: a cookie is only valid for the gateway
// that issued it, so a Session that carried the cookie alone would let a caller
// authenticate against one gateway and connect to another.
func TestLoginReturnsTheGatewayWithTheCookie(t *testing.T) {
	const sessionID = "test-session-id"

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/remote/saml/auth_id" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if got := r.URL.Query().Get("id"); got != sessionID {
			http.Error(w, "unexpected id "+got, http.StatusBadRequest)
			return
		}

		w.Header().Add("Set-Cookie", "SVPNCOOKIE=the-credential; path=/; secure; httponly")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	host, port := addressOf(t, server)
	samlPort := freePort(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	session, err := Login(ctx, Options{
		Host:     host,
		Port:     port,
		SAMLPort: samlPort,
		Insecure: true,
		// Stand in for the browser: the identity provider's only job here is
		// to redirect back to the loopback listener with an id.
		OpenBrowser: callBackWith(t, samlPort, sessionID),
	})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	if session.Cookie != "SVPNCOOKIE=the-credential" {
		t.Errorf("cookie = %q, want %q", session.Cookie, "SVPNCOOKIE=the-credential")
	}
	if session.Host != host || session.Port != port {
		t.Errorf("gateway = %s:%d, want %s:%d", session.Host, session.Port, host, port)
	}
}

// TestLoginReportsTheAuthURL covers what a GUI shows while it waits. The realm
// has to survive into the query string, or a gateway that uses one sends the
// user to the wrong identity provider.
func TestLoginReportsTheAuthURL(t *testing.T) {
	var reported string

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, _ = Login(ctx, Options{
		Host:      "gateway.example.com",
		Port:      8443,
		Realm:     "staff",
		SAMLPort:  freePort(t),
		NoBrowser: true,
		OnURL:     func(url string) { reported = url },
	})

	const want = "https://gateway.example.com:8443/remote/saml/start?redirect=1&realm=staff"
	if reported != want {
		t.Errorf("reported URL = %q, want %q", reported, want)
	}
}

// TestNoBrowserOpensNothing protects the option a headless caller relies on:
// it must report the URL and then wait, never launch anything.
func TestNoBrowserOpensNothing(t *testing.T) {
	opened := false

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	_, err := Login(ctx, Options{
		Host:        "gateway.example.com",
		SAMLPort:    freePort(t),
		NoBrowser:   true,
		OpenBrowser: func(string) error { opened = true; return nil },
	})

	if opened {
		t.Error("NoBrowser still opened a browser")
	}
	if err == nil {
		t.Error("expected the login to fail once the context expired")
	}
}

// TestLoginAbandonsAWalkedAwayUser is the cancellation path a GUI needs: the
// user closes the dialog and the login must stop rather than hold the loopback
// port until its deadline.
func TestLoginAbandonsAWalkedAwayUser(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	samlPort := freePort(t)
	done := make(chan error, 1)

	go func() {
		_, err := Login(ctx, Options{
			Host:      "gateway.example.com",
			SAMLPort:  samlPort,
			NoBrowser: true,
		})
		done <- err
	}()

	// Give the listener time to bind before pulling the context out from
	// under it, so the test exercises the wait and not the setup.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Login did not return after its context was cancelled")
	}

	// The port has to be free again, or a second attempt fails to bind.
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(samlPort)))
	if err != nil {
		t.Fatalf("callback port still held after cancellation: %v", err)
	}
	_ = listener.Close()
}

func TestDefaultsAreTheSorintGateway(t *testing.T) {
	got := Options{}.withDefaults()

	if got.Host != sorint.Host {
		t.Errorf("host = %q, want %q", got.Host, sorint.Host)
	}
	if got.Port != sorint.Port {
		t.Errorf("port = %d, want %d", got.Port, sorint.Port)
	}
	if got.SAMLPort != sorint.SAMLPort {
		t.Errorf("saml port = %d, want %d", got.SAMLPort, sorint.SAMLPort)
	}
	if got.OpenBrowser == nil {
		t.Error("expected a default browser opener")
	}
}

func TestDefaultsLeaveExplicitOptionsAlone(t *testing.T) {
	got := Options{Host: "other.example.com", Port: 8443, SAMLPort: 9999}.withDefaults()

	if got.Host != "other.example.com" || got.Port != 8443 || got.SAMLPort != 9999 {
		t.Errorf("defaults overwrote explicit options: %+v", got)
	}
}

// callBackWith returns a browser stand-in that performs the redirect the
// identity provider would.
func callBackWith(t *testing.T, samlPort int, sessionID string) func(string) error {
	t.Helper()

	return func(authURL string) error {
		if !strings.Contains(authURL, "/remote/saml/start") {
			t.Errorf("browser opened %q, which is not the SAML start URL", authURL)
		}

		go func() {
			callback := "http://127.0.0.1:" + strconv.Itoa(samlPort) + "/?id=" + sessionID
			response, err := http.Get(callback) //nolint:noctx
			if err != nil {
				t.Errorf("calling back: %v", err)
				return
			}
			_ = response.Body.Close()
		}()

		return nil
	}
}

func addressOf(t *testing.T, server *httptest.Server) (string, int) {
	t.Helper()

	host, port, err := net.SplitHostPort(server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("splitting the server address: %v", err)
	}

	number, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parsing the port: %v", err)
	}

	return host, number
}

// freePort reserves a port and gives it straight back, so the login can bind
// it. Tests must not share the real callback port: it is a fixed number, and
// two of them running at once would collide.
func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer func() { _ = listener.Close() }()

	return listener.Addr().(*net.TCPAddr).Port
}
