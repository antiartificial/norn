package etcdstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"norn/v2/api/store"
)

// AccessPatternStore is an etcd-backed store.AccessPatternStore. Each hour
// bucket is a single JSON value under
// prefix/access/<app>\x00<process>\x00<endpoint>\x00<source>\x00<bucketUnix>,
// mirroring the PostgreSQL unique key (app, process, endpoint, source,
// date_trunc('hour', observed_at)). RecordAccessObservation accumulates onto the
// bucket via compare-and-swap; ListAccessPatternRows scans and re-groups by
// hour-of-day/weekday exactly as the SQL GROUP BY does. It passes the shared
// conformance suite.
type AccessPatternStore struct {
	kv     clientv3.KV
	prefix string
}

// NewAccessPatternStore returns an etcd access-pattern store rooted at prefix.
func NewAccessPatternStore(kv clientv3.KV, prefix string) *AccessPatternStore {
	return &AccessPatternStore{kv: kv, prefix: prefix}
}

var _ store.AccessPatternStore = (*AccessPatternStore)(nil)

func (s *AccessPatternStore) bucketsPrefix() string { return s.prefix + "/access/" }

func (s *AccessPatternStore) key(app, process, endpoint, source string, bucketStart time.Time) string {
	return fmt.Sprintf("%s%s\x00%s\x00%s\x00%s\x00%d", s.bucketsPrefix(), app, process, endpoint, source, bucketStart.UTC().Unix())
}

type accessBucket struct {
	App          string    `json:"app"`
	Process      string    `json:"process"`
	Endpoint     string    `json:"endpoint"`
	Source       string    `json:"source"`
	BucketStart  time.Time `json:"bucketStart"`
	Requests     int64     `json:"requests"`
	Successes    int64     `json:"successes"`
	ClientErrors int64     `json:"clientErrors"`
	ServerErrors int64     `json:"serverErrors"`
	FirstSeen    time.Time `json:"firstSeen"`
	LastSeen     time.Time `json:"lastSeen"`
}

// normalizeAccessObservation and statusBuckets replicate the (unexported)
// PostgreSQL adapter helpers so the two backends bucket identically; the shared
// conformance suite guards against drift.
func normalizeAccessObservation(obs store.AccessObservation) store.AccessObservation {
	obs.App = strings.TrimSpace(obs.App)
	obs.Process = strings.TrimSpace(obs.Process)
	obs.Endpoint = strings.TrimSpace(obs.Endpoint)
	obs.Source = strings.TrimSpace(obs.Source)
	if obs.Source == "" {
		obs.Source = "external"
	}
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = time.Now().UTC()
	}
	obs.ObservedAt = obs.ObservedAt.UTC()
	if obs.Count <= 0 {
		obs.Count = 1
	}
	return obs
}

func statusBuckets(count int64, status int) (successes, clientErrors, serverErrors int64) {
	switch {
	case status >= 500:
		return 0, 0, count
	case status >= 400:
		return 0, count, 0
	default:
		return count, 0, 0
	}
}

func (s *AccessPatternStore) upsert(ctx context.Context, obs store.AccessObservation, accumulate bool) error {
	obs = normalizeAccessObservation(obs)
	succ, ce, se := statusBuckets(obs.Count, obs.Status)
	bucketStart := obs.ObservedAt.Truncate(time.Hour)
	key := s.key(obs.App, obs.Process, obs.Endpoint, obs.Source, bucketStart)
	for attempt := 0; attempt < 32; attempt++ {
		resp, err := s.kv.Get(ctx, key)
		if err != nil {
			return err
		}
		var (
			b   accessBucket
			rev int64
		)
		if len(resp.Kvs) > 0 {
			if err := json.Unmarshal(resp.Kvs[0].Value, &b); err != nil {
				return err
			}
			rev = resp.Kvs[0].ModRevision
		}
		if len(resp.Kvs) == 0 || !accumulate {
			b = accessBucket{
				App: obs.App, Process: obs.Process, Endpoint: obs.Endpoint, Source: obs.Source,
				BucketStart:  bucketStart,
				Requests:     obs.Count,
				Successes:    succ,
				ClientErrors: ce,
				ServerErrors: se,
				FirstSeen:    obs.ObservedAt,
				LastSeen:     obs.ObservedAt,
			}
		} else {
			b.Requests += obs.Count
			b.Successes += succ
			b.ClientErrors += ce
			b.ServerErrors += se
			if obs.ObservedAt.Before(b.FirstSeen) {
				b.FirstSeen = obs.ObservedAt
			}
			if obs.ObservedAt.After(b.LastSeen) {
				b.LastSeen = obs.ObservedAt
			}
		}
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		cmp := clientv3.Compare(clientv3.ModRevision(key), "=", rev)
		txn, err := s.kv.Txn(ctx).If(cmp).Then(clientv3.OpPut(key, string(raw))).Commit()
		if err != nil {
			return err
		}
		if txn.Succeeded {
			return nil
		}
	}
	return fmt.Errorf("record access observation: exhausted retries under contention")
}

