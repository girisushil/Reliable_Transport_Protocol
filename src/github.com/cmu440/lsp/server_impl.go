// Contains the implementation of a LSP server.

package lsp

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/cmu440/lspnet"
)

// connected client.
type peer struct {
	addr    *lspnet.UDPAddr
	id      int
	wantSeq int
	sendSeq int
	buffer  map[int][]byte
	closed  bool
}

// implements the Server interface.
type server struct {
	sock   *lspnet.UDPConn
	params *Params

	nextID    int
	byID      map[int]*peer    // A map to look up peers by their connection ID.
	byAddr    map[string]*peer // A map to look up peers by their UDP address string.
	appReads  chan readItem
	appWrites chan writeItem
	quit      chan struct{} // A channel to signal all goroutines to shut down.
	closed    bool          // A flag indicating whether the server has been closed.
}

// readItem represents a piece of data read from the network to be passed to the application.
type readItem struct {
	connID  int
	payload []byte
	err     error
}

// writeItem represents a piece of data from the application to be written to the network.
type writeItem struct {
	connID  int
	payload []byte
}

// NewServer creates, initiates, and returns a new server. This function should
// NOT block. Instead, it should spawn one or more goroutines (to handle things
// like accepting incoming client connections, triggering epoch events at
// fixed intervals, synchronizing events using a for-select loop like you saw in
// project 0, etc.) and immediately return. It should return a non-nil error if
// there was an error resolving or listening on the specified port number.
func NewServer(port int, params *Params) (Server, error) {
	// Use default parameters if none are provided.
	if params == nil {
		params = NewParams()
	}

	// Resolve the local address and start listening on the specified UDP port.
	addr, err := lspnet.ResolveUDPAddr("udp", ":"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}
	udpConn, err := lspnet.ListenUDP("udp", addr)
	if err != nil {
		return nil, err
	}

	// Initialize the server object.
	s := &server{
		sock:      udpConn,
		params:    params,
		nextID:    1, // Start connection IDs at 1.
		byID:      make(map[int]*peer),
		byAddr:    make(map[string]*peer),
		appReads:  make(chan readItem),
		appWrites: make(chan writeItem),
		quit:      make(chan struct{}),
	}

	// Start background goroutines
	go s.recvLoop()
	go s.sendLoop()

	return s, nil
}

// sendLoop takes write requests from the application and sends them as data messages to the correct clients.
func (s *server) sendLoop() {
	for {
		select {
		// Terminate the loop if a quit signal is received.
		case <-s.quit:
			return
		// Block until a write job is received from the application.
		case job := <-s.appWrites:
			cl, ok := s.byID[job.connID]
			if !ok || cl.closed {
				continue // Ignore writes to non-existent or closed connections.
			}
			// Prepare the next sequence number for the outgoing message.
			cl.sendSeq++
			// Create the data message with a calculated checksum.
			msg := NewData(cl.id, cl.sendSeq, len(job.payload), job.payload,
				CalculateChecksum(cl.id, cl.sendSeq, len(job.payload), job.payload))
			// Send the message to the client's address.
			_ = writeJSONToAddr(s.sock, cl.addr, msg)
		}
	}
}

// recvLoop reads incoming UDP packets, decodes them, and dispatches them to the appropriate handler.
func (s *server) recvLoop() {
	buf := make([]byte, 65536) // A buffer for reading incoming packets.

	for {
		select {
		case <-s.quit:
			return
		default:
		}

		// Read the next UDP packet and get the sender's address.
		n, raddr, err := s.sock.ReadFromUDP(buf)
		if err != nil {
			if !s.closed {
				continue
			}
			return // Exit if server is closed.
		}

		// unmarshalling the packet into a Message struct.
		var m Message
		if json.Unmarshal(buf[:n], &m) != nil {
			continue
		}

		// handle the message based on its type.
		switch m.Type {
		case MsgConnect:
			s.onConnect(&m, raddr)
		case MsgData:
			s.onData(&m, raddr)
		}
	}
}

