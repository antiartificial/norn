package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"norn/v2/api/fleet/executor"
)

// Commit verification for the controller-run executor lane (plan-5-7 D5,
// section 2.4, work package E3). It uses only permissions the App already
// holds: contents:read (branch, compare) and actions:read (workflow runs).
// Administration, Checks and Statuses are not used; required reviews are
// therefore not verified (D2 covers executable changes).

const (
	// ValidateWorkflow is the workflow whose push run proves a commit passed
	// validation. A renamed or missing workflow fails closed.
	ValidateWorkflow = "validate.yml"

	// ValidateRunGrace bounds how long a tip with no validate.yml push run at
	// all stays pending. validate.yml has no push path filters, so a missing
	// run means CI was skipped ([skip ci] etc.) or Actions are disabled; after
	// the grace it is trigger_unverified. A run that has started (queued or
	// in progress) stays pending without a bound. Re-evaluated every poll.
	ValidateRunGrace = 15 * time.Minute

	// compareMaxFiles and compareMaxCommits are GitHub's compare ceilings. A
	// diff that reaches either may be truncated, so it fails closed.
	compareMaxFiles   = 300
	compareMaxCommits = 250
	comparePerPage    = 100
)

// CommitVerdict is the outcome of VerifyProtectedCommit.
type CommitVerdict string

const (
	// CommitVerified: protected, validated at the SHA, descends from last applied.
	CommitVerified CommitVerdict = "verified"
	// CommitPending: validation has not finished. Re-poll; not a failure.
	CommitPending CommitVerdict = "pending"
	// CommitUnverified: blocker reason trigger_unverified.
	CommitUnverified CommitVerdict = "trigger_unverified"
	// CommitHistoryRewritten: blocker reason history_rewritten; an admin must
	// approve that exact tip. Blocker reason constants belong to
	// fleet/controller (E4b, ADR 0010), so the verdict carries the string.
	CommitHistoryRewritten CommitVerdict = "history_rewritten"
)

// CommitVerification is the result. Err-free results carry a verdict; only
// transport and decoding failures are returned as errors.
type CommitVerification struct {
	SHA     string        `json:"sha"`
	Verdict CommitVerdict `json:"verdict"`
	// Code is the admission refusal code: executor.CodeFleetExecutorTriggerUnverified
	// for CommitUnverified and CommitHistoryRewritten, empty otherwise.
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
	// PathClass is set when Verdict is CommitVerified: executor.PathClassConfig,
	// executor.PathClassExecutable or executor.PathClassInert. With no lastApplied it is
	// executable (the first converge needs approve).
	PathClass    string `json:"pathClass,omitempty"`
	ChangedPaths int    `json:"changedPaths,omitempty"`
	ValidateRun  int64  `json:"validateRunId,omitempty"`
}

// BranchTip is one ETag-conditioned read of the default branch.
type BranchTip struct {
	// NotModified is true on a 304: no work. Other fields except ETag are empty.
	NotModified bool
	SHA         string
	Protected   bool
	ETag        string
	// CommittedAt is the tip's committer date (zero if absent); it only
	// bounds how long a missing validate run stays pending.
	CommittedAt time.Time
}

// ReadBranchTip reads GET /repos/{r}/branches/{default} with If-None-Match.
// Pass the previous BranchTip.ETag (or "") on each poll.
func (c *Client) ReadBranchTip(ctx context.Context, etag string) (*BranchTip, error) {
	token, err := c.installationToken(ctx, map[string]string{"actions": "read", "contents": "read"})
	if err != nil {
		return nil, err
	}
	return c.readBranchTip(ctx, token, etag)
}

