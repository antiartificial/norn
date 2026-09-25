# Mini ingress destination join — 2026-09-25

This read-only follow-up narrows the four hostname identities that had no
declared app endpoint match in the [workload join](m0-mini-workload-join-2026-09-25.md).
It inspected Mini's owner-local cloudflared configuration, the authenticated
loopback Norn API, live listener metadata, and the current app allocation
summary. It did not copy hostnames, destination URLs, credentials, or job
environment values into this repository. No route or service was changed.

The inspected configuration was
`/Users/0xadb/.cloudflared/config.yml`, SHA-256
`da95d8bc386cefbfa19ff194cb8e82d1049ade912c1c5b12e97f84c944210d22`.
Its 16 named ingress entries matched the API's `/api/cloudflared/ingress`
hostname entries in order. The `com.norn.cloudflared` LaunchAgent reported
`running`. This binds the API's inventory to the inspected file at the
observation time; it does not prove every public DNS or proxy request reaches
its destination.

| Unmatched identity | Destination evidence | Disposition |
| --- | --- | --- |
| First and second | Both point to Mini `localhost:8080`. A later listener check found an IPv4 Python `open-webui` process bound to `*:8080`; the separate Docker backend listener was bound to Mini's LAN address. A loopback HEAD request returned 200 from Uvicorn. | Technical destination is the host Open WebUI process, outside Norn's declared app endpoints. Its accountable owner, supervision, and intended external exposure still need review. |
| Third | Points to Mini loopback port 8800. `norn-api` owns the listener, and TCP accepted a connection. | Identified as a Norn control API route, outside app endpoint ownership. Public reachability and intended exposure still need review. |
| Fourth | Points to the current `vigil-gateway` allocation's node address and its declared process port 8144. TCP accepted a connection. | Strong current runtime match to Vigil, although no declared app endpoint names this hostname; owner should record the exception and verify the full route. |

The file also contains a second entry for the third hostname with an
`http_status` destination after its API route. This is a duplicate rule in
the ordered configuration and requires an owner review of intended matching
and fallback behavior. A final unnamed fallback rule was outside the named
hostname count.

These observations identify a current technical destination for all four
unmatched identities: Open WebUI (two), Norn API (one), and Vigil (one).
They do not establish accountable ownership or intended external exposure
for the host Open WebUI and Norn API routes. The route gate still needs
cloudflared-to-service request proof, Consul or Traefik ownership where
applicable, and owner decisions before the Mini upgrade fixture can claim
representative traffic coverage.
