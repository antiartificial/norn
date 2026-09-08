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

func TestParseInfraSpecDocumentRejectsConfigurableNomadVariablePathOrMode(t *testing.T) {
	for name, document := range map[string][]byte{
		"path": []byte(`name: pilot
processes:
  web:
    nomadVariables:
      path: nomad/jobs/other-job
      uid: 65532
      gid: 65532
      files:
        - key: MYSQL_DSN
          destination: mysql-dsn
`),
		"mode": []byte(`name: pilot
processes:
  web:
    nomadVariables:
      uid: 65532
      gid: 65532
      files:
        - key: MYSQL_DSN
          destination: mysql-dsn
          mode: "0600"
`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseInfraSpecDocument(document); err == nil {
				t.Fatal("accepted configurable Nomad variable template control")
			}
		})
	}
}
