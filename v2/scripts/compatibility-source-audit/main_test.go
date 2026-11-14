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

func TestCatalogTypeComparisonIncludesExactJSONTagsAndTypes(t *testing.T) {
	got := astCatalogType("HEAD", catalogSourceType{"model.OperationReceipt", "model", "OperationReceipt", "v2/api/model/operation.go"})
	want := got
	want.Fields = append([]catalogField(nil), got.Fields...)
	for index := range want.Fields {
		if want.Fields[index].Name == "platform" {
			want.Fields[index].JSONTag = `json:"platform"`
		}
	}
	if sameCatalogType(got, want) {
		t.Fatal("catalog comparison ignored the source omitempty tag")
	}
	want = got
	want.Fields = append([]catalogField(nil), got.Fields...)
	for index := range want.Fields {
		if want.Fields[index].Name == "platform" {
			want.Fields[index].Type = "*model.OtherReceipt"
		}
	}
	if sameCatalogType(got, want) {
		t.Fatal("catalog comparison ignored the source field type")
	}
}

func TestOperationStatusCatalogValuesMatchGoConstants(t *testing.T) {
	want := []string{"canceled", "failed", "queued", "running", "succeeded"}
	if got := operationStatusValues("HEAD"); !sameStrings(got, want) {
		t.Fatalf("OperationStatus values = %v, want %v", got, want)
	}
}

func TestCatalogClosureRejectsMissingReferencedType(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("catalog closure accepted an uncataloged referenced type")
		}
	}()
	validateCatalogClosure(schemaCatalog{Types: []catalogType{
		{ID: "etcdstore.v3Record", Fields: []catalogField{{Name: "operation", Type: "model.Missing", JSONTag: `json:"operation"`}}},
		{ID: "fleet.CapacityPlan"}, {ID: "fleet.PlanRequest"},
		{ID: "model.Operation"}, {ID: "model.OperationReceipt"},
	}})
}
