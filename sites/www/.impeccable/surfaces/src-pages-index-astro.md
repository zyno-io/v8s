---
version: 1
slug: "src-pages-index-astro"
primary_target: "src/pages/index.astro"
related_targets: []
---

# Surface: v8s home page (`/`)

Mode: **Persuade**. Scope: the single marketing page for v8s at v8s.io (plus 404). World: settled by the
product, the PixelCI design system the v8s app already uses (cool gray desk, white hairline cards, 32px
rows, status tag triplets, Inter + Font Awesome Free) with v8s indigo (#4f46e5 light, #5b52f0 dark).

Audience: platform/SRE/DevOps engineers running several clusters. Action: download the right desktop
build for this computer (release-aware from the GitHub API), or run `v8s serve` / deploy hosted mode;
secondary: docs. Proof: real demo-mode screenshots (synthetic clusters), real commands, the stated design
budgets (labeled as budgets). Constraints: no invented users, logos, testimonials or measured benchmarks.
Footer carries the Zyno Consulting pitch for Kubernetes help (owner's brief) using only claims on
zyno.consulting (infrastructure and DevOps, Kubernetes/AWS migrations, CI/CD, monitoring and incident
response, embedded consulting); v8s never requires it.

Surface round (attended, decision page): grounded structures by resonance: 1 only the rows on screen;
2 the live table is the page; 3 the command bar is the page; 4 problems first; 5 three surfaces, one
binary; 6 your credential all the way through; 7 connect only what you pick. Dealt 3 (lead), 4, 1. The
owner locked 3. Challengers declined at surface scope (identity fixed); disciplines kept: darkroom
safelight (preview before commit: the YAML dry-run diff gets its own section); toy-catalogue spread
(density courage: problem families and integrations packed as 32px rows, not airy cards).

## Direction contract

THESIS: v8s is driven from a `:` command bar; so is the page. Each section is headed by the real
command that opens it in the app (`:ctx`, `:pb`, `:ks`, `:po`), and pressing `:` anywhere opens a
working command bar that jumps between them. Refuses the category default of a hero over three
feature cards and a logo strip.

OWN-WORLD: the app's own chrome at marketing scale: Desk Gray ground, white hairline cards with 12/8/5
radii, 32px table rows with 11px caps headers, tag triplets carrying status words, mono for commands
and machine text, one indigo for action/selection/focus only. Dark theme is the app's near-black. No
gradients, no glass, no illustration; the cluster is the content.

STORY: the visitor sees the real app answering `:po pay` across three clusters, understands it is a
fast multi-cluster manager, learns why it is fast (agent projections, rows on screen), sees Problems,
Flux/Helm, security and network, the safe-edit flow, the three ways to run it, and downloads.

FIRST VIEWPORT: header with brand and mono command nav (`:problems :ctx :flux :security :run`) plus a
`:` key cap hint; left, 64px headline and lede; right, a download card whose primary button names the
detected OS build; below spanning the column, the real app screenshot in a window frame with the
command bar open, rising into the fold.

FORM: structure 3 of 7 ("the command bar is the page"), seed:013ae65df9884dcc. Signature interaction:
press `:` (or click the bar) to open the page's command bar with prefix-then-fuzzy completion over the
section commands; Enter scrolls to the section and the sticky bar shows the active command. Motion:
the command bar's own open/close and a typed-command reveal per section heading, once, reduced-motion
safe.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance

## Amendments (after the finish review)

- Header nav stays plain words (Why it's fast, Features, Run it, Docs) plus a `:` command button, not mono
  commands: `:run`, `:security`, `:flux` aren't real v8s commands, and "only real commands" outranks the
  contract. Every section with a demo-able command carries its Run chip in the heading-row slot instead;
  sections without one carry doc links there.
- The first viewport's app frame is the interactive replica (owner's mid-build request), with the command bar
  open on `po pay` as the intro's end state (typed live, or a still under reduced motion).
- The Flux graph screenshot was removed: the app's graph renders its labels in a serif fallback font. Re-add it
  once v8s fixes that, using scripts/capture-shots.ts.

