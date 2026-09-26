# Ku Marketing Site (WEB)

Static marketing/landing site for [Ku](https://kuayle.com), built with SvelteKit and the static adapter.

## Canonical origin

`https://kuayle.com` — configured centrally in `src/lib/config/site.ts`.

## Stack

- **SvelteKit** (prerendered static, `@sveltejs/adapter-static`)
- **Svelte 5** (runes mode)
- **Tailwind CSS v4**
- **shadcn-svelte** primitives (button, badge)
- **Lucide** icons

## Scripts

| Command              | What it does                                           |
| -------------------- | ------------------------------------------------------ |
| `npm run dev`        | Start dev server                                       |
| `npm run build`      | Prerender static site to `build/`                      |
| `npm run preview`    | Preview the production build                           |
| `npm run check`      | Run `svelte-check` for type errors                     |
| `npm run validate-seo` | Run SEO validation on `build/` output               |
| `npm run validate`   | Build + validate (CI-ready)                            |
| `npm run test:responsive` | Run Playwright responsive layout tests            |

## SEO conventions

- **Centralized config**: `src/lib/config/site.ts` — canonical origin, defaults, `url()` helper.
- **Reusable SEO component**: `src/lib/components/Seo.svelte` — title, description, canonical, robots, full OG/Twitter metadata, JSON-LD.
- **JSON-LD**: Organization, WebSite, SoftwareApplication on homepage; BreadcrumbList + WebPage on all child pages.
- **Sitemap**: Generated at build time via `src/routes/sitemap.xml/+server.ts` using the route registry.
- **robots.txt**: Declares the sitemap location.
- **Image optimization**: All `<img>` tags carry intrinsic `width`/`height` and descriptive `alt`.

## Content conventions

- **Route registry**: `src/lib/data/routes.ts` — source of truth for sitemap entries, hubs, breadcrumbs, and shared metadata.
- **Content registries**: `src/lib/data/features.ts`, `self-hosting.ts`, `compare.ts`, `alternatives.ts` — data-driven content for dynamic routes.
- **Release version**: `src/lib/release.svelte.ts` — `useLatestRelease()` fetches the `UI/static/releases.json` manifest from the repository's main branch at runtime (same mechanism as the app). Static HTML renders `FALLBACK_VERSION`; bump it when the fallback becomes stale.
- **Silo architecture**: Each hub (Features, Self-Hosting, Compare, Alternatives) has a hub page + child detail pages using shared `PageLayout.svelte`.
- **Standalone pages**: `/open-source`, `/license`, `/security`, `/about`, `/roadmap` — each uses `StandalonePage.svelte` wrapper.
- **Breadcrumbs**: Every page includes a `<Breadcrumbs>` component with structured data.
- **CTA**: Every content page ends with a `<CtaSection>`.
- **Comparisons**: Neutral language, visible last-reviewed date (July 23, 2026), methodology notes.
- **Self-hosting pages**: Aligned with the actual README and `selfhosting/` configs. No secrets exposed.

## Route map

```
/                                              Homepage
/features                                      Features hub
/features/[slug]                               Feature detail (10 pages)
/self-hosting                                  Self-hosting hub
/self-hosting/[slug]                           Self-hosting guide (7 pages)
/compare                                       Comparison hub
/compare/[slug]                                Comparison pages (2)
/alternatives                                  Alternatives hub
/alternatives/[slug]                           Alternatives overview (2)
/open-source                                   Open source philosophy
/license                                       Apache 2.0 license details
/security                                      Security practices
/about                                         About Ku / Bakney
/roadmap                                       Development roadmap
/privacy                                       Privacy policy
/sitemap.xml                                   XML sitemap
```

## Build output

All routes prerendered to static HTML in `build/`. The `strict: true` adapter option ensures missing pages fail the build.

## SEO validation

The script at `scripts/validate-seo.mjs` checks:

1. Every route produces an HTML file
2. Unique `<title>` and `<meta name="description">`, exact `<link rel="canonical">`, and an index robots directive on every page
3. Exactly one `<h1>` on every page
4. `sitemap.xml` lists exactly the indexable routes, without duplicates, and `robots.txt` declares it
5. JSON-LD blocks are present and valid JSON; article pages match `src/lib/data/content-modified.json` in metadata and `dateModified`
6. Images have `alt` and intrinsic `width`/`height`
7. Internal links resolve to existing files and fragment links (`#...`) resolve to an element `id` on the same page
8. Product-accuracy guards: Dev Machines pages are labeled unreleased and specific overstated claims are rejected
9. Production CSS keeps the forced-colors fallbacks

The homepage screenshot has responsive 720 px and 1440 px variants. Social metadata uses the dedicated 1200 × 630 `static/social-card.png` asset.
