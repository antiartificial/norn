package model

import "testing"

func TestParseInfraSpecDocumentIsStrictAndSingleDocument(t *testing.T) {
	valid := []byte("name: toy\ndeploy: false\nplacement:\n  nodePool: app\n  distinctHosts: true\nprocesses:\n  web:\n    command: run\n")
	spec, err := ParseInfraSpecDocument(valid)
	if err != nil || spec.EffectiveNodePool() != "app" || !spec.RequiresDistinctHosts() {
		t.Fatalf("parse valid: spec=%#v err=%v", spec, err)
	}
	if _, err := ParseInfraSpecDocument(append(valid, []byte("unknownField: true\n")...)); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := ParseInfraSpecDocument(append(valid, []byte("---\nname: second\n")...)); err == nil {
		t.Fatal("multiple documents accepted")
	}
}
