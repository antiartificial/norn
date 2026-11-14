# Online compatibility source review aid

From the repository root, compare two immutable source revisions:

```sh
GOTOOLCHAIN=go1.26.10 GO111MODULE=on GOWORK=off go run v2/scripts/compatibility-source-audit/main.go OLD_SHA TARGET_SHA
GOTOOLCHAIN=go1.26.10 GO111MODULE=on GOWORK=off go test v2/scripts/compatibility-source-audit/main.go v2/scripts/compatibility-source-audit/main_test.go
GOTOOLCHAIN=go1.26.10 GO111MODULE=on GOWORK=off go run v2/scripts/compatibility-source-audit/main.go OLD_SHA TARGET_SHA --catalog release/online-compatibility.json
```

This reads Git objects, not working-tree files. It inventories baseline non-test
API types and constants, reports additions, and rejects deleted files or
removed declarations. Constant footprints include the preceding declaration
specifications so inserted `iota` values remain visible. Comments are excluded;
field tags are retained. File paths and declaration names contribute to hashes.
The explicit runtime-config exclusion is a review view, not a schema definition.

The retained `docs/v3/evidence/online-source-audit-fa301fa5-ec01f28c.json`
compares published source `fa301fa5` with candidate source `ec01f28c`. Its only
changed baseline declaration is runtime `config.Config`; it adds a Fleet-create
scope and four production files. Matching baseline hashes after excluding
runtime configuration do not establish compatibility. Review added records,
function behavior, key paths, conditional writes, encodings, feature gates and
reader/writer epochs separately. A source-reviewed declaration may accompany an immutable candidate proposal.
The actual signed old/new pair must pass runtime qualification before controller
promotion. This report cannot authorize promotion or populate a schema
fingerprint by itself.

The optional `--catalog FILE` mode audits a source-owned online-compatibility
declaration. It compares every cataloged struct field's JSON name, Go type and
exact JSON tag, plus `model.OperationStatus`, against both Git source refs. It
also verifies the catalog SHA-256 fields. This is limited to the named plan
catalog and does not imply compatibility for other database or API schemas.

The source declaration describes only the named plan catalog and endpoints.
It does not qualify workload continuity, general database schemas or stateful
applications. Publication, runtime qualification and controller promotion are
separate steps.
