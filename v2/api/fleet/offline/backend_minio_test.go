package offline

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func TestMinIOBackendReaderRequiresEnabledBucketVersioning(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
	}{{"Enabled", true}, {"Suspended", false}, {"", false}} {
		t.Run(tc.status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/state/" || !r.URL.Query().Has("versioning") {
					t.Errorf("unexpected object store call: %s %s", r.Method, r.URL)
					http.Error(w, "unexpected", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprintf(w, `<VersioningConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Status>%s</Status></VersioningConfiguration>`, tc.status)
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			client, err := minio.New(endpoint.Host, &minio.Options{Creds: credentials.NewStaticV4("read", "secret", ""), Secure: false, Region: "us-east-1", BucketLookup: minio.BucketLookupPath})
			if err != nil {
				t.Fatal(err)
			}
			got, err := (MinIOLatestBackendReader{Client: client}).VersioningEnabled(context.Background(), "state", "staging.tfstate")
			if err == nil && got != tc.want {
				t.Fatalf("status %q: got %v want %v", tc.status, got, tc.want)
			}
			if tc.want && err != nil {
				t.Fatalf("enabled rejected: %v", err)
			}
			if !tc.want && err == nil {
				t.Fatal("non-enabled accepted")
			}
		})
	}
}

func TestMinIOBackendReaderRejectsNullHeadVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.URL.Path != "/state/staging.tfstate" {
			t.Errorf("unexpected object store call: %s %s", r.Method, r.URL)
			http.Error(w, "unexpected", http.StatusBadRequest)
			return
		}
		w.Header().Set("x-amz-version-id", "null")
		w.Header().Set("Content-Length", "32")
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL)
	client, err := minio.New(endpoint.Host, &minio.Options{Creds: credentials.NewStaticV4("read", "secret", ""), Secure: false, Region: "us-east-1", BucketLookup: minio.BucketLookupPath})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = (MinIOLatestBackendReader{Client: client}).Latest(context.Background(), "state", "staging.tfstate")
	if !errors.Is(err, ErrLatestBackendMismatch) {
		t.Fatalf("null HEAD accepted: %v", err)
	}
}

func TestMinIOBackendReaderRequiresAuthenticatedMissingLock(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		code   string
		want   bool
	}{
		{"absent", http.StatusNotFound, "NoSuchKey", true},
		{"present", http.StatusOK, "", false},
		{"ambiguous", http.StatusForbidden, "AccessDenied", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead || r.URL.Path != "/state/staging.tfstate.tflock" {
					t.Fatalf("unexpected lock read: %s %s", r.Method, r.URL)
				}
				if tc.status != http.StatusOK {
					w.Header().Set("Content-Type", "application/xml")
					w.WriteHeader(tc.status)
					fmt.Fprintf(w, `<Error><Code>%s</Code><Message>test</Message></Error>`, tc.code)
					return
				}
				w.Header().Set("Content-Length", "10")
			}))
			defer server.Close()
			endpoint, _ := url.Parse(server.URL)
			client, err := minio.New(endpoint.Host, &minio.Options{Creds: credentials.NewStaticV4("read", "secret", ""), Secure: false, Region: "us-east-1", BucketLookup: minio.BucketLookupPath})
			if err != nil {
				t.Fatal(err)
			}
			err = (MinIOLatestBackendReader{Client: client}).ForeignLockAbsent(context.Background(), "state", "staging.tfstate")
			if (err == nil) != tc.want {
				t.Fatalf("lock absence=%v err=%v", tc.want, err)
			}
		})
	}
}
