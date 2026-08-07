# Norn v2 Web UI — Design Specification

Audience: developers and ops engineers running apps on Norn (a self-hosted app platform:
Nomad/Consul orchestration, deploy pipeline with sagas/durable operations, snapshots,
cron, functions, beacon incidents, wake gateway).

This document is the contract for the UI overhaul. It is grounded in the current code
(`v2/ui`) and the live API surface (`v2/api`). Implementers: follow it exactly; where it
is silent, match the existing codebase's conventions.

## 1. Product principles

1. **Ops console, not marketing site.** Dense, legible, monospace-forward for data.
   Every screen answers "is it healthy, what changed, what do I do next".
2. **Live-first.** The versioned websocket hub (`/api/v1/events`) drives the UI. State changes (deploys,
   incidents, restarts, snapshots) appear without refresh, announced via toasts and
   an activity feed — never via silent mutation alone.
3. **Keyboard-first.** Command palette (⌘K), Escape closes any layer, full tab
   traversal, visible focus. A developer should operate the whole console without a mouse.
4. **Honest states.** Every data region has explicit loading (skeleton), empty
   (guidance), and error (message + retry) states. No `.catch(() => {})`.
5. **Consistent surfaces.** One modal system, one drawer system, one toast system.
   The same gesture always produces the same interaction model.
6. **Deep-linkable.** Every view, app, and detail tab has a URL.

## 2. Stack decisions

- Keep: React 19, TypeScript (strict), Vite 7, pnpm, vitest + testing-library,
  hand-written CSS with design tokens (NO Tailwind, NO component kit),
  vendored FontAwesome (`fawsb` classes), `@xyflow/react`, `@xterm/xterm`.
- Add (only these):
  - `react-router-dom` (^7) — routing/deep links.
  - `@tanstack/react-query` (^5) — server state: caching, polling, invalidation-on-ws-event.
    Kills the N+1 canary fetches and ad-hoc polling.
  - `@fontsource-variable/inter`, `@fontsource/jetbrains-mono` — actually load the brand fonts.
- Do NOT add: Tailwind, styled-components, cmdk, radix, framer-motion, zustand, axios.
  Build primitives by hand in `src/components/ui/`.

## 3. Design tokens (`src/styles/tokens.css`)

Replace the ad-hoc `:root` block. Dark is default; light theme via
`[data-theme="light"]`; initial theme = localStorage override, else
`prefers-color-scheme`. Theme toggle in the sidebar footer.

```css
:root {
  /* surfaces (dark) */
  --bg: #0b0d12;            /* page */
  --surface-1: #12151d;     /* cards, sidebar */
  --surface-2: #1a1e29;     /* nested surfaces, hover */
  --surface-3: #232838;     /* active, inputs */
  --border: #262b3a;
  --border-strong: #38405a;
  /* text */
  --text-1: #e8eaf2;        /* primary */
  --text-2: #9aa1b5;        /* secondary */
  --text-3: #626a80;        /* faint/meta */
  /* brand + semantics (shared across themes, adjust for contrast in light) */
  --accent: #8b7cf6;        /* purple — primary actions, active nav */
  --accent-soft: rgba(139, 124, 246, 0.14);
  --ok: #34c98e;  --ok-soft: rgba(52,201,142,.12);
  --warn: #e5a54b; --warn-soft: rgba(229,165,75,.12);
  --danger: #e5636c; --danger-soft: rgba(229,99,108,.12);
  --info: #5aa7e8; --info-soft: rgba(90,167,232,.12);
  /* shape + type */
  --radius-sm: 6px; --radius: 8px; --radius-lg: 12px;
  --font: 'Inter Variable', system-ui, sans-serif;
  --mono: 'JetBrains Mono', ui-monospace, monospace;
  /* type scale (px): 11 meta / 12 dense-data / 13 body / 14 emphasized / 16 section / 20 page title */
  /* spacing: 4-based scale — 4, 8, 12, 16, 24, 32 */
}
```

Also define compat aliases used by existing code until migrated: `--color-warn: var(--warn)`,
`--color-muted: var(--text-3)`, `--muted: var(--text-3)`, `--bg/--surface/--border/--text/--text-dim`
mapped onto the new tokens, plus `--green/--red/--blue/--amber/--purple` → semantic tokens.

Focus: global `:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }`.
Status is never color-only: every status dot pairs with a text label or accessible name.

## 4. Information architecture & routes

Left **sidebar** (collapsible to icons, 220px ↔ 56px, persisted) replaces header-button nav.

```
/                → redirect to /overview
/overview        Overview: platform at a glance
/apps            Apps grid (cards) + filter
/apps/:id        App detail page with tabs:
/apps/:id/(overview|logs|deploys|snapshots|cron|functions|shell)
/deploys         Deploy history + live deploy tracking
/incidents       Beacon events: ack/snooze/open, severity, correlation
/operations      Durable operations queue + saga inspector (/operations/:sagaId)
/topology        React Flow graph (rethemed dark)
/platform        Platform hub with sub-tabs:
/platform/(releases|network|access|notifications|observability|contextdb)
```

