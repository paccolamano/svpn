// Package ipc carries commands between the unprivileged client and the
// privileged daemon.
//
// The wire format is newline-delimited JSON: it is trivial to inspect with
// socat or nc while developing, and the message rate here is a handful per
// session, so nothing is gained by a denser encoding.
package ipc

import "time"

// State is where the VPN connection currently is.
type State string

// The states a connection moves through. A client that polls sees every one of
// them; StateError is kept until the next command so a failure explains itself
// after the fact.
const (
	StateDisconnected  State = "disconnected"
	StateConnecting    State = "connecting"
	StateConnected     State = "connected"
	StateDisconnecting State = "disconnecting"
	StateError         State = "error"
)

// Command names accepted by the daemon.
const (
	CommandStatus     = "status"
	CommandConnect    = "connect"
	CommandDisconnect = "disconnect"
)

// Request is one command from a client.
type Request struct {
	Command string `json:"command"`
	// Cookie authenticates the VPN session. The client performs the SAML login
	// — it owns the user's browser session — and hands the result over, so the
	// daemon never needs a browser or the user's identity provider.
	Cookie string `json:"cookie,omitempty"`
	Host   string `json:"host,omitempty"`
	Port   int    `json:"port,omitempty"`
}

// Response is the daemon's reply.
type Response struct {
	OK     bool    `json:"ok"`
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
}

// Status describes the connection.
type Status struct {
	State State     `json:"state"`
	Since time.Time `json:"since,omitzero"`
	// Version is the daemon's build. The client and the daemon are separate
	// binaries that can now be updated separately — one by a self-update, one
	// by a package the user installed months ago — so a client has to be able
	// to notice that it is talking to a different build than its own.
	Version string `json:"version,omitempty"`
	// Gateway is the host the tunnel is established with.
	Gateway string `json:"gateway,omitempty"`
	// Interface is the name of the tunnel device, once it exists.
	Interface string   `json:"interface,omitempty"`
	LocalIP   string   `json:"localIP,omitempty"`
	PeerIP    string   `json:"peerIP,omitempty"`
	DNS       []string `json:"dns,omitempty"`
	Routes    []string `json:"routes,omitempty"`
	// LastError explains the most recent failure; it is kept after the state
	// returns to disconnected so a client that polls can still report why.
	LastError string `json:"lastError,omitempty"`
	BytesIn   int64  `json:"bytesIn,omitempty"`
	BytesOut  int64  `json:"bytesOut,omitempty"`
}
