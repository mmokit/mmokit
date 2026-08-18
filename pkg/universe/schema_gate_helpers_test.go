package universe

import (
	"strings"

	"github.com/mmokit/mmokit/pkg/logger"
)

func newGateTestLogger() *logger.Logger { return logger.New() }

func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), strings.ToLower(needle))
}
