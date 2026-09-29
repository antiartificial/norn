package ingress

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

const maxPublishedRouteBytes = 1 << 20
const routeGenerationPrefix = "# norn-generation: "

var publishedRouteName = regexp.MustCompile(`^norn-route-[0-9a-f]{32}$`)

// PublishedRouteRevision is the exact local file state. Generation never
// decreases, including when Present is false after a withdrawal. Generation
// zero is reserved for a route that has never been published on this node.
type PublishedRouteRevision struct {
	Generation  uint64
	RouteSHA256 string
	Present     bool
}

// PublishRenderedRoute atomically writes one canonical file-provider route.
// expected must be the node state previously read by the controller. A
// repeated exact generation/content is idempotent after an uncertain reply.
// This local fence rejects older writers, including after A→B→A rollback.
// The node agent must still authenticate the issued generation and check its
// durable operation authority before invoking this primitive.
func PublishRenderedRoute(directory string, desired RenderedRoute, expected PublishedRouteRevision, generation uint64) error {
	if generation == 0 {
		return fmt.Errorf("route generation must be positive")
	}
	if err := validateRenderedRoute(desired); err != nil {
		return fmt.Errorf("invalid route publication: %w", err)
	}
	root, lock, err := openRoutePublication(directory, desired.RouterName)
	if err != nil {
		return err
	}
	defer root.Close()
	defer lock.Close()
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	current, err := readRouteRevision(root, desired.RouterName)
	if err != nil {
		return err
	}
	if current.Generation == generation && current.Present && current.RouteSHA256 == desired.SHA256 {
		return nil
	}
	if current != expected || generation <= current.Generation {
		return fmt.Errorf("route publication generation or revision conflict")
	}
	return writeRouteFile(root, directory, desired.RouterName, generation, desired.YAML)
}

// WithdrawPublishedRoute atomically replaces a first-published route with an
// empty Traefik file. It retains the generation tombstone so an older writer
// cannot reintroduce a route after rollback. It does not authorize a
// withdrawal; the node agent must verify its durable operation authority.
func WithdrawPublishedRoute(directory, routerName string, expected PublishedRouteRevision, generation uint64) error {
	if generation == 0 || !expected.Present {
		return fmt.Errorf("invalid route withdrawal")
	}
	root, lock, err := openRoutePublication(directory, routerName)
	if err != nil {
		return err
	}
	defer root.Close()
	defer lock.Close()
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	current, err := readRouteRevision(root, routerName)
	if err != nil {
		return err
	}
	if current.Generation == generation && !current.Present {
		return nil
	}
	if current != expected || generation <= current.Generation {
		return fmt.Errorf("route withdrawal generation or revision conflict")
	}
	return writeRouteFile(root, directory, routerName, generation, []byte(withdrawnRouteYAML(routerName)))
}

// ReadPublishedRouteRevision returns a missing route as the zero revision.
// A withdrawal returns Present=false with its retained generation. The
// content digest is of the canonical route YAML, excluding the generation
// header. This is exact file readback, not proof Traefik loaded the route.
func ReadPublishedRouteRevision(directory, routerName string) (PublishedRouteRevision, error) {
	if !filepath.IsAbs(directory) || !publishedRouteName.MatchString(routerName) {
		return PublishedRouteRevision{}, fmt.Errorf("invalid route readback path")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return PublishedRouteRevision{}, err
	}
	defer root.Close()
	return readRouteRevision(root, routerName)
}

