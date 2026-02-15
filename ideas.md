# Norn Ideas

## IP Access Monitoring Dashboard

See who/what is hitting Norn-managed services. Three tiers:

### Tier 1: Norn API request logging (light)
- Add chi middleware that logs `Cf-Connecting-IP` (real client IP from Cloudflare) to a `request_log` table
- Fields: ip, path, method, status, user_agent, timestamp, app (norn itself)
- Norn dashboard page: table of IPs, hit counts, last seen, paths, user agents
- Covers Norn control plane only

### Tier 2: Cloudflare Analytics (zero code)
- Already available at dash.cloudflare.com — visitor IPs, geo, paths, bot scores
- Free tier. Limitation: not inside Norn UI, retention not controlled

### Tier 3: Shared gateway for all apps (medium)
- Lightweight reverse proxy sidecar or shared gateway service between Cloudflared and app pods
- Logs every request to shared Postgres table or sends events to Norn WebSocket hub
- Norn dashboard "Traffic" page showing all apps' access patterns unified
- `Cf-Connecting-IP` header carries real client IP through Cloudflare tunnel

### Temporary Access / Allowlisting
- **JWT with TTL**: Norn issues short-lived tokens, share URL with `?token=...`, auto-expires
- **IP allowlist with expiry**: Store `{ip, expires_at}` in DB, middleware checks. CLI: `norn access grant --ip 1.2.3.4 --ttl 24h`
- **Tailscale sharing**: Network-level, no code changes, temporary node sharing
- **Cloudflare Access**: Gate hostnames/paths with email OTP or OAuth, no Norn code needed
