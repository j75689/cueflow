# CueFlow Dashboard

Next.js (App Router) + Tailwind CSS, built as a static export and embedded into the Go binary.
The API contract lives in [`../docs/api.md`](../docs/api.md).

```bash
npm install
npm run dev        # http://localhost:3001, proxies /api/* to $CUEFLOW_API (default http://127.0.0.1:3000)
npm run typecheck  # tsc --noEmit
npm run build      # static export -> web/out
```

Routes use query parameters instead of dynamic segments (static export):
`/deliverables/view/?id=`, `/tasks/view/?id=`, `/connectors/edit/?id=`.
