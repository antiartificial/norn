package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

func git(a ...string) []byte {
	b, e := exec.Command("git", a...).Output()
	if e != nil {
		panic(e)
	}
	return b
}
func files(ref string) []string {
	var out []string
	for _, p := range strings.Split(strings.TrimSpace(string(git("ls-tree", "-r", "--name-only", ref, "--", "v2/api"))), "\n") {
		if strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") && !strings.Contains(p, "/testdata/") {
			out = append(out, p)
		}
	}
	return out
}
func declarations(ref, p string) map[string]string {
	return sourceDeclarations(p, git("show", ref+":"+p))
}

func sourceDeclarations(p string, src []byte) map[string]string {
	fs := token.NewFileSet()
	f, e := parser.ParseFile(fs, p, src, 0)
	if e != nil {
		panic(e)
	}
	out := map[string]string{}
	for _, d := range f.Decls {
		g, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		if g.Tok == token.TYPE {
			for _, s := range g.Specs {
				t := s.(*ast.TypeSpec)
				var b bytes.Buffer
				if e := printer.Fprint(&b, fs, t); e != nil {
					panic(e)
				}
				out[p+":type:"+t.Name.Name] = b.String()
			}
		}
		if g.Tok == token.CONST {
			for i, spec := range g.Specs {
				prefix := &ast.GenDecl{Tok: token.CONST, Lparen: g.Lparen, Specs: g.Specs[:i+1], Rparen: g.Rparen}
				var b bytes.Buffer
				if e := printer.Fprint(&b, fs, prefix); e != nil {
					panic(e)
				}
				for _, name := range spec.(*ast.ValueSpec).Names {
					out[p+":const:"+name.Name] = b.String()
				}
			}
		}
	}
	return out
}
func hash(v map[string]string) string {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

type catalogField struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	JSONTag string `json:"jsonTag"`
}

type catalogType struct {
	ID            string         `json:"id"`
	SchemaVersion string         `json:"schemaVersion,omitempty"`
	Fields        []catalogField `json:"fields"`
}

type schemaCatalog struct {
	SchemaVersion         string              `json:"schemaVersion"`
	CatalogRevision       int                 `json:"catalogRevision"`
	BaselineSourceSHA     string              `json:"baselineSourceSHA"`
	APIMajor              int                 `json:"apiMajor"`
	PlanKind              string              `json:"planKind"`
	APIEndpoints          []map[string]string `json:"apiEndpoints"`
	DynamicMapProjections map[string]any      `json:"dynamicMapProjections"`
	Types                 []catalogType       `json:"types"`
	Enums                 []struct {
		ID     string   `json:"id"`
		Values []string `json:"values"`
	} `json:"enums"`
}

type catalogSourceType struct {
	ID, Package, Name, File string
}

var catalogSources = []catalogSourceType{
	{"etcdstore.v3Record", "etcdstore", "v3Record", "v2/api/etcdstore/v3_operation_store.go"},
	{"fleet.CapacityPlan", "fleet", "CapacityPlan", "v2/api/fleet/schema.go"},
	{"fleet.Finding", "fleet", "Finding", "v2/api/fleet/schema.go"},
	{"fleet.NodePool", "fleet", "NodePool", "v2/api/fleet/schema.go"},
	{"fleet.PlanRequest", "fleet", "PlanRequest", "v2/api/fleet/schema.go"},
	{"fleet.ProtectedApplyPrestate", "fleet", "ProtectedApplyPrestate", "v2/api/fleet/schema.go"},
	{"fleet.ProtectedApplyStateSpaces", "fleet", "ProtectedApplyStateSpaces", "v2/api/fleet/schema.go"},
	{"fleet.ProtectedApplyTransitionRule", "fleet", "ProtectedApplyTransitionRule", "v2/api/fleet/schema.go"},
	{"fleet.Replacement", "fleet", "Replacement", "v2/api/fleet/schema.go"},
	{"model.AppOperationReceipt", "model", "AppOperationReceipt", "v2/api/model/operation.go"},
	{"model.FleetOperationReceipt", "model", "FleetOperationReceipt", "v2/api/model/operation.go"},
	{"model.HostAssuranceReceipt", "model", "HostAssuranceReceipt", "v2/api/model/operation.go"},
	{"model.Operation", "model", "Operation", "v2/api/model/operation.go"},
	{"model.OperationReceipt", "model", "OperationReceipt", "v2/api/model/operation.go"},
	{"model.PlatformOperationReceipt", "model", "PlatformOperationReceipt", "v2/api/model/operation.go"},
}

