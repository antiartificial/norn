# DO staging topology source audit — 2026-11-08

This audit reconciles the checked-in staging contract with the V3 release
models. It is source evidence only; it does not prove current provider state
or actual live membership.

## Source evidence

The current local `norn-fleet` checkout is at `cc2be083a00d6afd3964655519dc2abb580150b6`.
The audited files are clean relative to that checkout:

- `environments/staging/nyc3/cluster.yaml` declares two node pools: control
  (`min=desired=max=3`) and ingress (`min=desired=2`, `max=3`). That is five
  base compute nodes, with a separately approved one-node ingress scale-up
  available for the 2→3→2 rehearsal.
- `environments/staging/nyc3/main.tf` passes that `nodePools` map into the
  DigitalOcean module. The module creates one Droplet per desired pool node
  and exposes those Droplets through its sensitive `nodes` output.
- The same root declares managed PostgreSQL and MySQL clusters separately.
  Those databases are not application node pools and do not increase the
  Droplet count.
- The staging cluster document declares no application node pools.

## Reconciliation

Norn V3 documents and `FreshStagingRecovery.tla` currently describe a
3-control + 2-ingress + 4-app (nine-node) target. The checked-in staging source
does not implement that topology. Treat the nine-node chart as a proposed
future Fleet scenario, not as the current DO staging contract or evidence of a
deployed nine-node fleet. Prior notes describing a joined nine-node protected
recovery test were not reproducible in the current Norn and `norn-fleet`
checkouts during this audit; do not use that claim as a gate result without a
test file and run record.

The user separately specified that a new Fleet should default to two
application node pools. That requirement is not represented in the current
staging cluster document. Pool names, sizing, replica bounds, workload
placement, and the relationship to control/ingress pools still need an
explicit input contract before a new provider plan can be considered ready.

## Live-state limit

The local `doctl` context does not match the pinned work-staging account. No
provider inventory or health query was made. These source files establish the
declared configuration only; current DO resource count, identities, and health
remain unverified until the protected work-account read-only route is restored.

## Required follow-up

1. Keep the current DO staging lane at its five-node source contract for
   planning. Use the protected, fresh state/provider readback before making a
   live claim.
2. Define the future two-app-pool Fleet contract separately. Update its
   validators, plan policy, bootstrap inputs, workflow, receipt, and recovery
   model together before producing a reviewed plan.
3. Reclassify the nine-node statechart as a proposed topology, or replace it
   with a model derived from the adopted fresh-Fleet input contract. Do not
   use it to claim coverage of the current staging workflow.
