package database

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestMySQLRestorePostImportRechecksOpenedArtifact(t *testing.T) {
	payload := []byte("INSERT INTO records VALUES (1);\n")
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	artifact := MySQLSQLArtifact{Bytes: int64(len(payload)), SHA256: digest}
	for _, mode := range []string{"unchanged", "in-place-write", "mode-change", "path-replacement", "symlink-replacement"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.sql")
			if err := os.WriteFile(path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			before, err := file.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(io.Discard, file); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "mode-change":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "in-place-write":
				changed := append([]byte(nil), payload...)
				changed[0] = 'X'
				if err := os.WriteFile(path, changed, 0o600); err != nil {
					t.Fatal(err)
				}
			case "path-replacement", "symlink-replacement":
				moved := path + ".moved"
				if err := os.Rename(path, moved); err != nil {
					t.Fatal(err)
				}
				if mode == "path-replacement" {
					if err := os.WriteFile(path, payload, 0o600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.Symlink(moved, path); err != nil {
					t.Fatal(err)
				}
			}
			err = verifyOpenMySQLSQLArtifactAfterImport(file, path, artifact, before)
			if mode == "unchanged" && err != nil {
				t.Fatalf("unchanged artifact rejected: %v", err)
			}
			if mode != "unchanged" && err == nil {
				t.Fatalf("%s was accepted after import", mode)
			}
		})
	}
}
