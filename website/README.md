# emberd website

The documentation and landing site for **emberd**, the Firecracker microVM
sandboxing runtime. Built with [Next.js](https://nextjs.org) (App Router) and
[Fumadocs](https://fumadocs.dev); content is MDX.

## Stack

- Next.js 16 + React 19
- Fumadocs (`fumadocs-ui`, `fumadocs-core`, `fumadocs-mdx`)
- Tailwind CSS v4
- Fonts: Schibsted Grotesk + JetBrains Mono on the landing; Inter for docs body
- Dark theme by default (light/system still available in the toggle)

## Develop

```bash
pnpm install
pnpm dev          # http://localhost:3000
```

```bash
pnpm build        # production build
pnpm start        # serve the build
pnpm types:check  # fumadocs-mdx + next typegen + tsc --noEmit
```

## Structure

| Path | What it is |
| --- | --- |
| `app/(home)/page.tsx` | Custom landing page (hero, terminal, stats, features). Not the Fumadocs `HomeLayout` — its own dark shell. |
| `app/(home)/layout.tsx` | Landing shell: loads the display/mono fonts, renders the grid + glow + grain layers. |
| `app/docs/layout.tsx` | Fumadocs `DocsLayout` plus the pre-alpha `Banner`. |
| `app/layout.tsx` | Root layout. `RootProvider` sets the default theme to dark. |
| `app/global.css` | Tailwind + Fumadocs preset, and all landing styles scoped under `.ember-landing`. |
| `content/docs/*.mdx` | The documentation pages. |
| `content/docs/meta.json` | Sidebar order and grouping. |
| `lib/source.ts` | Fumadocs content source adapter. |
| `lib/layout.shared.tsx` | Shared layout options (nav title/logo, GitHub URL). |
| `lib/shared.ts` | App name, routes, git config constants. |
| `app/api/search/route.ts` | Search route handler. |
| `public/`, `app/icon.svg` | Static assets, logo, and favicon. |

## Editing the docs

Add or edit `.mdx` files under `content/docs/`. Frontmatter (`title`,
`description`) drives the page head; `meta.json` controls sidebar order. Fumadocs
components like `<Callout>`, `<Cards>`, and `<Banner>` are available in MDX. The
MDX/frontmatter schema lives in `source.config.ts`.

## Landing page

The landing is a self-contained dark design — engineered-minimal with a single
warm ember accent. Everything visual is scoped under the `.ember-landing` class
in `app/global.css` (palette variables, the fixed grid/glow/grain layers,
staggered load animation, focus rings), so it never leaks into the Fumadocs docs
theme. Fonts are loaded per-route in `app/(home)/layout.tsx` via `next/font`.

## Logo & favicon

The brand marks live in the repo-root `assets/` directory (source of truth). The
site uses copies:

- `public/logo.svg` — nav/header lockup (currently the split treatment)
- `app/icon.svg` — browser-tab favicon (the ember tile)

To swap the mark, copy a different file from `assets/` over those two.

## Pre-alpha banner

`app/docs/layout.tsx` renders a dismissible Fumadocs `Banner` warning that the
project is pre-alpha and may have breaking changes. Bump the banner's `id` when
you reword it so previously-dismissed visitors see the new message.