func (s *AccessPatternStore) RecordAccessObservation(ctx context.Context, obs store.AccessObservation) error {
	return s.upsert(ctx, obs, true)
}

func (s *AccessPatternStore) ReplaceAccessObservation(ctx context.Context, obs store.AccessObservation) error {
	return s.upsert(ctx, obs, false)
}

func (s *AccessPatternStore) scan(ctx context.Context) ([]accessBucket, error) {
	resp, err := s.kv.Get(ctx, s.bucketsPrefix(), clientv3.WithPrefix())
	if err != nil {
		return nil, err
	}
	out := make([]accessBucket, 0, len(resp.Kvs))
	for _, kv := range resp.Kvs {
		var b accessBucket
		if err := json.Unmarshal(kv.Value, &b); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (s *AccessPatternStore) ListAccessPatternRows(ctx context.Context, since time.Time) ([]store.AccessPatternRow, error) {
	buckets, err := s.scan(ctx)
	if err != nil {
		return nil, err
	}
	since = since.UTC()
	type key struct {
		app, process, endpoint, source string
		hour, weekday                  int
	}
	agg := map[key]*store.AccessPatternRow{}
	for _, b := range buckets {
		if b.BucketStart.Before(since) {
			continue
		}
		k := key{b.App, b.Process, b.Endpoint, b.Source, b.BucketStart.UTC().Hour(), int(b.BucketStart.UTC().Weekday())}
		row, ok := agg[k]
		if !ok {
			row = &store.AccessPatternRow{
				App: b.App, Process: b.Process, Endpoint: b.Endpoint, Source: b.Source,
				Hour: k.hour, Weekday: k.weekday,
				FirstSeen: b.FirstSeen, LastSeen: b.LastSeen,
			}
			agg[k] = row
		}
		row.Requests += b.Requests
		row.Successes += b.Successes
		row.ClientErrors += b.ClientErrors
		row.ServerErrors += b.ServerErrors
		if b.FirstSeen.Before(row.FirstSeen) {
			row.FirstSeen = b.FirstSeen
		}
		if b.LastSeen.After(row.LastSeen) {
			row.LastSeen = b.LastSeen
		}
	}
	out := make([]store.AccessPatternRow, 0, len(agg))
	for _, row := range agg {
		out = append(out, *row)
	}
	// ORDER BY app, process, requests DESC.
	sort.Slice(out, func(i, j int) bool {
		if out[i].App != out[j].App {
			return out[i].App < out[j].App
		}
		if out[i].Process != out[j].Process {
			return out[i].Process < out[j].Process
		}
		return out[i].Requests > out[j].Requests
	})
	return out, nil
}

func (s *AccessPatternStore) PruneAccessObservations(ctx context.Context, olderThan time.Time) error {
	buckets, err := s.kv.Get(ctx, s.bucketsPrefix(), clientv3.WithPrefix())
	if err != nil {
		return err
	}
	olderThan = olderThan.UTC()
	for _, kv := range buckets.Kvs {
		var b accessBucket
		if err := json.Unmarshal(kv.Value, &b); err != nil {
			return err
		}
		if b.BucketStart.Before(olderThan) {
			if _, err := s.kv.Delete(ctx, string(kv.Key)); err != nil {
				return err
			}
		}
	}
	return nil
}
