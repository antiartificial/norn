package githubapp

import (
	"context"
	"testing"
)

func TestVerifyOfflineEffectSourceFixedRepositoryAndProtectedValidatedTip(t *testing.T) {
	f := goodFake()
	f.runRepo = "antiartificial/norn-fleet"
	c := testClient(t, f.handler(t))
	if err := c.VerifyOfflineEffectSource(context.Background(), tipSHA); err == nil {
		t.Fatal("wrong repository accepted")
	}
	c.cfg.Repository = "antiartificial/norn-fleet"
	c.cfg.DefaultBranch = "release"
	if err := c.VerifyOfflineEffectSource(context.Background(), tipSHA); err == nil {
		t.Fatal("non-main source accepted")
	}
	c.cfg.DefaultBranch = "main"
	if err := c.VerifyOfflineEffectSource(context.Background(), tipSHA); err != nil {
		t.Fatalf("protected validated exact source refused: %v", err)
	}
	if err := c.VerifyOfflineEffectSource(context.Background(), baseSHA); err == nil {
		t.Fatal("non-tip source accepted")
	}
	f.protected = false
	if err := c.VerifyOfflineEffectSource(context.Background(), tipSHA); err == nil {
		t.Fatal("unprotected source accepted")
	}
	f.protected = true
	f.runs = nil
	if err := c.VerifyOfflineEffectSource(context.Background(), tipSHA); err == nil {
		t.Fatal("source without exact successful validation accepted")
	}
}
