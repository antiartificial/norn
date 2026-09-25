package nomad

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWordPressVerifiedTLSStartupPreservesManagedContent(t *testing.T) {
	sum := sha256.Sum256([]byte(wordpressVerifiedTLSDropIn))
	if got := hex.EncodeToString(sum[:]); got != wordpressVerifiedTLSDropInSHA256 {
		t.Fatalf("embedded drop-in digest %s differs from pinned %s", got, wordpressVerifiedTLSDropInSHA256)
	}
	root := t.TempDir()
	source := filepath.Join(root, "source.php")
	target := filepath.Join(root, "content", "db.php")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte(wordpressVerifiedTLSDropIn), 0444); err != nil {
		t.Fatal(err)
	}
	script := wordpressVerifiedTLSStartupScript()
	script = strings.Replace(script, "source=/local/norn-wordpress/db.php", "source="+strconv.Quote(source), 1)
	script = strings.Replace(script, "target=/var/www/html/wp-content/db.php", "target="+strconv.Quote(target), 1)
	script = strings.Replace(script, "exec /usr/local/bin/docker-entrypoint.sh apache2-foreground", "exit 0", 1)
	run := func(wantSuccess bool) {
		t.Helper()
		output, err := exec.Command("/bin/sh", "-ec", script).CombinedOutput()
		if wantSuccess && err != nil || !wantSuccess && err == nil {
			t.Fatalf("startup success=%t, err=%v, output=%q", wantSuccess, err, output)
		}
	}
	run(true)
	installed, err := os.ReadFile(target)
	if err != nil || string(installed) != wordpressVerifiedTLSDropIn {
		t.Fatalf("installed drop-in differs: err=%v", err)
	}
	run(true)
	if err := os.Chmod(target, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("<?php\n// altered managed file\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(false)
	unchanged, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(unchanged), "altered") {
		t.Fatalf("unrecognized drop-in was replaced: err=%v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("tampered template"), 0644); err != nil {
		t.Fatal(err)
	}
	run(false)
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("tampered template installed: stat error=%v", err)
	}
}
