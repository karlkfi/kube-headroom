<script setup>
// Replaces the default VPNavBarTitle (aliased in config.mjs) so the brand and
// "/ DOCS" are two REAL sibling anchors: correct hover URLs, no nested-<a>
// hacks. Brand → landing page (target=_self escapes the SPA router); DOCS →
// docs home (in-app navigation).
import { withBase } from "vitepress";
import { useSidebar } from "vitepress/theme";

const { hasSidebar } = useSidebar();
</script>

<template>
  <div class="VPNavBarTitle" :class="{ 'has-sidebar': hasSidebar }">
    <a class="title" href="/" target="_self">
      <img class="logo" :src="withBase('/logo.svg')" alt="" />
      <span>KUBE-HEADROOM</span>
    </a>
    <a class="title docs-title" :href="withBase('/')">/&nbsp;DOCS</a>
  </div>
</template>

<style scoped>
.VPNavBarTitle {
  display: flex;
  align-items: center;
}

.title {
  display: flex;
  align-items: center;
  border-bottom: 1px solid transparent;
  height: var(--vp-nav-height);
  font-size: 16px;
  font-weight: 600;
  color: var(--vp-c-text-1);
  transition: opacity 0.25s;
  white-space: nowrap;
}

@media (min-width: 960px) {
  .title {
    flex-shrink: 0;
  }

  .VPNavBarTitle.has-sidebar .title {
    border-bottom-color: var(--vp-c-divider);
  }
}

.logo {
  margin-right: 8px;
  height: var(--vp-nav-logo-height);
}

.docs-title {
  margin-left: 7px;
  color: var(--vp-c-text-3);
}

.docs-title:hover,
.docs-title:focus-visible {
  color: var(--vp-c-brand-1);
}
</style>
