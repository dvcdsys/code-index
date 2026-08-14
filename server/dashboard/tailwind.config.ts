import type { Config } from 'tailwindcss';
import animate from 'tailwindcss-animate';

// cix design system — cream & ink. The palette lives in src/index.css as
// space-separated RGB channel vars so the `.dark` class can swap the whole
// theme and Tailwind can still apply opacity modifiers (`bg-ink/40`).
//
// Two deliberate overrides enforce the system's geometry globally:
//   borderWidth.DEFAULT = 1.5px  — every outline in the UI is the same weight.
//   borderRadius.*      = 0      — controls are square; only `rounded-card`
//                                  (12px) rounds, and it is for cards only.
// A stray `rounded-md` copied in from somewhere therefore renders square
// instead of quietly breaking the look.
const c = (v: string) => `rgb(var(${v}) / <alpha-value>)`;

export default {
  darkMode: 'class',
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    borderRadius: {
      none: '0px',
      DEFAULT: '0px',
      sm: '0px',
      md: '0px',
      lg: '0px',
      xl: '0px',
      '2xl': '0px',
      '3xl': '0px',
      card: '12px',
      full: '9999px', // radio only — the single circle in the system
    },
    extend: {
      colors: {
        canvas: c('--c-canvas'),
        surface: {
          DEFAULT: c('--c-surface'),
          alt: c('--c-surface-alt'),
          head: c('--c-surface-head'),
          hover: c('--c-surface-hover'),
        },
        // The one surface a user types into. Kept a separate role rather than
        // an alias of `surface` so a control never silently inherits whatever
        // layer it happens to be dropped on.
        field: c('--c-field'),
        track: c('--c-track'),
        warm: {
          DEFAULT: c('--c-warm'),
          deep: c('--c-warm-deep'),
        },
        ink: {
          DEFAULT: c('--c-ink'),
          soft: c('--c-ink-soft'),
        },
        dim: c('--c-dim'),
        muted: c('--c-muted'),
        faint: c('--c-faint'),
        line: {
          soft: c('--c-line-soft'),
          quiet: c('--c-line-quiet'),
        },
        accent: {
          DEFAULT: c('--c-accent'),
          press: c('--c-accent-press'),
          deep: c('--c-accent-deep'),
        },
        ok: c('--c-ok'),
        warn: {
          DEFAULT: c('--c-warn'),
          ink: c('--c-warn-ink'),
          bg: c('--c-warn-bg'),
        },
        danger: {
          bg: c('--c-danger-bg'),
          ink: c('--c-danger-ink'),
        },
        well: {
          DEFAULT: c('--c-well'),
          text: c('--c-well-text'),
        },
      },
      borderWidth: {
        DEFAULT: '1.5px',
        0: '0px',
        2: '2px',
        3: '3px',
        6: '6px',
      },
      boxShadow: {
        hard: '4px 4px 0 var(--cix-shadow-color)',
        'hard-sm': '2px 2px 0 var(--cix-shadow-color)',
        'hard-lg': '8px 8px 0 var(--cix-shadow-color)',
        'hard-accent': '4px 4px 0 rgb(var(--c-accent))',
        none: 'none',
      },
      fontFamily: {
        sans: ['Helvetica Neue', 'Helvetica', 'Inter', 'system-ui', 'sans-serif'],
        mono: ['JetBrains Mono', 'ui-monospace', 'SF Mono', 'Menlo', 'monospace'],
      },
      keyframes: {
        'cix-in': {
          from: { opacity: '0' },
          to: { opacity: '1' },
        },
      },
      animation: {
        'cix-in': 'cix-in 90ms ease-out',
      },
    },
  },
  plugins: [animate],
} satisfies Config;
