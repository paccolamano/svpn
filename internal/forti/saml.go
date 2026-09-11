package forti

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sorintlab/errors"
)

// maxSAMLSessionIDLength bounds the id accepted from the browser callback.
// The value mirrors openfortivpn's own limit.
const maxSAMLSessionIDLength = 1024

// callbackPage is returned to the browser once the session id is captured.
// The window closes itself so the user is not left with a stray tab.
const callbackPage = `<!DOCTYPE html>
<html><body>
<p>Authentication complete. You can close this window.</p>
<script>window.setTimeout(() => { window.close(); }, 5000);</script>
</body></html>
`

// SAMLLogin performs the browser-based SAML login and returns the SVPNCOOKIE.
//
// The flow has three steps:
//
//  1. the user authenticates at /remote/saml/start on the gateway, which
//     redirects to the corporate identity provider;
//  2. the identity provider redirects back to a listener this process runs on
//     loopback, as GET /?id=<session-id>;
//  3. that id is exchanged for the session cookie at /remote/saml/auth_id.
//
// openBrowser is called with the authentication URL; pass nil to only report it
// through onURL and let the user open it manually.
func SAMLLogin(ctx context.Context, gw Gateway, listenPort int, openBrowser func(string) error, onURL func(string)) (string, error) {
	// Bind before sending the user to the identity provider: if the port is
	// taken, failing now is clearer than after they have authenticated.
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", fmt.Sprint(listenPort)))
	if err != nil {
		return "", errors.Wrapf(err, "listening for the SAML callback on port %d", listenPort)
	}
	defer func() { _ = listener.Close() }()

	authURL := gw.BaseURL() + "/remote/saml/start?redirect=1"
	if gw.Realm != "" {
		authURL += "&realm=" + url.QueryEscape(gw.Realm)
	}

	if onURL != nil {
		onURL(authURL)
	}

	if openBrowser != nil {
		if err := openBrowser(authURL); err != nil {
			// Not fatal: the URL has already been reported, so the user can
			// still open it by hand.
			return "", errors.Wrapf(err, "opening the browser (open the URL manually)")
		}
	}

	sessionID, err := waitForCallback(ctx, listener)
	if err != nil {
		return "", err
	}

	return exchangeSessionID(ctx, gw, sessionID)
}

// waitForCallback serves the loopback listener until the identity provider
// redirects the browser to it, and returns the session id it carried.
func waitForCallback(ctx context.Context, listener net.Listener) (string, error) {
	type result struct {
		id  string
		err error
	}
	results := make(chan result, 1)

	server := &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.URL.Query().Get("id")
			if err := validateSessionID(id); err != nil {
				http.Error(w, "invalid session id", http.StatusBadRequest)
				select {
				case results <- result{err: err}:
				default:
				}
				return
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Connection", "close")
			_, _ = w.Write([]byte(callbackPage))

			select {
			case results <- result{id: id}:
			default:
			}
		}),
	}

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case results <- result{err: err}:
			default:
			}
		}
	}()

	defer func() {
		// This runs precisely when ctx has been cancelled, so the shutdown
		// needs a deadline of its own or it would give up immediately.
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	select {
	case <-ctx.Done():
		return "", errors.Wrapf(ctx.Err(), "waiting for the SAML callback")
	case res := <-results:
		if res.err != nil {
			return "", errors.Wrapf(res.err, "receiving the SAML callback")
		}
		return res.id, nil
	}
}

// validateSessionID rejects ids that are empty, over-long, or contain anything
// but alphanumerics and hyphens. The id is interpolated into a URL, so it is
// checked rather than trusted.
func validateSessionID(id string) error {
	if id == "" {
		return errors.New("the callback carried no id parameter")
	}

	if len(id) > maxSAMLSessionIDLength {
		return errors.Errorf("session id is %d bytes, over the %d byte limit", len(id), maxSAMLSessionIDLength)
	}

	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
		default:
			return errors.Errorf("session id contains an unexpected character %q", r)
		}
	}

	return nil
}

// exchangeSessionID trades the id from the browser callback for the SVPNCOOKIE
// that authenticates the tunnel.
func exchangeSessionID(ctx context.Context, gw Gateway, sessionID string) (string, error) {
	endpoint := fmt.Sprintf("%s/remote/saml/auth_id?id=%s", gw.BaseURL(), url.QueryEscape(sessionID))

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", errors.Wrapf(err, "building the auth_id request")
	}

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: gw.tlsConfig()},
		// The cookie arrives on the first response; following a redirect would
		// discard it.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	gw.debugf("GET /remote/saml/auth_id?id=%s", sessionID)

	response, err := client.Do(request)
	if err != nil {
		return "", errors.Wrapf(err, "requesting the session cookie")
	}
	defer func() { _ = response.Body.Close() }()

	setCookies := response.Header.Values("Set-Cookie")

	gw.debugf("  -> HTTP %d (%s)", response.StatusCode, response.Proto)
	for _, header := range setCookies {
		gw.debugf("  -> Set-Cookie: %s", header)
	}

	cookie := extractSVPNCookie(setCookies)
	if cookie == "" {
		return "", errors.Errorf(
			"no SVPNCOOKIE in the auth_id response (HTTP %d, %d Set-Cookie header(s): %q)",
			response.StatusCode,
			len(setCookies),
			strings.Join(setCookies, " | "),
		)
	}

	gw.debugf("  session cookie: %s", cookie)

	return cookie, nil
}

// extractSVPNCookie pulls the session cookie out of the Set-Cookie headers.
//
// The value is taken verbatim, the way openfortivpn does, rather than through
// net/http's cookie parser: that parser drops or rewrites values containing
// bytes it considers invalid, and the gateway must receive back exactly what
// it issued.
//
// A gateway may send several Set-Cookie headers and clear the cookie in one of
// them, so the last non-empty value wins.
func extractSVPNCookie(setCookies []string) string {
	const name = "SVPNCOOKIE="

	var cookie string
	for _, header := range setCookies {
		index := strings.Index(header, name)
		if index < 0 {
			continue
		}

		value := header[index+len(name):]
		if end := strings.IndexByte(value, ';'); end >= 0 {
			value = value[:end]
		}
		value = strings.TrimSpace(value)

		// An empty value is the gateway deleting the cookie, not issuing one.
		if value != "" {
			cookie = name + value
		}
	}

	return cookie
}
