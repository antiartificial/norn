package controller

// The converge watcher (plan-5-7 D5, section 2.4, work package E4a). Every
// Interval it reads the protected default branch tip with an ETag (a 304 costs
// nothing), verifies the tip through githubapp (E3), and records a converge
// plan only for the newest verified tip of each controller-mode resource.
//
// Decision table, per resource, for the tip T and the resource's last applied
// commit L (verdicts come from VerifyProtectedCommit):
//
//	verdict            path class         newest recorded plan    action
//	pending            -                  -                       none (stays pending; re-polled)
//	trigger_unverified -                  -                       none (blocker; re-polled)
//	history_rewritten  -                  -                       none (admin must approve that tip)
//	verified           inert / L == T     -                       none (docs-only or already applied)
//	verified           config/executable  commit == T, admissible none ("exists"; idempotent)
//	verified           config/executable  none, older, refused    record plan for T over L..T
//
// When an unapplied plan for another commit P exists, T must also descend from
// P (otherwise history_rewritten): the watcher never moves a resource backwards.
//
// An older SHA is never recorded: only the tip read this poll, verified at that
// exact SHA, can be recorded, and a range is coalesced into one plan whose path
// class covers all of L..T. The 15 minute no-run grace lives in E3: a tip with
// no push run turns trigger_unverified once its committer date is older than
// githubapp.ValidateRunGrace, and the verdict is recomputed every poll.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"norn/v2/api/fleet/executor"
	"norn/v2/api/githubapp"
)

// DefaultWatchInterval is the tip poll interval (D5).
const DefaultWatchInterval = 60 * time.Second

// CommitSource is the E3 client subset the watcher uses; *githubapp.Client
// implements it.
type CommitSource interface {
	ReadBranchTip(ctx context.Context, etag string) (*githubapp.BranchTip, error)
	VerifyProtectedCommit(ctx context.Context, sha, lastApplied string) (*githubapp.CommitVerification, error)
}

// WatchedResource is one resource with executionMode=controller.
type WatchedResource struct {
	Name       string
	Cluster    string
	Repository string
	// LastApplied is the commit of the last applied converge plan ("" when
	// nothing has been applied).
	LastApplied string
	// DesiredGeneration is the resource's current desired generation; a new
	// plan records DesiredGeneration+1.
	DesiredGeneration int64
}

// WatchedResourceSource lists the controller-mode resources. E6 adapts each
// backend's resource store to it.
type WatchedResourceSource interface {
	ListWatchedResources(ctx context.Context) ([]WatchedResource, error)
}

// ConvergeDesiredSetter, when set, accepts the recorded plan as the
// resource's desired revision. It must be idempotent: the watcher calls it
// again on every evaluation until it succeeds, including when the plan
// already existed, so a failure after recording heals on the next poll.
type ConvergeDesiredSetter interface {
	SetConvergeDesired(ctx context.Context, resource WatchedResource, plan RecordedConverge) error
}

// WatchAction is what the watcher did for one resource.
type WatchAction string

const (
	WatchRecorded         WatchAction = "recorded"
	WatchExists           WatchAction = "exists"
	WatchNone             WatchAction = "none" // inert, docs-only or already applied
	WatchPending          WatchAction = "pending"
	WatchUnverified       WatchAction = "trigger_unverified"
	WatchHistoryRewritten WatchAction = "history_rewritten"
	WatchError            WatchAction = "error"
)

// WatchDecision is the latest outcome for a resource (also the rescan response).
type WatchDecision struct {
	Resource  string      `json:"resource"`
	TipSHA    string      `json:"tipSha"`
	Action    WatchAction `json:"action"`
	PathClass string      `json:"pathClass,omitempty"`
	PlanID    string      `json:"planId,omitempty"`
	Code      string      `json:"code,omitempty"`
	Reason    string      `json:"reason,omitempty"`
}

// ErrNotWatched means the resource is not in controller execution mode.
var ErrNotWatched = errors.New("resource is not watched (executionMode is not controller)")

type settleKey struct {
	tip, lastApplied string
	protected        bool
}

// Watcher polls and records. Zero values select defaults.
type Watcher struct {
	Source   WatchedResourceSource
	GitHub   CommitSource
	Recorder ConvergePlanRecorder
	Desired  ConvergeDesiredSetter
	Clock    Clock
	Interval time.Duration
	// SigningKey signs recorded plans ("" is development, unsigned).
	SigningKey string
	// NewID names new plans; defaults to uuid.
	NewID func() string
	// OnError is told about a failed poll (resource "") or resource evaluation.
	OnError func(resource string, err error)

	run       sync.Mutex // serializes Step and Rescan: no double recording
	mu        sync.Mutex
	etag      string
	tip       *githubapp.BranchTip
	settled   map[string]settleKey
	decisions map[string]WatchDecision
}

func (w *Watcher) clock() Clock {
	if w.Clock == nil {
		return realClock{}
	}
	return w.Clock
}

