# AGENTS.md — website

The emberd documentation site. Next.js 16 (App Router) + Fumadocs + Tailwind v4,
managed with pnpm. The Go runtime lives at the repo root — see `../AGENTS.md`.

## Setup commands

```sh
pnpm install            # runs fumadocs-mdx postinstall
pnpm dev                # local dev server
pnpm build              # production build
pnpm start              # serve the production build
```

## Checks

```sh
pnpm types:check        # fumadocs-mdx && next typegen && tsc --noEmit
```

Run `pnpm types:check` before finishing a change — there is no separate test
suite, so the type check is the gate.

## Conventions

- Docs content is MDX under `content/`; routing is App Router under `app/`.
- TypeScript strict; styling via Tailwind v4 (`postcss.config.mjs`).
- Keep docs in sync with runtime behavior described in `../README.md`.
