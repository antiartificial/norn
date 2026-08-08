package contract

import (
	_ "embed"
	"net/http"
)

//go:embed control-v1.openapi.yaml
var controlV1 []byte

func ServeOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(controlV1)
}
