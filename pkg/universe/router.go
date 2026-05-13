package universe

import "github.com/google/uuid"

// PlayerRouter resolves an authenticated player to their target cell.
// Called after auth succeeds; receives both the canonical UserID (from
// auth.users) and the display username.
type PlayerRouter func(userID uuid.UUID, username string) string
