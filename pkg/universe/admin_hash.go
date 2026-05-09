package universe

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/zenion/mmoserver/pkg/services/auth"
)

// promptAndPrintAdminHash reads a password from stdin (no echo when stdin is
// a TTY; raw line otherwise) and writes the encoded argon2id hash to stdout.
// Used by --admin-hash-password to generate AdminConfig.Operators entries.
func promptAndPrintAdminHash() error {
	fmt.Fprint(os.Stderr, "admin password: ")
	var pwd []byte
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return err
		}
		pwd = b
	} else {
		s := bufio.NewScanner(os.Stdin)
		if !s.Scan() {
			return errors.New("no input")
		}
		pwd = []byte(strings.TrimSpace(s.Text()))
	}
	if len(pwd) == 0 {
		return errors.New("empty password")
	}
	hash, err := auth.HashPassword(string(pwd), auth.DefaultArgonParams())
	if err != nil {
		return err
	}
	fmt.Println(hash)
	return nil
}
