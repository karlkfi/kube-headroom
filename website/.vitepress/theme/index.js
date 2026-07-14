import DefaultTheme from "vitepress/theme";
import { h } from "vue";
import "./custom.css";

// The navbar title anchor (icon + "KUBE-HEADROOM") links to the landing page
// (see logoLink in config). This "/ DOCS" suffix renders inside that anchor —
// nested <a> is invalid HTML, so it's a role=link span that intercepts its
// own clicks and goes to the docs home instead.
const DOCS_HOME = "/kube-headroom/docs/";

function goDocs(e) {
  e.preventDefault();
  e.stopPropagation();
  window.location.assign(DOCS_HOME);
}

const DocsHomeLink = () =>
  h(
    "span",
    {
      class: "title-docs-link",
      role: "link",
      tabindex: 0,
      "aria-label": "Docs home",
      onClick: goDocs,
      onKeydown(e) {
        if (e.key === "Enter" || e.key === " ") goDocs(e);
      },
    },
    "/ DOCS"
  );

export default {
  extends: DefaultTheme,
  Layout() {
    return h(DefaultTheme.Layout, null, {
      "nav-bar-title-after": DocsHomeLink,
    });
  },
};
