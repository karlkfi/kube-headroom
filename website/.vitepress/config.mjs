import { defineConfig } from "vitepress";
import { fileURLToPath } from "node:url";

// srcDir points outside the VitePress project root (../docs), so Vite's
// node_modules walk-up from those files misses website/node_modules. Pin the
// bare imports that VitePress injects into every compiled page.
const resolveLocal = (p) =>
  fileURLToPath(new URL("../node_modules/" + p, import.meta.url));

const SITE = "https://kube-headroom.dev";

// Per-page meta descriptions, keyed by path relative to srcDir. These live here
// rather than in markdown frontmatter because docs/ is read on GitHub too,
// where a frontmatter block renders as a stray table above the first heading.
// A page missing from this map falls back to the site description below.
const descriptions = {
  "index.md":
    "Documentation for Headroom, the Kubernetes controller that resizes CPU limits to node slack — design doc, operator runbook, tenant guide, and applicability matrix.",
  "design.md":
    "The Headroom design doc and source of truth: problem statement, the mechanism research behind in-place resize, the policy design, and the architecture.",
  "runbook.md":
    "Operator runbook for Headroom: preflight, install and rollout, day-2 triage for throttled pods, resize outcomes, metrics and dashboards, and failure modes.",
  "tenant-guide.md":
    "The Headroom contract for app teams: what you'll see, opting in and out, capping your own ceiling, the startup-quota caveat, and running alongside VPA and HPA.",
  "applicability.md":
    "When to run Headroom and when not to: workload applicability, behavior by scheduling mode, and interactions with HPA, VPA, and quota tooling.",
  "cpu-footguns.md":
    "Runtimes that size themselves from the wrong CPU count — Go, JVM, Python, OpenMP/OpenBLAS, Node.js, .NET — and the two universal workarounds for each.",
  "helm-migration.md":
    "Migrating a Headroom install from kustomize to Helm: what changes, adopting in place with no downtime or uninstall/reinstall, and how to verify the result.",
  "news/index.md":
    "Release announcements and project news for Headroom, the Kubernetes controller that resizes CPU limits to share unrequested node capacity.",
  "news/2026-07-headroom-v0.1.0.md":
    "Headroom v0.1.0, the first public release: what's in it, why it ships dry-run by default, how to install the Helm charts, and whether you should run it yet.",
  "development/README.md":
    "The development process for Headroom: how work is picked from the backlog, branch and PR conventions, and the testing, debt, and release process docs.",
  "development/testing.md":
    "How Headroom is tested: the test tiers, how fast each tier should stay, and how to choose a tier for a new test.",
  "development/kind-iteration.md":
    "The kind inner loop for Headroom: reusing the cluster, defeating the image cache with unique tags, rolling a new image without redeploying, and debug pods.",
  "development/kubernetes-conventions.md":
    "Kubernetes API conventions Headroom follows: enum-keyword label values, keys derived from one prefix, the two-tier condition ladder, events, and metrics.",
  "development/documentation-standards.md":
    "How docs are written in the Headroom repo: specifics over adjectives, one canonical home per fact, one term per concept, and honest not-yet-implemented notes.",
  "development/technical-debt.md":
    "The Headroom technical debt policy: the fix / flag / defer / decline decision, why flake fixes go to the top of the queue, and secure-by-default as a hard rule.",
  "development/releasing.md":
    "Releasing Headroom: the one invariant that image tags match the stamped chart appVersion, the tag-and-publish steps, and the versioning notes.",
};

// design.md -> https://kube-headroom.dev/docs/design.html; index.md -> the
// directory itself. Mirrors the rewrite VitePress's sitemap generator applies,
// so canonical URLs and sitemap entries name the same page.
const pageURL = (relativePath) =>
  SITE +
  "/docs/" +
  relativePath.replace(/(^|\/)index\.md$/, "$1").replace(/\.md$/, ".html");

