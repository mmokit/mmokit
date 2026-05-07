package chat

// SessionHook is the gateway-side adapter that drives chat presence
// after auth login/logout. mmokit.RegisterChatService installs an
// implementation that dispatches to the live Service.
//
// Methods are non-blocking — they run on the gateway's connection
// goroutine. Implementations should NOT do DB I/O inline.
type SessionHook interface {
	OnSessionEnter(connID uint32, userID, username, gatewayID string)
	OnSessionLeave(connID uint32, gatewayID string)
}
