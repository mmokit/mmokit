// Package auth — typed_messages.go defines the typed Go structs that
// replace the enginepb.Auth* protobuf types on the typed-op channel
// (channel 0x01, mmokit.RegisterOp[Req, Res]).
//
// These run through the engine's reflection codec — same shape as typed
// events on channel 0x00. Field names use Go's CamelCase convention; the
// generated TypeScript SDK exposes them as lowerCamelCase
// (`AuthLoginRequest{Username}` → `client.authLogin({username})`).
//
// Wire-stable typeIDs are derived from the package-qualified type name
// (`auth.AuthLoginRequest` etc.), so renames are wire-breaking.

package auth

// AuthError is the stable code identifying an auth failure mode. Wire
// values must not change once shipped — clients branch on this.
//
// Defined as a package-local Go const set instead of a proto enum so the
// auth subsystem owns its error vocabulary without leaking into the
// (now wire-empty) enginepb package.
type AuthError uint32

const (
	AuthErrorUnspecified        AuthError = 0
	AuthErrorInvalidCredentials AuthError = 1
	AuthErrorUsernameTaken      AuthError = 2
	AuthErrorUsernameInvalid    AuthError = 3
	AuthErrorPasswordTooWeak    AuthError = 4
	AuthErrorAccountLocked      AuthError = 5
	AuthErrorRateLimited        AuthError = 6
	AuthErrorMFARequired        AuthError = 7
	AuthErrorMFAInvalid         AuthError = 8
	AuthErrorTokenInvalid       AuthError = 9
	AuthErrorTokenExpired       AuthError = 10
	AuthErrorNotAuthenticated   AuthError = 11
	AuthErrorInternal           AuthError = 12
)

// AuthErrorMetadata carries per-error retry / disambiguation info. Lives
// on the *authError struct returned by handlers; serialized into the HTTP
// JSON error body and the typed-op response Error field.
type AuthErrorMetadata struct {
	RetryAfterMs int64
	Canonical    string // canonical (lowercased) username for USERNAME_TAKEN
}

// --- AUTH_OPCODE_LOGIN ---

// AuthLoginRequest is the typed request for the login op. Mirrors the
// JSON shape accepted by /auth/login.
type AuthLoginRequest struct {
	Username string
	Password string
	MFACode  string
}

// AuthLoginResponse is the typed response for both login and register
// (same field set; same code path on success). On the typed-op wire,
// failure is signaled by a non-empty Error / non-zero ErrorCode and the
// success-only fields are zero.
type AuthLoginResponse struct {
	UserID       string
	Username     string
	SessionToken string
	ExpiresAtMs  int64
	// Error fields — populated on failure paths so the typed-op client
	// can branch without relying on the framework OperationError envelope.
	// Empty / zero on success.
	ErrorCode    uint32
	ErrorMessage string
	RetryAfterMs int64
	Canonical    string
}

// --- AUTH_OPCODE_REGISTER ---

type AuthRegisterRequest struct {
	Username string
	Password string
	Email    string
}

// AuthRegisterResponse is structurally identical to AuthLoginResponse —
// kept distinct so client SDKs can branch by request type and so the
// typeID set on the wire keeps register / login independent.
type AuthRegisterResponse struct {
	UserID       string
	Username     string
	SessionToken string
	ExpiresAtMs  int64
	ErrorCode    uint32
	ErrorMessage string
	RetryAfterMs int64
	Canonical    string
}

// --- AUTH_OPCODE_VALIDATE_TOKEN ---

type AuthValidateTokenRequest struct {
	SessionToken string
}

type AuthValidateTokenResponse struct {
	UserID       string
	Username     string
	ExpiresAtMs  int64
	ErrorCode    uint32
	ErrorMessage string
	RetryAfterMs int64
}

// --- AUTH_OPCODE_LOGOUT ---

type AuthLogoutRequest struct{}

type AuthLogoutResponse struct {
	ErrorCode    uint32
	ErrorMessage string
}

// --- AUTH_OPCODE_CHANGE_PASSWORD ---

type AuthChangePasswordRequest struct {
	CurrentPassword string
	NewPassword     string
}

type AuthChangePasswordResponse struct {
	ErrorCode    uint32
	ErrorMessage string
}
