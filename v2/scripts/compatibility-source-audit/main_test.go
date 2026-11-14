package main

import "testing"

func TestAppendedConstantPreservesExistingFootprint(t *testing.T) {
	old := sourceDeclarations("record.go", []byte("package p\nconst (\n A = iota\n B\n)\n"))
	next := sourceDeclarations("record.go", []byte("package p\nconst (\n A = iota\n B\n C\n)\n"))
	for key, value := range old {
		if next[key] != value {
			t.Fatalf("appending constant changed %s", key)
		}
	}
}

func TestInsertedIotaConstantChangesExistingFootprint(t *testing.T) {
	old := sourceDeclarations("record.go", []byte("package p\nconst (\n A = iota\n B\n)\n"))
	next := sourceDeclarations("record.go", []byte("package p\nconst (\n A = iota\n X\n B\n)\n"))
	if old["record.go:const:B"] == next["record.go:const:B"] {
		t.Fatal("missed shifted iota value")
	}
}

func TestWireTagChangeIsVisible(t *testing.T) {
	old := sourceDeclarations("record.go", []byte("package p\ntype Record struct { ID string `json:\"id\"` }"))
	next := sourceDeclarations("record.go", []byte("package p\ntype Record struct { ID string `json:\"identity\"` }"))
	if hash(old) == hash(next) {
		t.Fatal("missed changed wire encoding")
	}
}
