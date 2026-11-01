# Staging release intent

This source revision is the deliberate first release trigger for the protected
Fleet canary lane. The release remains gated by
`NORN_FLEET_CANARY_STAGING_RELEASE_ENABLED`; merging this note while that
variable is `false` has no runtime effect.

Before enabling and merging, operators must verify all of the following:

- the protected app runtime advertises Fleet GitHub OIDC exchange, staging
  release admission, deployment, and operation read capabilities through the
  runner-local TLS route;
- the reviewed one-use runner gate admits only the exact protected `master`
  run and `staging-runner-private` effect job;
- the hosted `Fleet canary` check succeeds for this exact source revision; and
- the runner-local API CA path and SHA-256 match the reviewed staging
  environment variables.

A successful release operation proves durable canary admission and deployment;
this Fleet staging lane does not issue a production promotion qualification.
Public route health, two distinct healthy replicas, and later 2-to-3-to-2 scale
fitness remain separate live evidence.
