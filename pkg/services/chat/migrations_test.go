package chat_test

import (
	"io/fs"
	"testing"

	"github.com/mmokit/mmokit/pkg/services/chat"
)

func TestMigrationsFS_Embeds001(t *testing.T) {
	fsys := chat.MigrationsFS()
	for _, name := range []string{"001_init.up.sql", "001_init.down.sql"} {
		f, err := fsys.Open(name)
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		_ = f.Close()
	}
	// also walk to ensure no unexpected extras in v1
	count := 0
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	if count != 2 {
		t.Fatalf("expected exactly 2 SQL files in v1 migrations, got %d", count)
	}
}
