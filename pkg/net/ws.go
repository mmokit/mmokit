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

func (t *WSTransport) SendReliable(data []byte) SendResult   { return t.conn.Send(data) }
func (t *WSTransport) SendUnreliable(data []byte) SendResult { return t.conn.Send(data) }
func (t *WSTransport) DrainInput() [][]byte                  { return t.conn.DrainInput() }
func (t *WSTransport) DrainOpInput() [][]byte                { return t.conn.DrainOpInput() }
func (t *WSTransport) InjectInput(data []byte)               { t.conn.InjectInput(data) }
func (t *WSTransport) Close()                                { t.conn.Close() }
func (t *WSTransport) BytesSent() uint64                     { return t.conn.BytesSent() }
func (t *WSTransport) BytesRecv() uint64                     { return t.conn.BytesRecv() }
func (t *WSTransport) Stats() ConnStats                      { return t.conn.Stats() }
