import { JetBrains_Mono, Schibsted_Grotesk } from 'next/font/google';

// Paper-white spec-sheet landing. Schibsted Grotesk carries the display voice,
// JetBrains Mono the labels/code; both scoped to the landing via CSS variables
// so the Fumadocs docs keep their own fonts.
const display = Schibsted_Grotesk({
  subsets: ['latin'],
  variable: '--font-paper-display',
  display: 'swap',
});

const mono = JetBrains_Mono({
  subsets: ['latin'],
  variable: '--font-paper-mono',
  display: 'swap',
});

export default function Layout({ children }: LayoutProps<'/'>) {
  return (
    <div className={`paper-landing ${display.variable} ${mono.variable}`}>
      {children}
    </div>
  );
}
