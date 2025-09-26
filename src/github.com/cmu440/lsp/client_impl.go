// Contains the implementation of a LSP client.

package lsp

import (
	"encoding/json"
	"errors"

	"github.com/cmu440/lspnet"
)

type client struct {
	// identity + socket
	connID        int
	sock          *lspnet.UDPConn
	outSeq        int         // Sequence number
	appToNet      chan []byte // A channel to pass data from Write call to the network goroutine.
	inSeqExpected int
	buffer        map[int][]byte
	netToApp      chan []byte   // A channel to pass inorder data from the network goroutine to Read call.
	quit          chan struct{} // A channel to signal all goroutines to shut down.
	closed        bool
	params        *Params
}

// NewClient creates, initiates, and returns a new client. This function
// should return after a connection with the server has been established
// (i.e., the client has received an Ack message from the server in response
// to its connection request), and should return a non-nil error if a
// connection could not be made (i.e., if after K epochs, the client still
// hasn't received an Ack message from the server in response to its K
// connection requests).
//
// initialSeqNum is an int representing the Initial Sequence Number (ISN) this
// client must use. You may assume that sequence numbers do not wrap around.
//
// hostport is a colon-separated string identifying the server's host address
// and port number (i.e., "localhost:9999").

func NewClient(hostport string, initialSeqNum int, params *Params) (Client, error) {

	if params == nil {
		params = NewParams()
	}

	// Resolve the server's address and establish a UDP connection.
	servAddr, err := lspnet.ResolveUDPAddr("udp", hostport)
	if err != nil {
		return nil, err
	}
	udpConn, err := lspnet.DialUDP("udp", nil, servAddr)
	if err != nil {
		return nil, err
	}

	// Send a connection request message with the initial sequence number.
	if err := writeJSONConn(udpConn, NewConnect(initialSeqNum)); err != nil {
		_ = udpConn.Close() // Ensure connection is closed on failure.
		return nil, err
	}

	// Wait for an acknowledgment (Ack) from the server to confirm the connection.
	buf := make([]byte, 2048)
	n, err := udpConn.Read(buf)
	if err != nil {
		_ = udpConn.Close()
		return nil, err
	}
	var ack Message
	// Unmarshal the response and validate it to ensure it's a proper Ack for our connection request.
	if json.Unmarshal(buf[:n], &ack) != nil || ack.Type != MsgAck || ack.SeqNum != initialSeqNum || ack.ConnID <= 0 {
		_ = udpConn.Close()
		return nil, errors.New("handshake failed")
	}

	// Initialize the client object.
	c := &client{
		connID:        ack.ConnID,
		sock:          udpConn,
		outSeq:        initialSeqNum,
		inSeqExpected: initialSeqNum + 1,
		buffer:        make(map[int][]byte),
		appToNet:      make(chan []byte),
		netToApp:      make(chan []byte),
		quit:          make(chan struct{}),
		params:        params,
	}

	// Start the background goroutines.
	go c.recvLoop()
	go c.sendLoop()

	return c, nil
}

// recvLoop reads incoming UDP packets, validates them, sends acknowledgments,
// buffers outoforder data, and delivers inorder data
func (c *client) recvLoop() {
	buf := make([]byte, 65536)

	for {
		select {
		case <-c.quit:
			return
		default:
		}

		// Read the next packet from the UDP connection.
		n, err := c.sock.Read(buf)
		if err != nil {
			if !c.closed {
				select {
				case c.netToApp <- nil:
				default:
				}
			}
			return
		}

		// unmarshal the received data into a Message struct.
		var m Message
		if json.Unmarshal(buf[:n], &m) != nil {
			continue // Ignore malformed packets.
		}
		if m.Type != MsgData {
			continue
		}
		// Validate the message's checksum and size.
		if !validateDataMsg(&m) {
			continue
		}

		// send an ack for the received data message
		_ = writeJSONConn(c.sock, NewAck(c.connID, m.SeqNum))

		// Discard delivered duplicate messages
		if m.SeqNum < c.inSeqExpected {
			continue
		}

		// If this is a new, outoforder message, store it in the buffer
		if _, seen := c.buffer[m.SeqNum]; !seen {
			c.buffer[m.SeqNum] = append([]byte(nil), m.Payload...)
		}

		// Process the buffer to deliver any contiguous sequence of messages.
		for {
			payload, ok := c.buffer[c.inSeqExpected]
			if !ok {
				break // The next expected message is not yet received.
			}
			// Message found, remove it from the buffer and increment the expected sequence number.
			delete(c.buffer, c.inSeqExpected)
			c.inSeqExpected++

			// Send the payload to the application via the netToApp channel.
			select {
			case c.netToApp <- payload:
			case <-c.quit:
				return
			}
		}
	}
}

// sendLoop takes payloads via a channel and sends them as data messages.
func (c *client) sendLoop() {
	for {
		select {
		// Wait for a payload from Write call.
		case p := <-c.appToNet:
			if p == nil {
				return
			}
			c.outSeq++
			// Create a new data message with a calculated checksum.
			msg := NewData(c.connID, c.outSeq, len(p), p,
				CalculateChecksum(c.connID, c.outSeq, len(p), p))
			_ = writeJSONConn(c.sock, msg)
		case <-c.quit:
			return
		}
	}
}

// ConnID returns the client's connection ID.
func (c *client) ConnID() int { return c.connID }

// Read returns the next inorder payload read from the network.
func (c *client) Read() ([]byte, error) {
	select {
	case b := <-c.netToApp:
		if b == nil {
			return nil, errors.New("connection closed")
		}
		return b, nil
	case <-c.quit:
		return nil, errors.New("connection closed")
	}
}

// Write sends a payload over the connection.
func (c *client) Write(payload []byte) error {
	if c.closed {
		return errors.New("connection closed")
	}
	select {
	case c.appToNet <- append([]byte(nil), payload...):
		return nil
	case <-c.quit:
		return errors.New("connection closed")
	}
}

// Close shuts down the client connection.
func (c *client) Close() error {
	if c.closed {
		return nil
	}
	c.closed = true
	// Signal all goroutines to terminate.
	close(c.quit)
	_ = c.sock.Close()
	return nil
}

// helpers for client

// writeJSONConn marshals a Message struct to JSON and writes it to a connected UDP socket.
func writeJSONConn(conn *lspnet.UDPConn, m *Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = conn.Write(b)
	return err
}

// validateDataMsg checks the size and checksum of a data message.
func validateDataMsg(m *Message) bool {
	if m.Type != MsgData {
		return false
	}
	// Ensure the Size field is valid and consistent with the payload length.
	if m.Size < 0 || len(m.Payload) < m.Size {
		return false
	}
	// Trim the payload if it's larger than the specified size.
	if len(m.Payload) > m.Size {
		m.Payload = m.Payload[:m.Size]
	}
	// Verify the checksum to ensure data integrity.
	return CalculateChecksum(m.ConnID, m.SeqNum, m.Size, m.Payload) == m.Checksum
}
