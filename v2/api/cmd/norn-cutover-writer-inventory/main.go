// norn-cutover-writer-inventory reads an app's stored InfraSpec and regional
// Nomad jobs. It never fences a writer or claims a complete cutover inventory.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"norn/v2/api/model"
	"norn/v2/api/nomad"
)

var appID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "cutover writer inventory:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("norn-cutover-writer-inventory", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	app := flags.String("app", "", "stored application ID")
	apiURL := flags.String("api-url", "", "loopback Norn API base URL")
	nomadURL := flags.String("nomad-url", "", "loopback Nomad API base URL")
	if err := flags.Parse(arguments); err != nil || len(flags.Args()) != 0 || !appID.MatchString(*app) || !loopbackURL(*apiURL) || !loopbackURL(*nomadURL) {
		return errors.New("app and numeric-loopback API and Nomad URLs are required")
	}
	client := &http.Client{Timeout: 10 * time.Second}
	spec, before, err := readSpec(ctx, client, *apiURL, *app)
	if err != nil {
		return err
	}
	if spec.App != *app || !spec.DeclaresDatabase() {
		return errors.New("stored app identity or database declaration is missing")
	}
	nomadClient, err := nomad.NewClient(*nomadURL)
	if err != nil {
		return errors.New("Nomad client unavailable")
	}
	regions := map[string]bool{}
	for _, region := range spec.ResolvedRegions() {
		regions[region.NomadRegion] = true
	}
	names := make([]string, 0, len(regions))
	for region := range regions {
		names = append(names, region)
	}
	sort.Strings(names)
	observed := make([]nomad.CutoverWriterInventory, 0, len(names))
	for _, region := range names {
		inventory, err := nomadClient.ObserveCutoverWriterJobs(ctx, spec, region)
		if err != nil {
			return fmt.Errorf("Nomad writer observation for %s failed: %w", region, err)
		}
		observed = append(observed, inventory)
	}
	_, after, err := readSpec(ctx, client, *apiURL, *app)
	if err != nil || before != after {
		return errors.New("stored app spec changed during observation")
	}
	observationBytes, err := json.Marshal(observed)
	if err != nil {
		return errors.New("Nomad writer observation cannot be encoded")
	}
	observationDigest := sha256.Sum256(observationBytes)
	return json.NewEncoder(output).Encode(struct {
		App                       string                         `json:"app"`
		StoredSpecSHA256          string                         `json:"storedSpecSha256"`
		NomadObservationSHA256    string                         `json:"nomadObservationSha256"`
		Regions                   []nomad.CutoverWriterInventory `json:"regions"`
		ExternalWritersUnverified bool                           `json:"externalWritersUnverified"`
		PromotionReady            bool                           `json:"promotionReady"`
	}{*app, before, hex.EncodeToString(observationDigest[:]), observed, true, false})
}

func loopbackURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.Port() == "" {
		return false
	}
	return parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "::1"
}

func readSpec(ctx context.Context, client *http.Client, baseURL, app string) (*model.InfraSpec, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/api/v1/apps/"+app, nil)
	if err != nil {
		return nil, "", errors.New("app request invalid")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, "", errors.New("local Norn API unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("local Norn API returned %d", response.StatusCode)
	}
	encoded, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil || len(encoded) >= 2<<20 {
		return nil, "", errors.New("stored app response unavailable or oversized")
	}
	var envelope struct {
		Spec json.RawMessage `json:"spec"`
	}
	if json.Unmarshal(encoded, &envelope) != nil || len(envelope.Spec) == 0 || bytes.Equal(envelope.Spec, []byte("null")) {
		return nil, "", errors.New("stored app spec missing")
	}
	var spec model.InfraSpec
	if json.Unmarshal(envelope.Spec, &spec) != nil {
		return nil, "", errors.New("stored app spec invalid")
	}
	digest := sha256.Sum256(envelope.Spec)
	return &spec, hex.EncodeToString(digest[:]), nil
}
