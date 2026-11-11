package store

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ArjunRachithcf/homerslab/internal/testenv"
)

// SQLite's own "database or disk is full" is ErrDiskFull, whatever the write: here the database may not grow.
func TestAFullDiskIsErrDiskFull(t *testing.T) {
	testenv.Isolate(t)
	s, err := Open(filepath.Join(t.TempDir(), "m.db"), []Step{{ID: "welcome", Half: "lan"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	var pages int
	if err := s.db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA max_page_count = `+strconv.Itoa(pages)); err != nil {
		t.Fatal(err)
	}
	err = s.CompleteStep(ctx, "welcome", map[string]string{"big": strings.Repeat("x", 1<<20)}, time.Now())
	if !errors.Is(err, ErrDiskFull) {
		t.Errorf("a write that needs room on a full disk: %v, want ErrDiskFull", err)
	}
	if _, err := s.Steps(ctx); err != nil {
		t.Errorf("reading on a full disk: %v", err)
	}
}
