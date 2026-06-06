import { JetBrains_Mono, Schibsted_Grotesk } from 'next/font/google';

// Distinctive, technical type system — a refined grotesque for display/body and
// an editor-grade monospace for the labels, code, and stats. Scoped to the
// landing via CSS variables so the Fumadocs docs keep their own font.
const display = Schibsted_Grotesk({
  subsets: ['latin'],
  variable: '--font-ember-display',
  display: 'swap',
});

const mono = JetBrains_Mono({
  subsets: ['latin'],
  variable: '--font-ember-mono',
  display: 'swap',
});

export default function Layout({ children }: LayoutProps<'/'>) {
  return (
    <div className={`ember-landing ${display.variable} ${mono.variable}`}>
      <div aria-hidden className="ember-grid-bg" />
      <div aria-hidden className="ember-glow-bg" />
      <div aria-hidden className="ember-grain" />
      {children}
    </div>
  );
}
