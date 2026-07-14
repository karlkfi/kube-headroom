import { defineConfig } from "vitepress";
import { fileURLToPath } from "node:url";

// srcDir points outside the VitePress project root (../docs), so Vite's
// node_modules walk-up from those files misses website/node_modules. Pin the
// bare imports that VitePress injects into every compiled page.
const resolveLocal = (p) =>
  fileURLToPath(new URL("../node_modules/" + p, import.meta.url));

// Docs subsite for kube-headroom. Markdown sources live in the repo's docs/
// directory (srcDir below) — this config only curates navigation and theme.
// Deployed under /kube-headroom/docs/ on GitHub Pages; the hand-crafted
// landing page (website/landing/) owns the site root.
export default defineConfig({
  title: "Headroom",
  description:
    "CPU limits that resize to share unrequested node capacity — recomputed on scheduling events, applied via in-place pod resize.",
  base: "/kube-headroom/docs/",
  srcDir: "../docs",
  outDir: "./.vitepress/dist",
  srcExclude: ["STATUS.md", "plan/**"],
  // Repo docs link to files outside the docs tree (config/samples, CLAUDE.md);
  // those are fine on GitHub but unresolvable here.
  ignoreDeadLinks: true,
  appearance: "force-dark",
  vite: {
    resolve: {
      alias: [
        { find: /^vue\/server-renderer$/, replacement: resolveLocal("@vue/server-renderer/dist/server-renderer.esm-bundler.js") },
        { find: /^vue$/, replacement: resolveLocal("vue/dist/vue.runtime.esm-bundler.js") },
      ],
    },
  },
  head: [
    ["link", { rel: "icon", type: "image/svg+xml", href: "/kube-headroom/favicon.svg" }],
  ],
  themeConfig: {
    siteTitle: "KUBE-HEADROOM / DOCS",
    // the A3 mark in the navbar + the title both return to the landing page
    // (logoLink is used raw, not base-prefixed — same absolute path locally
    // and on Pages). The logo file lives in docs/public/.
    logo: "/logo.svg",
    logoLink: "/kube-headroom/",
    nav: [
      { text: "Design", link: "/design" },
      { text: "Runbook", link: "/runbook" },
      { text: "Tenant Guide", link: "/tenant-guide" },
      { text: "Applicability", link: "/applicability" },
    ],
    sidebar: [
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
