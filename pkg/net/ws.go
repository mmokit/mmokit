package net

// WSTransport wraps an existing WebSocket Conn to implement the Transport interface.
// Since WebSocket runs over TCP, both reliable and unreliable sends are identical.
type WSTransport struct {
	conn *Conn
}

// NewWSTransport creates a WSTransport wrapping the given Conn.
func NewWSTransport(conn *Conn) *WSTransport {
	return &WSTransport{conn: conn}
}

func (t *WSTransport) SendReliable(data []byte)  { t.conn.Send(data) }
func (t *WSTransport) SendUnreliable(data []byte) { t.conn.Send(data) }
func (t *WSTransport) DrainInput() [][]byte        { return t.conn.DrainInput() }
func (t *WSTransport) Close()                      { t.conn.Close() }
