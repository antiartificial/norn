# M2 retained artifact provider qualification

The opt-in `TestS3RealProviderQualification` exercises the actual S3-compatible
provider instead of the in-process emulator. It requires an existing dedicated
bucket with versioning and Object Lock enabled, a credential scoped to that
bucket, and permission to publish retained test objects. The test refuses a
provider that cannot prove conditional creation and compliance retention. Its
objects remain locked for at least 24 hours and incur provider storage charges.

Put the following JSON in an owner-owned mode-`0600` file. The prefix must
start with `norn-v3-disposable/`; the test adds a fresh UUID below it. Do not
put credentials in a command argument, repository file, or test output.

```json
{
  "endpoint": "s3.example.invalid",
  "bucket": "dedicated-artifact-qualification-bucket",
  "region": "provider-region",
  "accessKey": "provider-access-key",
  "secretKey": "provider-secret-key",
  "prefix": "norn-v3-disposable/qualification"
}
```

Run from `v2/api` with
`NORN_TEST_S3_CONFIG_FILE=/absolute/private/config.json go test ./artifactstore -run '^TestS3RealProviderQualification$' -v -count=1`.
The test publishes a multipart object, retries the same content key, requires
the same provider version ID after the retry, and verifies the exact bytes
through a second client with a separate private spool. It prints only the
bucket, generated prefix, and retained version ID. The configured credential
must be unable to delete or bypass retention by policy; the test does not
prove that policy by itself.

Passing this test qualifies the adapter's provider operations for one chosen
bucket. M2 still needs a separate-node restore of a signed MySQL artifact and
recovery under an interrupted publish/materialization before release.