// Docs subsite for kube-headroom. Markdown sources live in the repo's docs/
// directory (srcDir below) — this config only curates navigation and theme.
// Deployed at https://kube-headroom.dev/docs/ (GitHub Pages custom domain);
// the hand-crafted landing page (website/landing/) owns the site root.
export default defineConfig({
  title: "Headroom",
  description:
    "CPU limits that resize to share unrequested node capacity — recomputed on scheduling events, applied via in-place pod resize.",
  base: "/docs/",
  srcDir: "../docs",
  outDir: "./.vitepress/dist",
  srcExclude: ["STATUS.md", "plan/**"],
  // Repo docs link to files outside the docs tree (config/samples, CLAUDE.md);
  // those are fine on GitHub but unresolvable here.
  ignoreDeadLinks: true,
  appearance: "force-dark",
  // hostname must carry `base` — VitePress resolves each page against it, so
  // dropping /docs/ here would emit sitemap URLs pointing at the landing site.
  // Emitted to /docs/sitemap.xml; the root sitemap index references it.
  sitemap: { hostname: SITE + "/docs/" },
  vite: {
    resolve: {
      alias: [
        { find: /^vue\/server-renderer$/, replacement: resolveLocal("@vue/server-renderer/dist/server-renderer.esm-bundler.js") },
        { find: /^vue$/, replacement: resolveLocal("vue/dist/vue.runtime.esm-bundler.js") },
        // navbar title: two real anchors (brand → landing, /DOCS → docs home)
        { find: /^.*\/VPNavBarTitle\.vue$/, replacement: fileURLToPath(new URL("./theme/CustomNavBarTitle.vue", import.meta.url)) },
      ],
    },
  },
  head: [
    ["link", { rel: "icon", type: "image/svg+xml", href: "/favicon.svg" }],
    // Social preview — served from the landing page root, shared by every
    // docs page. Regenerate with website/og/render.sh.
    ["meta", { property: "og:site_name", content: "Headroom" }],
    ["meta", { property: "og:image", content: "https://kube-headroom.dev/og.png" }],
    ["meta", { property: "og:image:width", content: "1200" }],
    ["meta", { property: "og:image:height", content: "630" }],
    ["meta", { name: "twitter:card", content: "summary_large_image" }],
  ],
  transformPageData(pageData) {
    const description = descriptions[pageData.relativePath];
    if (description) pageData.description = description;
  },
  // Without these every docs page inherits the site card: one shared
  // description, no canonical, and an identical link preview.
  transformHead({ pageData, description }) {
    // VitePress renders its own 404 through this hook. A 404 must not claim a
    // canonical URL, and its relativePath differs across versions — skip both
    // spellings rather than advertise the docs index as its canonical.
    if (!pageData.relativePath || pageData.relativePath === "404.md") return [];
    const url = pageURL(pageData.relativePath);
    return [
      ["link", { rel: "canonical", href: url }],
      ["meta", { property: "og:url", content: url }],
      ["meta", { property: "og:type", content: "article" }],
      ["meta", { property: "og:title", content: pageData.title }],
      ["meta", { property: "og:description", content: description }],
    ];
  },
  themeConfig: {
    // The navbar title is a custom component (theme/CustomNavBarTitle.vue,
    // swapped in via the vite alias above): brand anchor → landing page,
    // "/ DOCS" anchor → docs home. siteTitle/logo/logoLink are unused there
    // but kept for anything else that reads them (e.g. local search).
    siteTitle: "KUBE-HEADROOM",
    logo: "/logo.svg",
    logoLink: { link: "/", target: "_self" },
    nav: [
      { text: "News", link: "/news/" },
      { text: "Design", link: "/design" },
      { text: "Runbook", link: "/runbook" },
      { text: "Tenant Guide", link: "/tenant-guide" },
      { text: "Applicability", link: "/applicability" },
    ],
    sidebar: [
      {
        text: "News",
        items: [
          { text: "v0.1.0 — first public release", link: "/news/2026-07-headroom-v0.1.0" },
        ],
      },
      {
        text: "Architecture",
        items: [{ text: "Design (source of truth)", link: "/design" }],
      },
      {
        text: "Operators",
        items: [
          { text: "Runbook", link: "/runbook" },
          { text: "Helm migration", link: "/helm-migration" },
        ],
      },
      {
        text: "App teams",
        items: [
          { text: "Tenant guide", link: "/tenant-guide" },
          { text: "CPU footguns", link: "/cpu-footguns" },
        ],
      },
      {
        text: "Adoption",
        items: [{ text: "Applicability matrix", link: "/applicability" }],
      },
      {
        text: "Contributing",
        items: [
          { text: "Development process", link: "/development/README" },
          { text: "Testing", link: "/development/testing" },
          { text: "Kind inner loop", link: "/development/kind-iteration" },
          { text: "Kubernetes conventions", link: "/development/kubernetes-conventions" },
          { text: "Documentation standards", link: "/development/documentation-standards" },
          { text: "Technical debt policy", link: "/development/technical-debt" },
          { text: "Releasing", link: "/development/releasing" },
        ],
      },
    ],
    socialLinks: [
      { icon: "github", link: "https://github.com/karlkfi/kube-headroom" },
    ],
    outline: { level: [2, 3] },
    search: { provider: "local" },
  },
});
