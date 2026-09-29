# M2 Nomad component environment rehearsal — 2026-09-24

This local rehearsal checked Nomad's actual `nomadVar` environment-template
parser with a synthetic WordPress-style database password. It did not use a
live app, production credential, or Mini/Fleet Nomad server.

The initial Norn template piped a `nomadVar` item directly to `toJSON`.
[Nomad's template contract](https://developer.hashicorp.com/nomad/docs/job-specification/template)
defines those items as objects and requires `.Value` when piping the stored
string into a template function. The Norn template now renders both database
URLs and component values as `{{ .<revision-item>.Value | toJSON }}` for
environment delivery. File delivery continues to render the item as text.

A disposable local Nomad 2.0.7 development agent used the `raw_exec` driver
and a batch allocation. Its private `nomad/jobs/<job-id>` variable held a
synthetic value containing a space, double quote, single quote, backslash,
and `#`. The allocation rendered the same `.Value | toJSON` expression as
Norn's component template, then printed only the SHA-256 of the resulting
environment bytes. Expected and allocation-observed SHA-256 were both
`bacf20e4cae939eaa20476b8716db1e1f577909d05830d6bcd6856a0aaacddaa`.
The batch allocation completed. The dev agent was stopped and its probe
configuration removed. Focused `go test ./nomad ./pipeline -count=1` passed.

This proves the item accessor and quoting for one actual Nomad allocation.
It does **not** yet qualify Norn's full translated service, periodic, or
function jobs; workload-identity permissions; staged revision promotion;
managed MySQL TLS; or database backup/restore. Those remain M2 release gates.