func (w *Watcher) interval() time.Duration {
	if w.Interval <= 0 {
		return DefaultWatchInterval
	}
	return w.Interval
}

// Decision returns the latest decision for a resource.
func (w *Watcher) Decision(resource string) (WatchDecision, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	d, ok := w.decisions[resource]
	return d, ok
}

// Run polls until ctx is cancelled. It returns promptly on cancellation and
// never records after it: every external call carries ctx, and Step checks it
// before each resource. Stopping loses nothing, because a restarted watcher
// re-derives everything from GitHub and the recorder.
func (w *Watcher) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := w.Step(ctx); err != nil && ctx.Err() == nil && w.OnError != nil {
			w.OnError("", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-w.clock().After(w.interval()):
		}
	}
}

// Step is one poll: read the tip (ETag-conditioned), then evaluate every
// resource that is not already settled for this tip.
func (w *Watcher) Step(ctx context.Context) error {
	w.run.Lock()
	defer w.run.Unlock()
	w.mu.Lock()
	etag := w.etag
	if w.tip == nil {
		etag = "" // never trust an ETag without the tip it describes
	}
	w.mu.Unlock()

	tip, err := w.GitHub.ReadBranchTip(ctx, etag)
	if err != nil {
		return fmt.Errorf("read branch tip: %w", err)
	}
	w.mu.Lock()
	if !tip.NotModified {
		w.tip, w.etag = tip, tip.ETag
	}
	current := w.tip
	w.mu.Unlock()
	if current == nil {
		return nil
	}
	resources, err := w.Source.ListWatchedResources(ctx)
	if err != nil {
		return fmt.Errorf("list watched resources: %w", err)
	}
	w.forget(resources)
	var firstErr error
	for _, r := range resources {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		key := settleKey{tip: current.SHA, lastApplied: r.LastApplied, protected: current.Protected}
		w.mu.Lock()
		done := w.settled[r.Name] == key
		w.mu.Unlock()
		if done {
			continue
		}
		if _, err := w.evaluate(ctx, r, current); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Rescan forces a fresh tip read and evaluates one resource now. It records a
// plan only when the verified tip has no admissible converge plan, so repeating
// it is harmless. It takes no SHA: only the tip read here, verified at that
// SHA, can be recorded. A replay returns the same plan because the recorder key
// is derived from (resource, tip, superseded plan), never from the caller (see
// recordKey); a caller-scoped key would let two callers record two plans.
func (w *Watcher) Rescan(ctx context.Context, resource string) (WatchDecision, error) {
	w.run.Lock()
	defer w.run.Unlock()
	resources, err := w.Source.ListWatchedResources(ctx)
	if err != nil {
		return WatchDecision{}, fmt.Errorf("list watched resources: %w", err)
	}
	var target *WatchedResource
	for i := range resources {
		if resources[i].Name == resource {
			target = &resources[i]
		}
	}
	if target == nil {
		return WatchDecision{}, ErrNotWatched
	}
	tip, err := w.GitHub.ReadBranchTip(ctx, "")
	if err != nil {
		return WatchDecision{}, fmt.Errorf("read branch tip: %w", err)
	}
	w.mu.Lock()
	w.tip, w.etag = tip, tip.ETag
	w.mu.Unlock()
	return w.evaluate(ctx, *target, tip)
}

func (w *Watcher) forget(live []WatchedResource) {
	names := map[string]bool{}
	for _, r := range live {
		names[r.Name] = true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	for name := range w.settled {
		if !names[name] {
			delete(w.settled, name)
			delete(w.decisions, name)
		}
	}
}

func (w *Watcher) finish(r WatchedResource, tip *githubapp.BranchTip, d WatchDecision, settle bool) WatchDecision {
	d.Resource, d.TipSHA = r.Name, tip.SHA
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.decisions == nil {
		w.decisions, w.settled = map[string]WatchDecision{}, map[string]settleKey{}
	}
	w.decisions[r.Name] = d
	if settle {
		w.settled[r.Name] = settleKey{tip: tip.SHA, lastApplied: r.LastApplied, protected: tip.Protected}
	} else {
		delete(w.settled, r.Name)
	}
	return d
}

func (w *Watcher) fail(r WatchedResource, tip *githubapp.BranchTip, err error) (WatchDecision, error) {
	d := w.finish(r, tip, WatchDecision{Action: WatchError, Reason: err.Error()}, false)
	if w.OnError != nil {
		w.OnError(r.Name, err)
	}
	return d, err
}

func (w *Watcher) evaluate(ctx context.Context, r WatchedResource, tip *githubapp.BranchTip) (WatchDecision, error) {
	v, err := w.GitHub.VerifyProtectedCommit(ctx, tip.SHA, r.LastApplied)
	if err != nil {
		return w.fail(r, tip, fmt.Errorf("verify %s: %w", tip.SHA, err))
	}
	switch v.Verdict {
	case githubapp.CommitPending:
		return w.finish(r, tip, WatchDecision{Action: WatchPending, Reason: v.Reason}, false), nil
	case githubapp.CommitUnverified:
		return w.finish(r, tip, WatchDecision{Action: WatchUnverified, Code: v.Code, Reason: v.Reason}, false), nil
	case githubapp.CommitHistoryRewritten:
		return w.finish(r, tip, WatchDecision{Action: WatchHistoryRewritten, Code: v.Code, Reason: v.Reason}, true), nil
	case githubapp.CommitVerified:
	default:
		return w.fail(r, tip, fmt.Errorf("unknown verification verdict %q", v.Verdict))
	}
	if v.PathClass == executor.PathClassInert || r.LastApplied == tip.SHA {
		return w.finish(r, tip, WatchDecision{Action: WatchNone, PathClass: executor.PathClassInert}, true), nil
	}
	if v.PathClass != executor.PathClassConfig && v.PathClass != executor.PathClassExecutable {
		return w.fail(r, tip, fmt.Errorf("unknown path class %q", v.PathClass))
	}
	newest, err := w.Recorder.NewestConvergePlan(ctx, r.Name)
	if err != nil {
		return w.fail(r, tip, fmt.Errorf("read newest converge plan: %w", err))
	}
	if newest != nil && newest.Converge.CommitSHA != tip.SHA && newest.Converge.CommitSHA != r.LastApplied {
		// An unapplied plan for another commit exists. The tip must descend
		// from it: a tip that is behind or diverged from it is a rewritten or
		// reset history, and recording it would move the resource backwards.
		chain, err := w.GitHub.VerifyProtectedCommit(ctx, tip.SHA, newest.Converge.CommitSHA)
		if err != nil {
			return w.fail(r, tip, fmt.Errorf("verify %s over recorded plan %s: %w", tip.SHA, newest.Converge.CommitSHA, err))
		}
		switch chain.Verdict {
		case githubapp.CommitVerified:
		case githubapp.CommitHistoryRewritten:
			return w.finish(r, tip, WatchDecision{Action: WatchHistoryRewritten, Code: chain.Code, Reason: "tip is not a descendant of the newest recorded converge plan's commit"}, true), nil
		case githubapp.CommitPending:
			return w.finish(r, tip, WatchDecision{Action: WatchPending, Reason: chain.Reason}, false), nil
		default:
			// Fail closed: the branch moved between the two reads (the tip is
			// no longer T) or the second read could not verify T.
			return w.finish(r, tip, WatchDecision{Action: WatchUnverified, Code: chain.Code, Reason: chain.Reason}, false), nil
		}
	}
	action := WatchExists
	if newest == nil || newest.Converge.CommitSHA != tip.SHA || !newest.Admissible {
		if ctx.Err() != nil {
			return w.fail(r, tip, ctx.Err())
		}
		previous := ""
		if newest != nil {
			previous = newest.PlanID
		}
		// MergeActor and PullRequest are left empty: E3's verification does
		// not resolve the merge's PR (it needs the commits/{sha}/pulls read).
		// Plan section 2.6 wants both; E6 fills them before going live. They
		// are omitempty, so filling them later changes no existing digest.
		plan, err := BuildConvergePlan(ConvergePlanInput{
			Resource: r.Name, Cluster: r.Cluster, Repository: r.Repository, CommitSHA: tip.SHA, LastApplied: r.LastApplied,
			PathClass: v.PathClass, Generation: r.DesiredGeneration + 1, SigningKey: w.SigningKey, NewID: w.NewID,
		})
		if err != nil {
			return w.fail(r, tip, err)
		}
		newest, err = w.Recorder.RecordConvergePlan(ctx, plan, recordKey(r.Name, tip.SHA, previous))
		if err != nil {
			return w.fail(r, tip, fmt.Errorf("record converge plan: %w", err))
		}
		action = WatchRecorded
	}
	if w.Desired != nil {
		if err := w.Desired.SetConvergeDesired(ctx, r, *newest); err != nil {
			return w.fail(r, tip, fmt.Errorf("set desired revision: %w", err))
		}
	}
	return w.finish(r, tip, WatchDecision{Action: action, PathClass: v.PathClass, PlanID: newest.PlanID}, true), nil
}

// recordKey makes recording idempotent per (resource, tip, plan it supersedes),
// derived only from durable facts and never from a caller: the poll, every
// rescan caller and a second watcher briefly running during an authority
// restart all derive the same key, so the recorder's storage-enforced key
// (PG: the acceptance unique index) admits one plan. A plan recorded after the
// previous one was refused supersedes a different plan and gets a new key.
func recordKey(resource, sha, previousPlanID string) string {
	sum := sha256.Sum256([]byte(resource + "\x00" + sha + "\x00" + previousPlanID))
	return "converge:" + hex.EncodeToString(sum[:16])
}
