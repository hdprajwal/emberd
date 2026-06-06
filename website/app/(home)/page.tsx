import type { ReactNode } from 'react';
import Link from 'next/link';
import { ArrowRight, ArrowUpRight } from 'lucide-react';

const GITHUB = 'https://github.com/hdprajwal/emberd';

const stats = [
  { value: '≈450 ms', label: 'Cold boot' },
  { value: '256 MiB', label: 'Guest RAM' },
  { value: '1 : 1', label: 'VM per sandbox' },
  { value: 'None', label: 'Network by default' },
];

const features = [
  {
    n: '01',
    title: 'Real hardware isolation',
    body: 'Every sandbox is its own KVM microVM with its own Linux kernel. A guest escape has to beat the hypervisor — not just a namespace it shares with the host.',
  },
  {
    n: '02',
    title: 'Serving in ~450 ms',
    body: 'A custom Go initramfs boots an overlayfs root and switch_roots in. create blocks on a vsock readiness probe, so a returned sandbox is usable on the very first exec.',
  },
  {
    n: '03',
    title: 'A control plane you can read',
    body: 'Length-prefixed JSON over a vsock socket — no IP stack. v0.1 sandboxes run with no network device at all, and PID 1 reaps orphaned processes so nothing leaks.',
  },
];

export default function HomePage() {
  return (
    <>
      {/* ── header ─────────────────────────────────────────────── */}
      <header className="ember-rise sticky top-0 z-20 border-b bd backdrop-blur-md">
        <div
          className="absolute inset-0 -z-10"
          style={{ background: 'rgba(10,10,11,0.62)' }}
          aria-hidden
        />
        <div className="mx-auto flex h-14 w-full max-w-5xl items-center justify-between px-6">
          <Link href="/" className="flex items-center gap-2.5">
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img src="/logo.svg" alt="" width={23} height={20} className="h-5 w-auto" />
            <span className="ember-mono text-[0.95rem] font-medium tracking-tight">
              emberd
            </span>
            <span className="ember-mono rounded border bd px-1.5 py-0.5 text-[0.62rem] tracking-wide t-faint">
              v0.1
            </span>
          </Link>
          <nav className="ember-mono flex items-center gap-6 text-[0.82rem]">
            <Link href="/docs" className="ember-navlink">
              Docs
            </Link>
            <Link href="/docs/roadmap" className="ember-navlink hidden sm:inline">
              Roadmap
            </Link>
            <a
              href={GITHUB}
              aria-label="emberd on GitHub"
              className="ember-navlink inline-flex items-center"
            >
              <GithubIcon className="h-[18px] w-[18px]" />
            </a>
          </nav>
        </div>
      </header>

      <main className="flex-1">
        {/* ── hero ─────────────────────────────────────────────── */}
        <section className="mx-auto w-full max-w-5xl px-6 pb-24 pt-24 sm:pt-32">
          <h1
            className="ember-rise max-w-3xl font-semibold leading-[1.03] tracking-[-0.035em]"
            style={{ fontSize: 'clamp(2.6rem, 5.4vw, 4rem)', animationDelay: '120ms' }}
          >
            Run agent code in a{' '}
            <span className="t-accent">microVM</span>,
            <br className="hidden sm:block" /> not a container.
          </h1>

          <p
            className="ember-rise mt-6 max-w-[33rem] text-[1.13rem] leading-[1.6] t-sub sm:mt-7"
            style={{ animationDelay: '180ms' }}
          >
            A local-first, open-source runtime that runs AI-agent tool calls in
            isolated Firecracker microVMs — real hardware isolation, not a shared
            kernel.
          </p>

          <div
            className="ember-rise mt-10 flex flex-wrap items-center gap-3"
            style={{ animationDelay: '240ms' }}
          >
            <Link
              href="/docs"
              className="ember-btn rounded-md px-5 py-2.5 text-sm font-medium"
            >
              Read the docs
            </Link>
            <Link
              href="/docs/getting-started"
              className="ember-ghost rounded-md px-5 py-2.5 text-sm font-medium"
            >
              Get started
            </Link>
            <a
              href={GITHUB}
              className="ember-mono ml-1 inline-flex items-center gap-1.5 px-2 py-2.5 text-[0.82rem] t-muted transition-colors hover:text-[var(--ember-fg)]"
            >
              View source <ArrowUpRight className="h-4 w-4" strokeWidth={2} aria-hidden />
            </a>
          </div>

          {/* ── terminal ───────────────────────────────────────── */}
          <div
            className="ember-rise ember-term mt-16 overflow-hidden rounded-xl sm:mt-20"
            style={{ animationDelay: '320ms' }}
          >
            <div className="flex items-center gap-2 border-b bd px-4 py-3">
              <span className="flex gap-1.5" aria-hidden>
                <span className="h-2.5 w-2.5 rounded-full" style={{ background: 'rgba(255,255,255,0.13)' }} />
                <span className="h-2.5 w-2.5 rounded-full" style={{ background: 'rgba(255,255,255,0.13)' }} />
                <span className="h-2.5 w-2.5 rounded-full" style={{ background: 'rgba(255,255,255,0.13)' }} />
              </span>
              <span className="ember-mono ml-2 text-[0.74rem] t-muted">
                emberd — localhost:7777
              </span>
            </div>

            <div className="ember-mono overflow-x-auto px-5 py-6 text-[0.8rem] leading-[1.7] sm:px-7 sm:text-[0.9rem]">
              <Line>
                <Prompt /> curl -X <Method>POST</Method> :7777/sandboxes
              </Line>
              <Line muted>
                {'{ "id": "sb_c1728b82ac4f" }'}
                <span className="t-faint">  # boots a microVM · ~450 ms</span>
              </Line>

              <Spacer />

              <Line>
                <Prompt /> curl -X <Method>POST</Method> :7777/sandboxes/sb_c17.../exec \
              </Line>
              <Line>
                {'     -d '}
                <span className="t-amber">{`'{"code":"print(6*7)"}'`}</span>
              </Line>
              <Line muted>
                {'{ "stdout": "'}
                <span className="t-accent">42</span>
                {'\\n", "exit_code": '}
                <span className="t-accent">0</span>
                {' }'}
              </Line>

              <Spacer />

              <Line>
                <Prompt /> curl -X <Method del>DELETE</Method> :7777/sandboxes/sb_c17...
              </Line>
              <Line muted>
                204 No Content
                <span className="t-faint">  # VM gone, overlay discarded</span>
              </Line>

              <Line>
                <Prompt />
                <span className="ember-caret" aria-hidden />
              </Line>
            </div>
          </div>
        </section>

        {/* ── stats ────────────────────────────────────────────── */}
        <section className="mx-auto w-full max-w-5xl px-6">
          <h2 className="ember-mono mb-6 text-[0.72rem] uppercase tracking-[0.2em] t-muted">
            <span className="t-accent">/</span> Measured on the reference host
          </h2>
          <dl className="grid grid-cols-2 border-l border-t bd sm:grid-cols-4">
            {stats.map((s) => (
              <div key={s.label} className="border-b border-r bd px-5 py-8 sm:px-6">
                <dt className="tnum text-[1.9rem] font-semibold leading-none tracking-[-0.02em] sm:text-[2.15rem]">
                  {s.value}
                </dt>
                <dd className="ember-mono mt-3 text-[0.7rem] uppercase tracking-[0.14em] t-muted">
                  {s.label}
                </dd>
              </div>
            ))}
          </dl>
        </section>

        {/* ── features ─────────────────────────────────────────── */}
        <section className="mx-auto w-full max-w-5xl px-6 pt-28">
          <h2 className="ember-mono mb-8 text-[0.72rem] uppercase tracking-[0.2em] t-muted">
            <span className="t-accent">/</span> Why a microVM
          </h2>
          <div className="grid border-l border-t bd md:grid-cols-3">
            {features.map((f) => (
              <article key={f.n} className="ember-cell border-b border-r bd p-8">
                <div className="ember-mono mb-6 text-[0.85rem] t-accent">{f.n}</div>
                <h3 className="text-[1.15rem] font-semibold tracking-tight">{f.title}</h3>
                <p className="mt-3.5 text-[0.95rem] leading-[1.65] t-muted">
                  {f.body}
                </p>
              </article>
            ))}
          </div>
        </section>

        {/* ── closing CTA ──────────────────────────────────────── */}
        <section className="mx-auto w-full max-w-5xl px-6 pb-28 pt-28">
          <div className="border-t bd pt-24 text-center">
            <h2 className="mx-auto max-w-xl text-[2rem] font-semibold tracking-[-0.025em] sm:text-[2.6rem]">
              Boot your first sandbox.
            </h2>
            <p className="ember-mono mx-auto mt-5 max-w-md text-[0.85rem] t-muted">
              Three endpoints. One daemon. Runs on your machine.
            </p>
            <div className="mt-9 flex items-center justify-center">
              <Link
                href="/docs/getting-started"
                className="ember-btn inline-flex items-center gap-2 rounded-md px-5 py-2.5 text-sm font-medium"
              >
                Read the getting-started guide
                <ArrowRight className="h-4 w-4" strokeWidth={2} aria-hidden />
              </Link>
            </div>
          </div>
        </section>
      </main>

      {/* ── footer ─────────────────────────────────────────────── */}
      <footer className="border-t bd">
        <div className="mx-auto flex w-full max-w-5xl flex-col gap-6 px-6 py-10 sm:flex-row sm:items-center sm:justify-between">
          <div className="flex items-center gap-2.5">
            {/* eslint-disable-next-line @next/next/no-img-element */}
            <img src="/logo.svg" alt="" width={23} height={20} className="h-5 w-auto" />
            <span className="ember-mono text-sm">emberd</span>
            <span className="t-faint">·</span>
            <span className="text-sm t-muted">
              Firecracker microVM sandboxing runtime
            </span>
          </div>
          <nav className="ember-mono flex flex-wrap items-center gap-5 text-[0.8rem]">
            <Link href="/docs" className="ember-navlink">Docs</Link>
            <Link href="/docs/roadmap" className="ember-navlink">Roadmap</Link>
            <Link href="/docs/design-notes" className="ember-navlink">Design notes</Link>
            <a href={GITHUB} className="ember-navlink">GitHub</a>
          </nav>
        </div>
      </footer>
    </>
  );
}