Sidebar sections: **Operate** (Overview, Apps, Deploys, Incidents, Operations),
**Understand** (Topology), **Configure** (Platform). Sidebar footer: theme toggle,
version, WS connection indicator (green dot "Live" / amber "Reconnecting…").

Header (slim, per-page): page title, contextual actions, global search button (⌘K),
`StatusBar` health pills (PG/Nomad/Consul).

**Command palette (⌘K / Ctrl+K)**: custom component. Fuzzy-matches apps (jump to
`/apps/:id`), views, and actions ("Deploy <app>", "Restart <app>", "Ack all incidents").
Arrow keys + Enter, Escape closes. Listed actions execute the same mutations as buttons.

## 5. Views

### Overview (new — the landing page)
Grid of compact panels, each linking to its full view:
- **Fleet health**: N apps healthy / M unhealthy / K idle; unhealthy apps listed by name.
- **Active incidents**: open beacon events by severity (critical first), ack/snooze inline.
- **Running operations**: active durable ops with kind, app, attempt count, live status.
- **Recent deploys**: last 5 with status chips, relative time, commit sha (mono).
- **Platform**: version, release channel, PG/Nomad/Consul pills, snapshot readiness.
An **activity ticker** (aria-live polite) shows the last ~20 websocket events with
relative timestamps.

### Apps
Keep the (good) existing card grid and empty state, but:
- Cards navigate to `/apps/:id`; quick actions remain (Deploy, Restart, Logs shortcut).
- Canary status comes from a single React Query batched per-app query, not N+1 on mount —
  fetch on card visibility or via one shared query keyed per app with staleTime.
- Filter bar: all/healthy/unhealthy/idle + text filter, reflected in `?filter=` URL param.

### App detail `/apps/:id` (new — replaces the modal pile)
Header: name, health, nomad status, endpoints (copyable), repo link, primary actions
(Preflight, Deploy, Restart, Scale). Tabs:
- **Overview**: processes, allocations (live/retained), infra badges, resources, canary
  state + promote, endpoints toggle, secrets status (names + set/unset only).
- **Logs**: existing streaming LogViewer, full-height.
- **Deploys**: per-app deployment history; expanding a row shows the step timeline from
  `/api/deployments/{id}/steps` (step, kind, attempt, duration, message).
- **Snapshots / Cron / Functions**: existing panel features, rendered as tab content
  (not overlays). Destructive actions (restore, import) use the shared ConfirmDialog.
- **Shell**: xterm exec terminal as a tab.
Scale stays a Modal. A live deploy shows the DeployPanel step tracker pinned at the top
of whichever tab is open, plus a global toast on start/success/failure.

### Deploys
Existing DeployHistory table + filters, with URL-synced filters, expandable step
timelines, and a "live" row pinned on top while a deploy runs anywhere in the fleet.

### Incidents (new)
Beacon events from `/api/events` (+ `/api/events/active`, `/correlated`):
severity-grouped list (critical/warning/info), state chips, ack / snooze / re-open
actions (`POST /api/events/{id}/ack|snooze|open`), filter by app and state,
relative + absolute timestamps. Live insert via `beacon.event` ws type with toast for
critical severity. Include notification sinks status link to /platform/notifications.

### Operations (new)
Durable operations queue from `/api/operations` (+ `/active`): table of kind, app,
status chip (queued/running/succeeded/failed/canceled), risk, attempts (n/max),
next-attempt countdown for retrying ops, lastError expandable. Row click → saga
inspector `/operations/:sagaId`: ordered saga event log from `/api/saga/{sagaId}`
rendered as a timeline (mono, timestamped, payload collapsible).

### Topology
Keep the React Flow structure; retheme to design tokens (dark + light via CSS vars),
remove the hardcoded `'ft-trove'` preferred app (default = first app or none).

### Platform
Split the current 571-line PlatformPanel firehose into sub-tab routes:
- **Releases**: platform releases + rollback (ConfirmDialog), deploy groups.
- **Network**: service manifest table, cloudflared ingress, wake-gateway targets.
- **Access**: grants, token creation, access events/patterns.
- **Notifications**: channels CRUD + test.
- **Observability**: metrics summary, prometheus/alerts config downloads, install action.
- **ContextDB**: existing OpsPanel content, labeled as ContextDB here (and only here).
All inline `style={{}}` replaced by stylesheet classes on tokens.

## 6. Shared primitives (`src/components/ui/`)

Build once, use everywhere. All keyboard/a11y complete:
- `Button` (variant: primary/secondary/ghost/danger; size sm/md; loading state).
- `Modal` — focus trap, Escape, focus return, `aria-modal`, backdrop click, sizes.
- `Drawer` (right-side, for inspectors) — same a11y contract as Modal.
- `ConfirmDialog` — replaces every native `confirm()`; danger variant states consequence.
- `Toast` system — `useToast()`; success/error/info; aria-live polite; auto-dismiss with
  hover-pause; top-right stack.