func (c *Client) readBranchTip(ctx context.Context, token, etag string) (*BranchTip, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.APIBaseURL+c.repoPath("/branches/"+url.PathEscape(c.cfg.DefaultBranch)), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", apiVersion)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return &BranchTip{NotModified: true, ETag: etag}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &apiError{Status: resp.StatusCode, Message: http.StatusText(resp.StatusCode)}
	}
	var body struct {
		Commit struct {
			SHA    string `json:"sha"`
			Commit struct {
				Committer struct {
					Date time.Time `json:"date"`
				} `json:"committer"`
			} `json:"commit"`
		} `json:"commit"`
		Protected bool `json:"protected"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode GitHub response: %w", err)
	}
	if !commitSHARe.MatchString(body.Commit.SHA) {
		return nil, fmt.Errorf("GitHub returned an invalid branch tip")
	}
	return &BranchTip{SHA: body.Commit.SHA, Protected: body.Protected, ETag: resp.Header.Get("ETag"), CommittedAt: body.Commit.Commit.Committer.Date}, nil
}

// VerifyProtectedCommit verifies that sha is the current protected default
// branch tip, passed validate.yml on push at exactly that SHA, and is a
// descendant of lastApplied ("" means nothing applied yet). It also returns
// the default-deny path class of lastApplied...sha.
func (c *Client) VerifyProtectedCommit(ctx context.Context, sha, lastApplied string) (*CommitVerification, error) {
	if !commitSHARe.MatchString(sha) || (lastApplied != "" && !commitSHARe.MatchString(lastApplied)) {
		return nil, fmt.Errorf("commit SHA is invalid")
	}
	token, err := c.installationToken(ctx, map[string]string{"actions": "read", "contents": "read"})
	if err != nil {
		return nil, err
	}
	out := &CommitVerification{SHA: sha}
	unverified := func(format string, args ...any) (*CommitVerification, error) {
		out.Verdict, out.Code, out.Reason = CommitUnverified, executor.CodeFleetExecutorTriggerUnverified, fmt.Sprintf(format, args...)
		return out, nil
	}

	tip, err := c.readBranchTip(ctx, token, "")
	if err != nil {
		return nil, err
	}
	if tip.SHA != sha {
		return unverified("commit is not the %s branch tip", c.cfg.DefaultBranch)
	}
	if !tip.Protected {
		return unverified("branch %s is not protected", c.cfg.DefaultBranch)
	}

	runID, state, err := c.validateRunState(ctx, token, sha)
	if err != nil {
		return nil, err
	}
	switch state {
	case validateMissingWorkflow:
		return unverified("%s workflow not found", ValidateWorkflow)
	case validateFailed:
		return unverified("%s push run at this commit did not succeed", ValidateWorkflow)
	case validatePending:
		out.Verdict, out.Reason = CommitPending, ValidateWorkflow+" push run at this commit has not completed"
		return out, nil
	case validateNoRun:
		// A zero or future-dated committer date only delays or hastens a
		// blocker; it can never verify.
		if !tip.CommittedAt.IsZero() && c.now().Sub(tip.CommittedAt) < ValidateRunGrace {
			out.Verdict, out.Reason = CommitPending, ValidateWorkflow+" push run at this commit has not started"
			return out, nil
		}
		return unverified("no %s push run at this commit after %s (CI skipped or Actions disabled)", ValidateWorkflow, ValidateRunGrace)
	}
	out.ValidateRun = runID

	if lastApplied == "" {
		out.Verdict, out.PathClass, out.Reason = CommitVerified, executor.PathClassExecutable, "nothing applied yet; first converge needs approve"
		return out, nil
	}
	if lastApplied == sha {
		out.Verdict, out.PathClass = CommitVerified, executor.PathClassInert
		return out, nil
	}
	status, class, n, err := c.compareClass(ctx, token, lastApplied, sha)
	if errors.Is(err, errCompareUnrelated) {
		out.Verdict, out.Code, out.Reason = CommitHistoryRewritten, executor.CodeFleetExecutorTriggerUnverified, "last applied commit is not comparable with the tip"
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if status != "ahead" && status != "identical" {
		out.Verdict, out.Code, out.Reason = CommitHistoryRewritten, executor.CodeFleetExecutorTriggerUnverified, "tip is "+status+" relative to the last applied commit"
		return out, nil
	}
	out.Verdict, out.PathClass, out.ChangedPaths = CommitVerified, class, n
	return out, nil
}

type validateState int

const (
	validateSucceeded validateState = iota
	validatePending
	validateFailed
	validateMissingWorkflow
	validateNoRun
)

func (c *Client) validateRunState(ctx context.Context, token, sha string) (int64, validateState, error) {
	var runs struct {
		WorkflowRuns []struct {
			ID         int64  `json:"id"`
			HeadSHA    string `json:"head_sha"`
			HeadBranch string `json:"head_branch"`
			Event      string `json:"event"`
			Path       string `json:"path"`
			Status     string `json:"status"`
			HeadRepo   struct {
				FullName string `json:"full_name"`
			} `json:"head_repository"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	query := "?event=push&branch=" + url.QueryEscape(c.cfg.DefaultBranch) + "&head_sha=" + sha + "&per_page=100"
	err := c.request(ctx, token, http.MethodGet, c.repoPath("/actions/workflows/"+url.PathEscape(ValidateWorkflow)+"/runs"+query), nil, &runs)
	if apiErr := new(apiError); errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return 0, validateMissingWorkflow, nil
	}
	if err != nil {
		return 0, 0, err
	}
	pending, failed := false, false
	for _, run := range runs.WorkflowRuns {
		// The listing is filtered server side, but never trust the filter.
		if run.HeadSHA != sha || run.Event != "push" || run.HeadBranch != c.cfg.DefaultBranch || run.HeadRepo.FullName != c.cfg.Repository {
			continue
		}
		if wf := ".github/workflows/" + ValidateWorkflow; run.Path != wf && !strings.HasPrefix(run.Path, wf+"@") {
			continue
		}
		switch {
		case run.Status != "completed":
			pending = true
		case run.Conclusion == "success":
			return run.ID, validateSucceeded, nil
		default:
			failed = true
		}
	}
	switch {
	case pending:
		return 0, validatePending, nil
	case failed:
		return 0, validateFailed, nil
	}
	// No run at this SHA: not started yet, or never will be (bounded by the caller).
	return 0, validateNoRun, nil
}