// onConnect handles an incoming connection request.
func (s *server) onConnect(m *Message, from *lspnet.UDPAddr) {
	key := from.String() // Use the client's address as a unique key.

	// If we already have a peer for this address, it's a duplicate connect request
	// Just resend the acknowledgment
	if cl, ok := s.byAddr[key]; ok {
		_ = writeJSONToAddr(s.sock, from, NewAck(cl.id, m.SeqNum))
		return
	}

	id := s.nextID
	s.nextID++

	// Create a new peer object to track the client's state.
	p := &peer{
		addr:    from,
		id:      id,
		wantSeq: m.SeqNum + 1,
		sendSeq: m.SeqNum,
		buffer:  make(map[int][]byte),
	}
	// Store the new peer in the maps.
	s.byID[id] = p
	s.byAddr[key] = p

	// Send an acknowledgment back to the client to confirm the connection.
	_ = writeJSONToAddr(s.sock, from, NewAck(id, m.SeqNum))
}

// onData handles an incoming data message from a client.
func (s *server) onData(m *Message, from *lspnet.UDPAddr) {
	// Look up the peer primarily by its address.
	key := from.String()
	cl, ok := s.byAddr[key]

	// If not found by address , fall back to the connection ID.
	if !ok && m.ConnID != 0 {
		if tmp, ok2 := s.byID[m.ConnID]; ok2 {
			cl = tmp
		}
	}
	if cl == nil || cl.closed {
		return
	}

	// Validate the message's size and checksum.
	if !validateDataMsg(m) {
		return
	}

	// Always send an acknowledgment immediately upon receiving a valid data message.
	_ = writeJSONToAddr(s.sock, cl.addr, NewAck(cl.id, m.SeqNum))

	// Ignore duplicate messages that have already been delivered.
	if m.SeqNum < cl.wantSeq {
		return
	}

	if _, seen := cl.buffer[m.SeqNum]; !seen {
		cl.buffer[m.SeqNum] = append([]byte(nil), m.Payload...)
	}

	for {
		payload, ok := cl.buffer[cl.wantSeq]
		if !ok {
			break
		}
		// Message found, remove it from buffer and advance the expected sequence number.
		delete(cl.buffer, cl.wantSeq)
		cl.wantSeq++

		// Send the payload to the application via the appReads channel.
		select {
		case s.appReads <- readItem{connID: cl.id, payload: payload, err: nil}:
		case <-s.quit:
			return
		}
	}
}

// Read blocks until a message is received from any client.
func (s *server) Read() (int, []byte, error) {
	select {
	// Wait for a read item from the network receive loop.
	case r := <-s.appReads:
		return r.connID, r.payload, r.err
	case <-s.quit:
		return 0, nil, errors.New("server closed")
	}
}

// Write sends a payload to a specific client.
func (s *server) Write(connId int, payload []byte) error {
	if s.closed {
		return errors.New("server closed")
	}
	// Check if the connection ID is valid.
	if _, ok := s.byID[connId]; !ok {
		return errors.New("connection does not exist")
	}
	// Pass the write job to the send loop.
	select {
	case s.appWrites <- writeItem{connID: connId, payload: append([]byte(nil), payload...)}:
		return nil
	case <-s.quit:
		return errors.New("server closed")
	}
}

// CloseConn closes a single client connection.
func (s *server) CloseConn(connId int) error {
	cl, ok := s.byID[connId]
	if !ok {
		return errors.New("connection does not exist")
	}
	// Mark the peer as closed and remove it from the maps.
	cl.closed = true
	delete(s.byID, connId)
	delete(s.byAddr, cl.addr.String())
	return nil
}

// Close shuts down the entire server.
func (s *server) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	// Signal all goroutines to terminate.
	close(s.quit)
	// Close the underlying listening socket.
	_ = s.sock.Close()
	return nil
}

// helpers

// writeJSONToAddr marshals a message to JSON and sends it to a specific UDP address.
func writeJSONToAddr(conn *lspnet.UDPConn, addr *lspnet.UDPAddr, m *Message) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = conn.WriteToUDP(b, addr)
	return err
}
