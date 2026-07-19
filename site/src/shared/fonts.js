// Single import point for self-hosted fonts (both pages import this module,
// so weights can never drift between landing and docs).
//
// latin-only subsets on purpose: the copy is English/ASCII, and the bare
// `@fontsource/<family>/<weight>.css` entrypoints would emit ~55 woff2 files
// (arabic, gujarati, cyrillic…) into dist for nothing.
//
// Weights are the exact set the CSS uses — check styles.css/docs.css before
// adding or removing one (e.g. JetBrains Mono 800 backs the stamp/sticker
// text; without it browsers render smeared synthetic bold).
import '@fontsource/rubik/latin-400.css';
import '@fontsource/rubik/latin-500.css';
import '@fontsource/rubik/latin-600.css';
import '@fontsource/rubik/latin-700.css';
import '@fontsource/rubik/latin-800.css';
import '@fontsource/shrikhand/latin-400.css';
import '@fontsource/jetbrains-mono/latin-400.css';
import '@fontsource/jetbrains-mono/latin-600.css';
import '@fontsource/jetbrains-mono/latin-700.css';
import '@fontsource/jetbrains-mono/latin-800.css';
