# M2 local MinIO provider qualification — 2026-09-25

The opt-in real-provider test ran against MinIO
`RELEASE.2025-10-15T17-29-55Z` on macOS arm64, binary SHA-256
`b107901fd1afe7b36165c6aa66bb027f96e2ec2b690eebe8c9376746d9a9b0df`.
The server bound only to loopback and used a disposable 2 GiB sparse APFS
volume because the Mac's main volume was below MinIO's free-space threshold.
The bucket was created with versioning and Object Lock. A fresh UUID prefix
was used under `norn-v3-disposable/local-minio/`.

The first run found a real adapter defect: multipart completion sent the
retention and metadata headers from initiation again. MinIO rejected that
completion with `Invalid Request`; no retained artifact was visible. The
adapter now sends the conditional `If-None-Match: *` header alone on
completion, while initiation carries retention, metadata, and content type.

The corrected run passed a >8 MiB multipart publication, compliance-retention
verification, a repeated publication that retained the same provider version
ID, and exact byte recovery through a second S3 client with a separate private
spool. The retained version ID was
`e0c352fc-221f-40ba-ba8d-85a262c4ba82`. The full artifactstore suite and
race suite passed afterward.

This is stronger than the in-process emulator because it exercises an
independent S3 server implementation. It remains a single-Mac, loopback,
root-credential test. It does not prove a remote provider's policy, restricted
credentials, off-host durability, cross-node restore, or interrupted network
behavior. M2 is still open.
