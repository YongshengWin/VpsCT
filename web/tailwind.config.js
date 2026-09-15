/** @type {import('tailwindcss').Config} */
export default {
  darkMode: "class",
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    container: { center: true, padding: "1rem" },
    extend: {
      colors: {
        border: "hsl(var(--border))",
        input: "hsl(var(--input))",
        ring: "hsl(var(--ring))",
        background: "hsl(var(--background))",
        foreground: "hsl(var(--foreground))",
        primary: { DEFAULT: "hsl(var(--primary))", foreground: "hsl(var(--primary-foreground))" },
        secondary: { DEFAULT: "hsl(var(--secondary))", foreground: "hsl(var(--secondary-foreground))" },
        destructive: { DEFAULT: "hsl(var(--destructive))", foreground: "hsl(var(--destructive-foreground))" },
        muted: { DEFAULT: "hsl(var(--muted))", foreground: "hsl(var(--muted-foreground))" },
        accent: { DEFAULT: "hsl(var(--accent))", foreground: "hsl(var(--accent-foreground))" },
        card: { DEFAULT: "hsl(var(--card))", foreground: "hsl(var(--card-foreground))" },
        // soft pastel surfaces for hero / feature cards (see index.css)
        tint: {
          peach: "hsl(var(--tint-peach))",
          lavender: "hsl(var(--tint-lavender))",
          rose: "hsl(var(--tint-rose))",
          mint: "hsl(var(--tint-mint))",
          sky: "hsl(var(--tint-sky))",
          sand: "hsl(var(--tint-sand))",
        },
      },
      // softer corner scale across the whole app
      borderRadius: {
        sm: "0.5rem",
        DEFAULT: "0.625rem",
        md: "0.75rem",
        lg: "1rem",
        xl: "1.25rem",
        "2xl": "1.5rem",
        "3xl": "2rem",
      },
      boxShadow: {
        card: "0 1px 2px rgba(16, 24, 40, 0.04), 0 6px 20px -8px rgba(16, 24, 40, 0.08)",
        lift: "0 2px 4px rgba(16, 24, 40, 0.06), 0 14px 32px -12px rgba(16, 24, 40, 0.16)",
        pop: "0 8px 32px -8px rgba(16, 24, 40, 0.24)",
      },
      fontSize: {
        "2xs": ["0.6875rem", { lineHeight: "1rem" }],
      },
      keyframes: {
        "fade-up": { from: { opacity: "0", transform: "translateY(6px)" }, to: { opacity: "1", transform: "translateY(0)" } },
      },
      animation: {
        // `backwards` (not `both`): once finished no transform remains, so a
        // wrapper using this animation does not become the containing block
        // for position:fixed children such as dialogs.
        "fade-up": "fade-up 0.25s ease-out backwards",
      },
    },
  },
  plugins: [],
};