func readRouteRevision(root *os.Root, routerName string) (PublishedRouteRevision, error) {
	file, err := root.OpenFile(routerName+".yaml", os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		return PublishedRouteRevision{}, nil
	}
	if err != nil {
		return PublishedRouteRevision{}, err
	}
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Size() > maxPublishedRouteBytes {
		file.Close()
		return PublishedRouteRevision{}, fmt.Errorf("published route is not a bounded regular file")
	}
	body, readErr := io.ReadAll(io.LimitReader(file, maxPublishedRouteBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || len(body) > maxPublishedRouteBytes {
		return PublishedRouteRevision{}, fmt.Errorf("read published route: %v, %v", readErr, closeErr)
	}
	newline := bytes.IndexByte(body, '\n')
	if newline < 0 || !bytes.HasPrefix(body, []byte(routeGenerationPrefix)) {
		return PublishedRouteRevision{}, fmt.Errorf("published route has no generation header")
	}
	rawGeneration := string(body[len(routeGenerationPrefix):newline])
	generation, err := strconv.ParseUint(rawGeneration, 10, 64)
	if err != nil || generation == 0 || strconv.FormatUint(generation, 10) != rawGeneration {
		return PublishedRouteRevision{}, fmt.Errorf("published route generation is invalid")
	}
	yamlBody := body[newline+1:]
	if string(yamlBody) == withdrawnRouteYAML(routerName) {
		return PublishedRouteRevision{Generation: generation}, nil
	}
	digest := sha256.Sum256(yamlBody)
	return PublishedRouteRevision{Generation: generation, RouteSHA256: hex.EncodeToString(digest[:]), Present: true}, nil
}

// Traefik's file-provider watcher may retain a router when a previously
// populated file becomes an empty HTTP document. An explicit replacement
// rule removes the public host while preserving a generation in the same
// atomic file. The reserved .invalid host has no public DNS meaning.
func withdrawnRouteYAML(routerName string) string {
	return fmt.Sprintf("http:\n    routers:\n        %s:\n            rule: Host(`withdrawn-%s.invalid`)\n            entryPoints:\n                - web\n            service: %s-withdrawn\n    services:\n        %s-withdrawn:\n            loadBalancer:\n                servers:\n                    - url: http://127.0.0.1:1\n", routerName, strings.TrimPrefix(routerName, "norn-route-"), routerName, routerName)
}

func openRoutePublication(directory, routerName string) (*os.Root, *os.File, error) {
	if !filepath.IsAbs(directory) || !publishedRouteName.MatchString(routerName) {
		return nil, nil, fmt.Errorf("invalid route publication path")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o002 != 0 {
		return nil, nil, fmt.Errorf("route directory must exist without world write access")
	}
	if info.Mode().Perm()&0o020 != 0 {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Uid != 0 || info.Mode()&os.ModeSticky == 0 {
			return nil, nil, fmt.Errorf("group-writable route directory must be root-owned and sticky")
		}
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, nil, err
	}
	lock, err := root.OpenFile("."+routerName+".lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		root.Close()
		return nil, nil, fmt.Errorf("open route publication lock: %w", err)
	}
	lockInfo, err := lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0o077 != 0 {
		lock.Close()
		root.Close()
		return nil, nil, fmt.Errorf("route publication lock is not private and regular")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		root.Close()
		return nil, nil, err
	}
	return root, lock, nil
}

func writeRouteFile(root *os.Root, directory, routerName string, generation uint64, yamlBody []byte) error {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return err
	}
	temporaryName := "." + routerName + "." + hex.EncodeToString(random) + ".tmp"
	// The route directory is group-owned by the dedicated publisher group on
	// Fleet. Traefik reads these route files through that group, while its own
	// TLS and readback files remain owner-only.
	temporary, err := root.OpenFile(temporaryName, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0o640)
	if err != nil {
		return err
	}
	defer root.Remove(temporaryName)
	contents := append([]byte(routeGenerationPrefix+strconv.FormatUint(generation, 10)+"\n"), yamlBody...)
	if len(contents) > maxPublishedRouteBytes {
		temporary.Close()
		return fmt.Errorf("published route exceeds size limit")
	}
	if _, err := temporary.Write(contents); err != nil {
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
	if err := root.Rename(temporaryName, routerName+".yaml"); err != nil {
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

func validateRenderedRoute(desired RenderedRoute) error {
	if !publishedRouteName.MatchString(desired.RouterName) || desired.ServiceName != desired.RouterName || len(desired.YAML) == 0 || len(desired.YAML) > maxPublishedRouteBytes {
		return fmt.Errorf("route identity or content is invalid")
	}
	digest := sha256.Sum256(desired.YAML)
	if hex.EncodeToString(digest[:]) != desired.SHA256 {
		return fmt.Errorf("route revision differs from content")
	}
	var document routeDocument
	if err := yaml.Unmarshal(desired.YAML, &document); err != nil {
		return fmt.Errorf("decode route: %w", err)
	}
	canonical, err := yaml.Marshal(document)
	if err != nil || string(canonical) != string(desired.YAML) || len(document.HTTP.Routers) != 1 || len(document.HTTP.Services) != 1 {
		return fmt.Errorf("route is not canonical and single-purpose")
	}
	router, routerOK := document.HTTP.Routers[desired.RouterName]
	service, serviceOK := document.HTTP.Services[desired.ServiceName]
	if !routerOK || !serviceOK || !routeHostname.MatchString(desired.EndpointHost) || router.Rule != "Host(`"+desired.EndpointHost+"`)" || router.Service != desired.ServiceName || len(router.EntryPoints) != 1 || len(service.Weighted.Services) == 0 {
		return fmt.Errorf("route metadata differs from content")
	}
	if router.EntryPoints[0] != "web" && router.EntryPoints[0] != "websecure" || (router.EntryPoints[0] == "websecure") != (router.TLS != nil) {
		return fmt.Errorf("route entry point is invalid")
	}
	if len(desired.BackendNames) != len(service.Weighted.Services) {
		return fmt.Errorf("route backends differ from metadata")
	}
	weightTotal := 0
	seenBackends := make(map[string]bool, len(desired.BackendNames))
	for index, backend := range service.Weighted.Services {
		if desired.BackendNames[index] == "" || seenBackends[backend.Name] || backend.Name != desired.BackendNames[index]+"@consulcatalog" || backend.Weight <= 0 || backend.Weight > 100 {
			return fmt.Errorf("route backend is invalid")
		}
		seenBackends[backend.Name] = true
		weightTotal += backend.Weight
	}
	if weightTotal != 100 {
		return fmt.Errorf("route weights must total 100")
	}
	return nil
}