func expression(ref *token.FileSet, node ast.Node) string {
	var b bytes.Buffer
	if err := printer.Fprint(&b, ref, node); err != nil {
		panic(err)
	}
	return b.String()
}

func packageLocalTypes(pkg string) map[string]bool {
	names := map[string]bool{}
	for _, source := range catalogSources {
		if source.Package == pkg {
			names[source.Name] = true
		}
	}
	if pkg == "model" {
		names["OperationStatus"] = true
	}
	return names
}

func astCatalogType(ref string, source catalogSourceType) catalogType {
	filename := source.File
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, filename, git("show", ref+":"+filename), 0)
	if err != nil {
		panic(err)
	}
	var result *ast.StructType
	for _, declaration := range f.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.TYPE {
			continue
		}
		for _, item := range group.Specs {
			typeSpec := item.(*ast.TypeSpec)
			if typeSpec.Name.Name == source.Name {
				result, _ = typeSpec.Type.(*ast.StructType)
			}
		}
	}
	if result == nil {
		panic("catalog source type is not a struct: " + source.ID)
	}
	local := packageLocalTypes(source.Package)
	fields := []catalogField{}
	for _, field := range result.Fields.List {
		if len(field.Names) != 1 || field.Tag == nil {
			panic("catalog source has an embedded, grouped, or untagged field: " + source.ID)
		}
		tag, err := strconv.Unquote(field.Tag.Value)
		if err != nil {
			panic("invalid struct tag in " + source.ID)
		}
		jsonTag := ""
		for _, item := range strings.Split(tag, " ") {
			if strings.HasPrefix(item, "json:") {
				jsonTag = item
				break
			}
		}
		if jsonTag == "" {
			panic("catalog source field has no JSON tag: " + source.ID + "." + field.Names[0].Name)
		}
		jsonValue, err := strconv.Unquote(strings.TrimPrefix(jsonTag, "json:"))
		if err != nil {
			panic("invalid JSON struct tag in " + source.ID)
		}
		jsonName := strings.SplitN(jsonValue, ",", 2)[0]
		if jsonName == "" {
			jsonName = field.Names[0].Name
		} else if jsonName == "-" {
			fieldName := field.Names[0].Name
			jsonName = strings.ToLower(fieldName[:1]) + fieldName[1:]
		}
		typeName := expression(fs, field.Type)
		for name := range local {
			typeName = strings.ReplaceAll(typeName, "*"+name, "*"+source.Package+"."+name)
			typeName = strings.ReplaceAll(typeName, "[]"+name, "[]"+source.Package+"."+name)
			typeName = strings.ReplaceAll(typeName, "["+name, "["+source.Package+"."+name)
			typeName = strings.ReplaceAll(typeName, " "+name, " "+source.Package+"."+name)
			if typeName == name {
				typeName = source.Package + "." + name
			}
		}
		fields = append(fields, catalogField{Name: jsonName, Type: typeName, JSONTag: jsonTag})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	item := catalogType{ID: source.ID, Fields: fields}
	if source.ID == "fleet.CapacityPlan" {
		item.SchemaVersion = "norn.fleet-capacity-plan/v1"
	}
	if source.ID == "model.OperationReceipt" {
		item.SchemaVersion = "norn.operation-receipt/v1"
	}
	return item
}

func operationStatusValues(ref string) []string {
	filename := "v2/api/model/operation.go"
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, filename, git("show", ref+":"+filename), 0)
	if err != nil {
		panic(err)
	}
	values := []string{}
	for _, declaration := range f.Decls {
		group, ok := declaration.(*ast.GenDecl)
		if !ok || group.Tok != token.CONST {
			continue
		}
		for _, item := range group.Specs {
			spec := item.(*ast.ValueSpec)
			if typeName, ok := spec.Type.(*ast.Ident); !ok || typeName.Name != "OperationStatus" {
				continue
			}
			for _, value := range spec.Values {
				literal, ok := value.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					panic("OperationStatus values must be string literals")
				}
				decoded, err := strconv.Unquote(literal.Value)
				if err != nil {
					panic("invalid OperationStatus string")
				}
				values = append(values, decoded)
			}
		}
	}
	sort.Strings(values)
	return values
}

