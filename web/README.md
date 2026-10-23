# wi web client

The browser client served by `wi web`: React 19, TypeScript, Vite, Tailwind
CSS v4, shadcn/ui (Radix) and AI Elements, with TanStack Router and TanStack
Query. `npm run build` writes `../internal/web/dist/app`, which the `wi`
binary embeds; `scripts/install-local` and `scripts/release-local` run it via
`scripts/build-web`. Without a build, `wi web` serves a placeholder page.

The Go server (`internal/web`) owns all policy and exposes a JSON API plus one
server-sent event stream per tab (`/api/events`), which pushes full snapshots
of the work list, creation receipts, the open conversation and its live
output. The client writes those frames into the TanStack Query cache.

```sh
npm ci
npm test          # Vitest unit tests
npm run build     # type-check and build into internal/web/dist/app
WI_WEB_URL=http://127.0.0.1:8787 npm run dev   # proxy /api to a running wi web
```

With `npm run dev`, open the dev server with the `#token=...` from `wi web`'s
launch URL, or run `wi web --access-url` with `--no-token` behind a private
proxy. Components in `src/components/ui` come from the shadcn registry
(`npx shadcn@latest add NAME`) and are owned here.