- `Tabs` — roving tabindex, arrow keys, `aria-selected`, URL-synced where routed.
- `StatusChip` / `StatusDot` — status + label, semantic colors, never color-only.
- `Skeleton`, `EmptyState` (icon + title + hint + optional action), `ErrorState`
  (message + Retry button).
- `DataTable` — header, monospace numeric cells, row hover, empty/error/loading built in,
  horizontal scroll containment on narrow screens.
- `CopyButton` — copy-to-clipboard with tooltip feedback, real `<button>`.
- Keep `Tooltip` (make it focus-triggerable too).

## 7. Data layer

- `src/lib/api.ts`: typed `apiFetch<T>(path, init?)` — joins base URL, sets credentials,
  normalizes errors to `ApiError { status, message, detail }`, parses JSON. All
  components use it; zero raw `fetch()` outside this file and the streaming/ws code.
- React Query provider at root. Query keys: `['apps']`, `['app', id]`, `['deployments', filters]`,
  `['events', filters]`, `['operations']`, `['saga', id]`, etc. Sensible staleTimes;
  polling only where ws doesn't cover (stats 30s, health 30s).
- `useHubEvents()`: single `/api/v1/events` subscription with last-event cursor replay that (a) exposes
  typed events to subscribers, (b) invalidates matching query keys per event type
  (`deploy.* → ['deployments'], ['app', appId]`; `beacon.event → ['events']`; etc.),
  (c) feeds the toast system and activity ticker. Discriminated-union type
  `HubEvent` in `src/types/ws.ts` for all known event types (deploy.*, preflight.*,
  snapshot.*, function.completed, beacon.event, rollback.*, canary.promoted).
- Keep deploy step-tracking reducer logic, extracted from App.tsx into
  `src/hooks/useDeployProgress.ts`.

## 8. Accessibility & keyboard (acceptance checklist)

- Every interactive element is a real `<button>`/`<a>`/input (no clickable spans/icons/h3).
- Modals/drawers/palette: focus trap, Escape to close, focus returns to the invoker.
- `aria-live="polite"` on toasts, activity ticker, and deploy step tracker.
- All icons are `aria-hidden`; buttons carry `aria-label` when icon-only.
- `:focus-visible` ring everywhere; logical tab order; skip-to-content link.
- Contrast ≥ 4.5:1 for text in both themes (verify token pairs).
- `prefers-reduced-motion`: disable non-essential animation.

## 9. Responsiveness

Breakpoints: 1280 (sidebar auto-collapses to icons), 920 (two-column grids stack),
680 (single column; tables scroll horizontally inside their container; header actions
collapse into an overflow menu). No page-level horizontal scroll ever.

## 10. Quality bar & testing

- `pnpm build` (tsc + vite) clean; no TypeScript errors; no unused exports.
- Vitest coverage for: every ui/ primitive (behavioral: focus trap, Escape, toast
  lifecycle, tabs keyboard nav), `apiFetch` error normalization, `useHubEvents`
  invalidation mapping, deploy progress reducer. Target ≥ 60 meaningful tests.
- No inline `style={{}}` except truly dynamic values (e.g. computed widths).
- All shared domain types in `src/types/` — delete the duplicated inline interfaces
  in PlatformPanel/OpsPanel.
- Every list/table renders skeleton on load, EmptyState when empty, ErrorState on failure.

## 11. Constraints

- Touch ONLY `v2/ui/**` (plus this file). Never modify `v2/api/**` or repo-root files.
  There are unrelated uncommitted changes in `v2/api/pipeline/` — leave them alone.
- Keep pnpm; commit `pnpm-lock.yaml` changes.
- Keep the vendored FontAwesome icon usage (`fawsb` classes) — do not swap icon systems.
- Preserve all existing functionality: nothing that works today may be lost.
- Vite dev proxy (`/api` → 127.0.0.1:8800) carries the versioned WebSocket endpoint.

## 12. Milestones

**M1 — Foundation**: deps (router, react-query, fontsource); tokens.css + theme
switching + font loading; `src/components/ui/` primitives with tests; apiFetch;
useHubEvents + HubEvent types; toast system. App still renders as before (old shell may
temporarily wrap new providers).

**M2 — Shell & IA**: sidebar layout, routes, command palette, Overview page, Apps grid
migration, App detail page with tabs (logs/deploys/snapshots/cron/functions/shell as
tabs; modals → ConfirmDialog), Deploys view. App.tsx shrinks to providers + routes.

**M3 — Depth & polish**: Incidents view, Operations + saga inspector, Platform sub-tabs
(inline styles eliminated), topology retheme + light theme, responsive pass, a11y pass,
remaining tests, dead code removal (old panels/CSS).

Each milestone must end with: `pnpm build` green, `pnpm test` green, and a short
CHANGES summary printed.
