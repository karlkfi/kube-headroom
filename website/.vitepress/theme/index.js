import DefaultTheme from "vitepress/theme";
import "./custom.css";

// Navbar title customization (brand → landing, "/ DOCS" → docs home) lives in
// CustomNavBarTitle.vue, swapped in via a vite alias in config.mjs.
export default DefaultTheme;
