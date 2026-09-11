package ipc

import (
	"bufio"
	"encoding/json"
	"net"
	"time"

	"github.com/sorintlab/errors"
)

// Client talks to the daemon.
type Client struct {
	conn    net.Conn
	scanner *bufio.Scanner
}

// NewClient connects to the daemon listening at path.
func NewClient(path string) (*Client, error) {
	conn, err := Dial(path)
	if err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)

	return &Client{conn: conn, scanner: scanner}, nil
}

// Close releases the connection.
func (c *Client) Close() error {
	return errors.Wrapf(c.conn.Close(), "closing the connection to the daemon")
}

// Do sends one request and waits for its reply.
func (c *Client) Do(request Request, timeout time.Duration) (Response, error) {
	if err := c.conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return Response{}, errors.Wrapf(err, "setting the deadline")
	}

	data, err := json.Marshal(request)
	if err != nil {
		return Response{}, errors.Wrapf(err, "encoding the request")
	}

	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		return Response{}, errors.Wrapf(err, "sending the request")
	}

	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return Response{}, errors.Wrapf(err, "reading the reply")
		}
		return Response{}, errors.Errorf("the daemon closed the connection without replying")
	}

	var response Response
	if err := json.Unmarshal(c.scanner.Bytes(), &response); err != nil {
		return Response{}, errors.Wrapf(err, "decoding the reply")
	}

	return response, nil
}
