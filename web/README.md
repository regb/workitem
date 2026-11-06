# wi web client

The browser client served by `wi web`: React 19, TypeScript, Vite, Tailwind
CSS v4, shadcn/ui (Radix) and AI Elements, with TanStack Router and TanStack
Query. `npm run build` writes `../internal/web/dist/app`, which the `wi`
binary embeds; `scripts/install-local` and `scripts/release-local` run it via
`scripts/build-web`. Without a build, `wi web` serves a placeholder page.

The page calls the daemon's API, `wi.core.v1`, with the clients generated from
`proto/` into `src/gen` (`scripts/generate-api`). `wi web` (`internal/web`)
forwards those calls to the daemon under the tab's identity and serves the
page, attachments and checkout documents; it adds no policy of its own. One
`EventService.Watch` stream per visible tab says when to read the dashboard
and the open conversation again, and carries live output and jobs.

```sh
npm ci
npm test          # Vitest unit tests
npm run build     # type-check and build into internal/web/dist/app
WI_WEB_URL=http://127.0.0.1:8787 npm run dev   # proxy API calls to a running wi web
```

With `npm run dev`, run `wi web --port 8787` and open the dev server; it
forwards the API as `wi web`'s own origin. Components in `src/components/ui`
come from the shadcn registry (`npx shadcn@latest add NAME`) and are owned
here.
