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
	"sort"
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
func main() {
	if len(os.Args) != 3 {
		panic("old and target refs required")
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
	result := map[string]any{"oldRef": old, "targetRef": new, "baselineFiles": len(base), "baselineDeclarations": len(a), "changedExistingDeclarations": changes, "addedDeclarationsInExistingFiles": added, "addedFiles": addedFiles, "allBaselineOldSHA256": hash(a), "allBaselineTargetSHA256": hash(b), "baselineExcludingRuntimeConfigOldSHA256": hash(withoutA), "baselineExcludingRuntimeConfigTargetSHA256": hash(withoutB), "scope": "Source review aid only, not schema compatibility proof. Baseline AST types and constant declaration prefixes in non-test API source; function behavior, key paths, additions, runtime encodings and reader/writer epochs require separate review"}
	raw, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		panic(e)
	}
	fmt.Println(string(raw))
}
