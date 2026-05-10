import type { Config } from "tailwindcss";

export default {
  content: ["./index.html", "./src/**/*.{ts,svelte}"],
  darkMode: "class",
  theme: {
    extend: {
      colors: {
        accent: {
          50: "#ecfeff",
          200: "#a5f3fc",
          300: "#7dd3fc",
          400: "#38bdf8",
          500: "#0ea5e9",
        },
      },
      fontFamily: {
        sans: ["ui-sans-serif", "system-ui", "-apple-system", "Inter", "sans-serif"],
        mono: ["ui-monospace", "SFMono-Regular", "Menlo", "monospace"],
      },
    },
  },
} satisfies Config;
