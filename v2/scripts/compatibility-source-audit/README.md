# Online compatibility source review aid

From the repository root, compare two immutable source revisions:

```sh
GO111MODULE=off GOWORK=off go run v2/scripts/compatibility-source-audit/main.go OLD_SHA TARGET_SHA
GO111MODULE=off GOWORK=off go test ./v2/scripts/compatibility-source-audit
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
reader/writer epochs separately. Run the signed old/new runtime qualification
before issuing a compatibility declaration. This report cannot authorize a
release or populate a schema fingerprint by itself.
