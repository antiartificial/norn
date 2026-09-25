package nomad

import (
	_ "embed"
	"fmt"

	nomadapi "github.com/hashicorp/nomad/api"

	"norn/v2/api/model"
)

const wordpressVerifiedTLSDropInDestination = "local/norn-wordpress/db.php"
const wordpressVerifiedTLSDropInSHA256 = "498b2a8b79d93172bfedb5fc17cd0ec25160262a3daddba37f76107d2a1fa6d1"

//go:embed wordpress_verified_tls_db.php
var wordpressVerifiedTLSDropIn string

// applyStartupAdapter installs the qualified db.php before the official image
// entrypoint. It refuses to replace any existing db.php whose bytes differ
// from the pinned managed file. Initial installation uses a same-directory
// temporary file and atomic rename on the mounted content filesystem.
func applyStartupAdapter(spec *model.InfraSpec, processName, image string, task *nomadapi.Task) {
	if spec == nil || task == nil || spec.StartupAdapter != model.StartupAdapterWordPressVerifiedTLS {
		return
	}
	if processName != "web" || !wordpressVerifiedTLSManifestQualified(image) {
		task.Config["command"] = "/bin/sh"
		task.Config["args"] = []string{"-ec", "exit 78"}
		return
	}
	task.Templates = append(task.Templates, &nomadapi.Template{
		EmbeddedTmpl:  strPtr(wordpressVerifiedTLSDropIn),
		DestPath:      strPtr(wordpressVerifiedTLSDropInDestination),
		Perms:         strPtr("0444"),
		ChangeMode:    strPtr("restart"),
		ErrMissingKey: boolPtr(true),
	})
	task.Config["command"] = "/bin/sh"
	task.Config["args"] = []string{"-ec", wordpressVerifiedTLSStartupScript()}
}

func wordpressVerifiedTLSStartupScript() string {
	return fmt.Sprintf(`source=/local/norn-wordpress/db.php
target=/var/www/html/wp-content/db.php
expected=%s
source_digest="$(sha256sum "$source" | cut -d ' ' -f 1)"
if [ "$source_digest" != "$expected" ]; then
  echo "norn: WordPress db.php template digest mismatch" >&2
  exit 78
fi
if [ -e "$target" ]; then
  target_digest="$(sha256sum "$target" | cut -d ' ' -f 1)"
  if [ "$target_digest" != "$expected" ]; then
    echo "norn: refusing to replace unrecognized WordPress db.php" >&2
    exit 78
  fi
else
  tmp="$(mktemp "${target}.norn.XXXXXX")"
  trap 'rm -f "$tmp"' EXIT
  install -m 0444 "$source" "$tmp"
  mv -f "$tmp" "$target"
fi
exec /usr/local/bin/docker-entrypoint.sh apache2-foreground`, wordpressVerifiedTLSDropInSHA256)
}

func wordpressVerifiedTLSManifestQualified(image string) bool {
	return image == model.QualifiedWordPressVerifiedTLSImage
}
