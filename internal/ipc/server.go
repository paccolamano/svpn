package ipc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/sorintlab/errors"
)

// Handler serves one request. Implementations must be safe for concurrent use.
type Handler interface {
	Handle(ctx context.Context, peer Peer, request Request) Response
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(ctx context.Context, peer Peer, request Request) Response

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, peer Peer, request Request) Response {
	return f(ctx, peer, request)
}

// Peer identifies the process on the other end of a connection.
type Peer struct {
	UID uint32
	GID uint32
	PID int32
	// Known reports whether the platform supplied credentials. Policy that
	// depends on the identity must refuse when it did not.
	Known bool
}

// requestTimeout bounds how long one command may take. Connecting involves a
// full PPP negotiation, so it is generous.
const requestTimeout = 90 * time.Second

// Server accepts client connections and dispatches their requests.
type Server struct {
	handler Handler
	// Authorize decides whether a peer may issue commands. A nil value accepts
	// every peer that could open the socket, leaving access control to the
	// socket's own permissions.
	Authorize func(Peer) error
	// Logf receives operational messages; nil silences them.
	Logf func(format string, args ...any)

	wg sync.WaitGroup
}

// NewServer returns a server dispatching to handler.
func NewServer(handler Handler) *Server {
	return &Server{handler: handler}
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Serve accepts connections until ctx is done or the listener fails.
//
// It returns once every in-flight connection has finished, so a caller that
// closes the listener on shutdown can rely on nothing still touching the VPN.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	defer s.wg.Wait()

	// Unblock Accept on cancellation: a listener has no context-aware accept.
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return errors.Wrapf(err, "accepting a client")
		}

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			defer func() { _ = conn.Close() }()

			s.serveConn(ctx, conn)
		}()
	}
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	peer, err := peerCredentials(conn)
	if err != nil {
		s.logf("could not read the peer credentials: %v", err)
	}

	if s.Authorize != nil {
		if err := s.Authorize(peer); err != nil {
			s.logf("refused uid=%d pid=%d: %v", peer.UID, peer.PID, err)
			_ = writeResponse(conn, Response{Error: err.Error()})
			return
		}
	}

	scanner := bufio.NewScanner(conn)
	// Requests are small; a large one is a client bug or an attempt to exhaust
	// memory, so cap the line rather than growing without bound.
	scanner.Buffer(make([]byte, 0, 4096), 64*1024)

	for scanner.Scan() {
		var request Request
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			_ = writeResponse(conn, Response{Error: fmt.Sprintf("malformed request: %v", err)})
			continue
		}

		requestCtx, cancel := context.WithTimeout(ctx, requestTimeout)
		response := s.handler.Handle(requestCtx, peer, request)
		cancel()

		if err := writeResponse(conn, response); err != nil {
			s.logf("could not answer uid=%d: %v", peer.UID, err)
			return
		}
	}
}

func writeResponse(conn net.Conn, response Response) error {
	data, err := json.Marshal(response)
	if err != nil {
		return errors.Wrapf(err, "encoding the response")
	}

	if _, err := conn.Write(append(data, '\n')); err != nil {
		return errors.Wrapf(err, "writing the response")
	}

	return nil
}