func verifyCatalog(oldRef, targetRef, filename string) map[string]any {
	raw, err := os.ReadFile(filename)
	if err != nil {
		panic(err)
	}
	var declaration map[string]json.RawMessage
	if err := json.Unmarshal(raw, &declaration); err != nil {
		panic("invalid compatibility declaration: " + err.Error())
	}
	var catalog schemaCatalog
	if err := json.Unmarshal(declaration["schemaCatalog"], &catalog); err != nil {
		panic("invalid schema catalog: " + err.Error())
	}
	oldSHA := strings.TrimSpace(string(git("rev-parse", "--verify", oldRef+"^{commit}")))
	if catalog.SchemaVersion != "norn.online-fleet-plan-schema-catalog/v1" || catalog.CatalogRevision != 1 || catalog.BaselineSourceSHA != oldSHA || catalog.APIMajor != 1 {
		panic("catalog identity, revision, baseline or API major differs")
	}
	if catalog.PlanKind != "fleet.capacity-plan" || !reflect.DeepEqual(catalog.APIEndpoints, []map[string]string{
		{"method": "GET", "path": "/api/v1/fleet/plans", "response": "fleet.PlanListResponse{plans:[]model.Operation,count:int}"},
		{"method": "POST", "path": "/api/v1/fleet/node-pools/{pool}/plan", "request": "fleet.PlanRequest", "response": "model.Operation"},
	}) {
		panic("catalog API scope differs from the reviewed capacity-plan endpoints")
	}
	expectedDynamic := map[string]any{
		"model.Operation.payload": map[string]any{"kind": "fleet.capacity-plan", "schemaType": "fleet.CapacityPlan"},
		"model.Operation.metadata": map[string]any{
			"keys":       []any{"planDigest", "planId", "signature"},
			"valueTypes": map[string]any{"planDigest": "string", "planId": "string", "signature": "string"},
		},
	}
	if !reflect.DeepEqual(catalog.DynamicMapProjections, expectedDynamic) {
		panic("catalog dynamic-map projections differ from the reviewed plan contract")
	}
	if len(catalog.Types) != len(catalogSources) {
		panic("catalog type inventory differs from the reviewed closure")
	}
	for index, source := range catalogSources {
		want := astCatalogType(oldRef, source)
		if catalog.Types[index].ID != source.ID || !sameCatalogType(catalog.Types[index], want) {
			panic(fmt.Sprintf("catalog fields or types differ from baseline source: %s: catalog=%#v source=%#v", source.ID, catalog.Types[index], want))
		}
		target := astCatalogType(targetRef, source)
		if !sameCatalogType(want, target) {
			panic("catalog source type changed across old/target: " + source.ID)
		}
	}
	if len(catalog.Enums) != 1 || catalog.Enums[0].ID != "model.OperationStatus" || !sameStrings(catalog.Enums[0].Values, operationStatusValues(oldRef)) || !sameStrings(catalog.Enums[0].Values, operationStatusValues(targetRef)) {
		panic("catalog OperationStatus enum differs from Go source")
	}
	validateCatalogClosure(catalog)
	var decoded any
	if err := json.Unmarshal(declaration["schemaCatalog"], &decoded); err != nil {
		panic(err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(canonical)
	got := hex.EncodeToString(digest[:])
	for _, field := range []string{"schemaCatalogSHA256", "oldSchemaSHA256", "newSchemaSHA256"} {
		var value string
		if err := json.Unmarshal(declaration[field], &value); err != nil || value != got {
			panic("declaration catalog digest differs: " + field)
		}
	}
	return map[string]any{"baselineSourceSHA": oldSHA, "targetSourceSHA": strings.TrimSpace(string(git("rev-parse", "--verify", targetRef+"^{commit}"))), "catalogRevision": catalog.CatalogRevision, "catalogSHA256": got, "checkedStructs": len(catalog.Types), "checkedEnums": len(catalog.Enums), "scope": "exact JSON tags, Go field types, and OperationStatus values from both Git source revisions; no global schema epoch"}
}

func validateCatalogClosure(catalog schemaCatalog) {
	known := map[string]bool{"model.OperationStatus": true}
	for _, item := range catalog.Types {
		known[item.ID] = true
	}
	for _, root := range []string{"etcdstore.v3Record", "fleet.CapacityPlan", "fleet.PlanRequest", "model.Operation", "model.OperationReceipt"} {
		if !known[root] {
			panic("catalog omits a required plan-contract root: " + root)
		}
	}
	for _, item := range catalog.Types {
		for _, field := range item.Fields {
			for _, pkg := range []string{"etcdstore", "fleet", "model"} {
				marker := pkg + "."
				for offset := 0; ; {
					index := strings.Index(field.Type[offset:], marker)
					if index < 0 {
						break
					}
					start := offset + index
					end := start + len(marker)
					for end < len(field.Type) && (field.Type[end] == '_' || field.Type[end] >= 'A' && field.Type[end] <= 'Z' || field.Type[end] >= 'a' && field.Type[end] <= 'z' || field.Type[end] >= '0' && field.Type[end] <= '9') {
						end++
					}
					ref := field.Type[start:end]
					if !known[ref] {
						panic("catalog closure references an uncataloged Go type: " + ref)
					}
					offset = end
				}
			}
		}
	}
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameCatalogType(left, right catalogType) bool {
	if left.ID != right.ID || left.SchemaVersion != right.SchemaVersion || len(left.Fields) != len(right.Fields) {
		return false
	}
	for index := range left.Fields {
		if left.Fields[index] != right.Fields[index] {
			return false
		}
	}
	return true
}

func main() {
	if len(os.Args) != 3 && (len(os.Args) != 5 || os.Args[3] != "--catalog") {
		panic("usage: compatibility-source-audit OLD_SHA TARGET_SHA [--catalog FILE]")
	}
	old, new := strings.TrimSpace(string(git("rev-parse", "--verify", os.Args[1]+"^{commit}"))), strings.TrimSpace(string(git("rev-parse", "--verify", os.Args[2]+"^{commit}")))
	nf := map[string]bool{}
	for _, p := range files(new) {
		nf[p] = true
	}
	a, b := map[string]string{}, map[string]string{}
	changes := []string{}
	added := []string{}
	base := files(old)
	for _, p := range base {
		if !nf[p] {
			panic("deleted source: " + p)
		}
		delete(nf, p)
		x, y := declarations(old, p), declarations(new, p)
		for k, v := range x {
			a[k] = v
			w, ok := y[k]
			if !ok {
				panic("removed declaration: " + k)
			}
			b[k] = w
			if w != v {
				changes = append(changes, k)
			}
		}
		for k := range y {
			if _, ok := x[k]; !ok {
				added = append(added, k)
			}
		}
	}
	addedFiles := []string{}
	for p := range nf {
		addedFiles = append(addedFiles, p)
	}
	sort.Strings(changes)
	sort.Strings(added)
	sort.Strings(addedFiles)
	withoutA, withoutB := map[string]string{}, map[string]string{}
	for k, v := range a {
		if k != "v2/api/config/config.go:type:Config" {
			withoutA[k] = v
			withoutB[k] = b[k]
		}
	}
	result := map[string]any{"toolchain": runtime.Version(), "algorithm": "go-ast-type-and-constant-prefix/v1", "oldRef": old, "targetRef": new, "baselineFiles": len(base), "baselineDeclarations": len(a), "changedExistingDeclarations": changes, "addedDeclarationsInExistingFiles": added, "addedFiles": addedFiles, "allBaselineOldSHA256": hash(a), "allBaselineTargetSHA256": hash(b), "baselineExcludingRuntimeConfigOldSHA256": hash(withoutA), "baselineExcludingRuntimeConfigTargetSHA256": hash(withoutB), "scope": "Source review aid only, not schema compatibility proof. Baseline AST types and constant declaration prefixes in non-test API source; function behavior, key paths, additions, runtime encodings and reader/writer epochs require separate review"}
	if len(os.Args) == 5 {
		result["catalogAudit"] = verifyCatalog(old, new, os.Args[4])
	}
	raw, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		panic(e)
	}
	fmt.Println(string(raw))
}
