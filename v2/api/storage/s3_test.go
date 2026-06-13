package storage

import (
	"testing"

	"norn/v2/api/model"
)

func TestApplyBucketEnvSetsDefaultAndNamedBuckets(t *testing.T) {
	env := map[string]string{}

	applyBucketEnv(env, model.ObjectStorageBucket{
		Name:   "omniphore-media",
		Prefix: "prod/",
		Env:    "MEDIA",
	}, 0)
	applyBucketEnv(env, model.ObjectStorageBucket{
		Name: "omniphore-snapshots",
	}, 1)

	assertEnv(t, env, "S3_BUCKET", "omniphore-media")
	assertEnv(t, env, "S3_PREFIX", "prod/")
	assertEnv(t, env, "S3_BUCKET_MEDIA", "omniphore-media")
	assertEnv(t, env, "S3_PREFIX_MEDIA", "prod/")
	assertEnv(t, env, "S3_BUCKET_OMNIPHORE_SNAPSHOTS", "omniphore-snapshots")
}

func assertEnv(t *testing.T, env map[string]string, key, want string) {
	t.Helper()
	if got := env[key]; got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}
