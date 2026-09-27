package ingress

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"

	"gopkg.in/yaml.v3"
)

const maxPublishedRouteBytes = 1 << 20

var publishedRouteName = regexp.MustCompile(`^norn-route-[0-9a-f]{32}$`)

// PublishRenderedRoute atomically replaces one route in a trusted Traefik file
// provider directory. expectedSHA256 is the version last observed on this
// node, or empty when no route exists. The caller must coordinate all ingress
// nodes and verify effective configuration and endpoints before activating
// traffic. The directory must be a dedicated, locally trusted path.
func PublishRenderedRoute(directory string, desired RenderedRoute, expectedSHA256 string) error {
	if !filepath.IsAbs(directory) || !publishedRouteName.MatchString(desired.RouterName) || desired.ServiceName != desired.RouterName || len(desired.YAML) == 0 || len(desired.YAML) > maxPublishedRouteBytes {
		return fmt.Errorf("invalid route publication")
	}
	if expectedSHA256 != "" && !validRouteSHA(expectedSHA256) {
		return fmt.Errorf("invalid expected route revision")
	}
	digest := sha256.Sum256(desired.YAML)
	if hex.EncodeToString(digest[:]) != desired.SHA256 {
		return fmt.Errorf("route revision differs from content")
	}
	var document routeDocument
	if err := yaml.Unmarshal(desired.YAML, &document); err != nil {
		return fmt.Errorf("decode desired route: %w", err)
	}
	canonical, err := yaml.Marshal(document)
	if err != nil || string(canonical) != string(desired.YAML) || len(document.HTTP.Routers) != 1 || len(document.HTTP.Services) != 1 {
		return fmt.Errorf("desired route is not canonical and single-purpose")
	}
	router, routerOK := document.HTTP.Routers[desired.RouterName]
	service, serviceOK := document.HTTP.Services[desired.ServiceName]
	if !routerOK || !serviceOK || !routeHostname.MatchString(desired.EndpointHost) || router.Rule != "Host(`"+desired.EndpointHost+"`)" || router.Service != desired.ServiceName || len(router.EntryPoints) != 1 || len(service.Weighted.Services) == 0 {
		return fmt.Errorf("desired route metadata differs from content")
	}
	if router.EntryPoints[0] != "web" && router.EntryPoints[0] != "websecure" || (router.EntryPoints[0] == "websecure") != (router.TLS != nil) {
		return fmt.Errorf("desired route entry point is invalid")
	}
	weightTotal := 0
	if len(desired.BackendNames) != len(service.Weighted.Services) {
		return fmt.Errorf("desired route backends differ from metadata")
	}
	seenBackends := make(map[string]bool, len(desired.BackendNames))
	for index, backend := range service.Weighted.Services {
		if desired.BackendNames[index] == "" || seenBackends[backend.Name] || backend.Name != desired.BackendNames[index]+"@consulcatalog" || backend.Weight <= 0 || backend.Weight > 100 {
			return fmt.Errorf("desired route backend is invalid")
		}
		seenBackends[backend.Name] = true
		weightTotal += backend.Weight
	}
	if weightTotal != 100 {
		return fmt.Errorf("desired route weights must total 100")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("route directory must exist without group or world write access")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer root.Close()
	name := desired.RouterName + ".yaml"
	lock, err := root.OpenFile("."+desired.RouterName+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("open route publication lock: %w", err)
	}
	defer lock.Close()
	lockInfo, err := lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("route publication lock is not private and regular")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	current, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	currentSHA := ""
	if err == nil {
		currentInfo, statErr := current.Stat()
		if statErr != nil || !currentInfo.Mode().IsRegular() || currentInfo.Size() > maxPublishedRouteBytes {
			current.Close()
			return fmt.Errorf("existing route is not a bounded regular file")
		}
		body, readErr := io.ReadAll(io.LimitReader(current, maxPublishedRouteBytes+1))
		closeErr := current.Close()
		if readErr != nil || closeErr != nil || len(body) > maxPublishedRouteBytes {
			return fmt.Errorf("read existing route: %v, %v", readErr, closeErr)
		}
		sum := sha256.Sum256(body)
		currentSHA = hex.EncodeToString(sum[:])
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("open existing route: %w", err)
	}
	if currentSHA != expectedSHA256 {
		return fmt.Errorf("route revision conflict: observed %s", currentSHA)
	}
	if currentSHA == desired.SHA256 {
		return nil
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporaryName := "." + desired.RouterName + "." + hex.EncodeToString(random) + ".tmp"
	temporary, err := root.OpenFile(temporaryName, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer root.Remove(temporaryName)
	if _, err := temporary.Write(desired.YAML); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := root.Rename(temporaryName, name); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	syncErr, closeErr := dir.Sync(), dir.Close()
	if syncErr != nil || closeErr != nil {
		return fmt.Errorf("sync route directory: %v, %v", syncErr, closeErr)
	}
	return nil
}

func validRouteSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' && c < 'a' || c > 'f' {
			return false
		}
	}
	return true
}
