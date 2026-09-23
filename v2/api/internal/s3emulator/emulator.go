// Package s3emulator is an in-process, test-only emulation of the small S3
// API subset Norn's object archive uses: HEAD bucket, conditional PUT
// (If-None-Match: *), HEAD/GET object (with If-Match) and ListObjectsV2. It
// verifies Content-MD5 and signed payload digests and the request's access
// key. It exists to test Norn's adapter logic and fault handling; passing
// against it qualifies no real provider (Garage, MinIO, S3 or a Fleet
// object service).
package s3emulator

import (
	"bytes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"
)

type object struct {
	data     []byte
	metadata http.Header
	etag     string
	modified time.Time
}

// Emulator is one bucket. Fault switches may be changed between requests.
type Emulator struct {
	Bucket    string
	AccessKey string

	mu      sync.Mutex
	objects map[string]object
	// IgnoreConditional makes PUT overwrite despite If-None-Match (a store
	// that cannot enforce immutable publication).
	IgnoreConditional bool
	// CorruptReads flips a byte in every GET body.
	CorruptReads bool
	// FailWrites answers PUT with 503.
	FailWrites bool
	// QuotaBytes, when positive, refuses PUTs that would exceed it (507).
	QuotaBytes int64
	// Requests counts requests by method.
	Requests map[string]int
}

// Start serves the emulator over TLS; use Server.Client().Transport.
func Start(bucket, accessKey string) (*Emulator, *httptest.Server) {
	emulator := &Emulator{Bucket: bucket, AccessKey: accessKey, objects: map[string]object{}, Requests: map[string]int{}}
	return emulator, httptest.NewTLSServer(emulator)
}

// Configure changes fault switches under the emulator's lock (requests are
// served concurrently).
func (e *Emulator) Configure(mutate func(*Emulator)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	mutate(e)
}

// Tamper replaces an object's stored bytes (privileged corruption).
func (e *Emulator) Tamper(key string, data []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if current, ok := e.objects[key]; ok {
		current.data = append([]byte(nil), data...)
		e.objects[key] = current
	}
}

// Keys lists stored keys.
func (e *Emulator) Keys() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	keys := make([]string, 0, len(e.objects))
	for key := range e.objects {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

type errorBody struct {
	XMLName xml.Name `xml:"Error"`
	Code    string   `xml:"Code"`
	Message string   `xml:"Message"`
}

func fail(w http.ResponseWriter, r *http.Request, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_ = xml.NewEncoder(w).Encode(errorBody{Code: code, Message: code})
	}
}

func (e *Emulator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Requests[r.Method]++
	if !strings.Contains(r.Header.Get("Authorization"), "Credential="+e.AccessKey+"/") {
		fail(w, r, http.StatusForbidden, "InvalidAccessKeyId")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	bucket, key, _ := strings.Cut(path, "/")
	if bucket != e.Bucket {
		fail(w, r, http.StatusNotFound, "NoSuchBucket")
		return
	}
	switch {
	case key == "" && (r.Method == http.MethodHead || (r.Method == http.MethodGet && r.URL.Query().Has("location"))):
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><LocationConstraint>us-east-1</LocationConstraint>`))
		}
	case key == "" && r.Method == http.MethodGet:
		e.list(w, r)
	case r.Method == http.MethodPut:
		e.put(w, r, key)
	case r.Method == http.MethodHead || r.Method == http.MethodGet:
		e.get(w, r, key)
	default:
		fail(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

func (e *Emulator) put(w http.ResponseWriter, r *http.Request, key string) {
	if e.FailWrites {
		fail(w, r, http.StatusServiceUnavailable, "ServiceUnavailable")
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	if err != nil {
		fail(w, r, http.StatusBadRequest, "IncompleteBody")
		return
	}
	if sum := r.Header.Get("Content-Md5"); sum != "" {
		digest := md5.Sum(data)
		if sum != base64.StdEncoding.EncodeToString(digest[:]) {
			fail(w, r, http.StatusBadRequest, "BadDigest")
			return
		}
	}
	if declared := r.Header.Get("X-Amz-Content-Sha256"); len(declared) == 64 {
		digest := sha256.Sum256(data)
		if declared != hex.EncodeToString(digest[:]) {
			fail(w, r, http.StatusBadRequest, "XAmzContentSHA256Mismatch")
			return
		}
	}
	if _, exists := e.objects[key]; exists && r.Header.Get("If-None-Match") == "*" && !e.IgnoreConditional {
		fail(w, r, http.StatusPreconditionFailed, "PreconditionFailed")
		return
	}
	if e.QuotaBytes > 0 {
		var used int64
		for _, stored := range e.objects {
			used += int64(len(stored.data))
		}
		if used+int64(len(data)) > e.QuotaBytes {
			fail(w, r, http.StatusInsufficientStorage, "XMinioStorageFull")
			return
		}
	}
	metadata := http.Header{}
	for name, values := range r.Header {
		if strings.HasPrefix(strings.ToLower(name), "x-amz-meta-") {
			metadata[name] = values
		}
	}
	digest := md5.Sum(data)
	stored := object{data: data, metadata: metadata, etag: `"` + hex.EncodeToString(digest[:]) + `"`, modified: time.Now().UTC()}
	e.objects[key] = stored
	w.Header().Set("ETag", stored.etag)
}

func (e *Emulator) get(w http.ResponseWriter, r *http.Request, key string) {
	stored, ok := e.objects[key]
	if !ok {
		fail(w, r, http.StatusNotFound, "NoSuchKey")
		return
	}
	if match := r.Header.Get("If-Match"); match != "" && strings.Trim(match, `"`) != strings.Trim(stored.etag, `"`) {
		fail(w, r, http.StatusPreconditionFailed, "PreconditionFailed")
		return
	}
	for name, values := range stored.metadata {
		w.Header()[name] = values
	}
	body := stored.data
	if e.CorruptReads && len(body) > 0 {
		body = bytes.Clone(body)
		body[len(body)/2] ^= 0x01
	}
	w.Header().Set("ETag", stored.etag)
	w.Header().Set("Last-Modified", stored.modified.Format(http.TimeFormat))
	w.Header().Set("Content-Length", fmt.Sprint(len(body)))
	w.Header().Set("Content-Type", "application/octet-stream")
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}

type listContents struct {
	Key          string `xml:"Key"`
	LastModified string `xml:"LastModified"`
	ETag         string `xml:"ETag"`
	Size         int64  `xml:"Size"`
	StorageClass string `xml:"StorageClass"`
}

type listResult struct {
	XMLName     xml.Name       `xml:"ListBucketResult"`
	Name        string         `xml:"Name"`
	Prefix      string         `xml:"Prefix"`
	KeyCount    int            `xml:"KeyCount"`
	MaxKeys     int            `xml:"MaxKeys"`
	IsTruncated bool           `xml:"IsTruncated"`
	Contents    []listContents `xml:"Contents"`
}

func (e *Emulator) list(w http.ResponseWriter, r *http.Request) {
	prefix := r.URL.Query().Get("prefix")
	result := listResult{Name: e.Bucket, Prefix: prefix, MaxKeys: 1000}
	keys := make([]string, 0, len(e.objects))
	for key := range e.objects {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		stored := e.objects[key]
		result.Contents = append(result.Contents, listContents{Key: key, LastModified: stored.modified.Format(time.RFC3339), ETag: stored.etag, Size: int64(len(stored.data)), StorageClass: "STANDARD"})
	}
	result.KeyCount = len(result.Contents)
	w.Header().Set("Content-Type", "application/xml")
	_ = xml.NewEncoder(w).Encode(result)
}
