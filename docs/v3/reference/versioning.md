# Version and release identity

The V3 documentation uses **Norn 3.0.0** for the first tested Fleet milestone,
completed on 2026-11-16. This documentation update does not publish a final
version tag, rebuild a bundle, or change the bytes of a signed release.

| Identity | Current value or meaning |
| --- | --- |
| Product milestone | Norn 3.0.0 |
| Qualified target source | `cdf2081e10ed117563aeb3a35d19c0baede1f42d` |
| Published immutable release | `platform-cdf2081e10ed117563aeb3a35d19c0baede1f42d`, currently a GitHub prerelease |
| Existing V3 version tag | `v3.0.0-rc.1-control`, on an older source commit |
| Final 3.0.0 tag | Not published by this documentation change |
| Active source layout | `v2/api`, `v2/cli`, `v2/ui`, and `v2/scripts` |
| Current versioned control API | `/api/v1`, alongside still-used compatibility routes |

## Number future changes

Use `3.0.x` for compatible fixes to the 3.0 behavior. Use `3.x.0` for compatible
capabilities. A breaking public contract needs an explicit migration policy
and a major-version decision. Protocol/schema versions evolve independently
when their own contracts require it.

Retain the full source SHA, manifest digest, signature identity, and executor
source for every deployment. A human-readable version helps operators choose
a release; immutable signed identities determine what the controller executes.
Do not move an existing tag or replace a signed artifact to change its label.

[Release milestones](../release-program-milestones.md) distinguish publication,
qualification, deployment, observation, finalization, and retirement.