var errCompareUnrelated = errors.New("commits are not comparable")

// compareClass returns the compare status and the path class over every file
// of base...head. A truncated, oversized or unparseable diff is executable.
func (c *Client) compareClass(ctx context.Context, token, base, head string) (string, string, int, error) {
	var status string
	var paths []string
	truncated := false
	for page := 1; ; page++ {
		var resp struct {
			Status  string `json:"status"`
			AheadBy int    `json:"ahead_by"`
			Files   []struct {
				Filename string `json:"filename"`
				Previous string `json:"previous_filename"`
			} `json:"files"`
		}
		endpoint := c.repoPath(fmt.Sprintf("/compare/%s...%s?per_page=%d&page=%d", base, head, comparePerPage, page))
		err := c.request(ctx, token, http.MethodGet, endpoint, nil, &resp)
		if apiErr := new(apiError); errors.As(err, &apiErr) && (apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusUnprocessableEntity) {
			// A force-pushed-away base no longer resolves.
			return "", "", 0, errCompareUnrelated
		}
		if err != nil {
			return "", "", 0, err
		}
		status = resp.Status
		if status != "ahead" && status != "identical" {
			return status, "", 0, nil
		}
		if resp.AheadBy >= compareMaxCommits {
			truncated = true
		}
		for _, f := range resp.Files {
			paths = append(paths, f.Filename)
			if f.Previous != "" {
				paths = append(paths, f.Previous)
			}
		}
		if len(resp.Files) < comparePerPage {
			break
		}
		if len(paths) >= compareMaxFiles {
			truncated = true
			break
		}
	}
	if len(paths) >= compareMaxFiles {
		truncated = true
	}
	if truncated {
		return status, executor.PathClassExecutable, len(paths), nil
	}
	return status, ClassifyPaths(paths), len(paths), nil
}

// ClassifyPaths is the default-deny path classifier (plan-5-7 section 2.5).
// Any path outside the config allow list or the docs list is executable, so
// one executable path makes the whole range executable. An empty list is inert.
//
// config is exactly environments/<env>/<region>/cluster.yaml (schema-checked by
// validate.yml; its values are inventory, never code) and
// .norn/fleet-plan-receipts/<id>.json. *.tfvars is not config: none is
// committed, and a committed *.auto.tfvars would be auto-loaded over TF_VAR_*
// (ssh_key_fingerprints, admin_cidrs). inert is *.md outside .github/; the rest
// of docs/ is not inert because scripts read docs/policies/*.json and
// docs/incidents/*.json.
func ClassifyPaths(paths []string) string {
	class := executor.PathClassInert
	for _, p := range paths {
		switch classifyPath(p) {
		case executor.PathClassExecutable:
			return executor.PathClassExecutable
		case executor.PathClassConfig:
			class = executor.PathClassConfig
		}
	}
	return class
}

func classifyPath(p string) string {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return executor.PathClassExecutable
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return executor.PathClassExecutable
		}
	}
	parts := strings.Split(p, "/")
	switch {
	case len(parts) == 4 && parts[0] == "environments" && parts[3] == "cluster.yaml":
		return executor.PathClassConfig
	case len(parts) == 3 && parts[0] == ".norn" && parts[1] == "fleet-plan-receipts" && path.Ext(parts[2]) == ".json" && len(parts[2]) > len(".json"):
		return executor.PathClassConfig
	case path.Ext(p) == ".md" && parts[0] != ".github":
		return executor.PathClassInert
	}
	return executor.PathClassExecutable
}