/* ── small presentational helpers for the terminal ───────────────── */

function Line({
  children,
  muted = false,
}: {
  children: ReactNode;
  muted?: boolean;
}) {
  return (
    <div className={muted ? 't-muted' : 't-fg'} style={{ whiteSpace: 'pre' }}>
      {children}
    </div>
  );
}

function Prompt() {
  return <span className="t-accent">$</span>;
}

function Method({
  children,
  del = false,
}: {
  children: ReactNode;
  del?: boolean;
}) {
  return (
    <span className="font-medium" style={{ color: del ? 'var(--ember-amber)' : 'var(--ember-accent)' }}>
      {children}
    </span>
  );
}

function Spacer() {
  return <div aria-hidden style={{ height: '0.9em' }} />;
}

function GithubIcon({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 16 16" fill="currentColor" aria-hidden className={className}>
      <path
        fillRule="evenodd"
        clipRule="evenodd"
        d="M8 0C3.58 0 0 3.58 0 8C0 11.54 2.29 14.53 5.47 15.59C5.87 15.66 6.02 15.42 6.02 15.21C6.02 15.02 6.01 14.39 6.01 13.72C4 14.09 3.48 13.23 3.32 12.78C3.23 12.55 2.84 11.84 2.5 11.65C2.22 11.5 1.82 11.13 2.49 11.12C3.12 11.11 3.57 11.7 3.72 11.94C4.44 13.15 5.59 12.81 6.05 12.6C6.12 12.08 6.33 11.73 6.56 11.53C4.78 11.33 2.92 10.64 2.92 7.58C2.92 6.71 3.23 5.99 3.74 5.43C3.66 5.23 3.38 4.41 3.82 3.31C3.82 3.31 4.49 3.1 6.02 4.13C6.66 3.95 7.34 3.86 8.02 3.86C8.7 3.86 9.38 3.95 10.02 4.13C11.55 3.09 12.22 3.31 12.22 3.31C12.66 4.41 12.38 5.23 12.3 5.43C12.81 5.99 13.12 6.7 13.12 7.58C13.12 10.65 11.25 11.33 9.47 11.53C9.76 11.78 10.01 12.26 10.01 13.01C10.01 14.08 10 14.94 10 15.21C10 15.42 10.15 15.67 10.55 15.59C13.71 14.53 16 11.53 16 8C16 3.58 12.42 0 8 0Z"
      />
    </svg>
  );
}
