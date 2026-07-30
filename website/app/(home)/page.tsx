import type { ReactNode } from 'react';
import Link from 'next/link';
import { CopyButton } from '@/components/copy-button';

const GITHUB = 'https://github.com/hdprajwal/emberd';

/** The one install command. Rendered verbatim and copied verbatim — what the
 *  reader sees is exactly what lands on the clipboard. Single source: change
 *  it here and both the hero block and the copy action follow. */
const INSTALL_CMD = 'curl -fsSL https://emberd.hdprajwal.dev/install.sh | sh';

const specs = [
  { value: '43 ms', label: 'full round trip' },
  { value: '<1 ms', label: 'create, warm pool' },
  { value: '7 MiB', label: 'idle RAM / sandbox' },
  { value: '0', label: 'network devices' },
];

const endpoints = [
  {
    chip: 'POST /sandboxes',
    text: 'Boot a fresh microVM — or take a pre-warmed one off the pool.',
  },
  {
    chip: 'POST /sandboxes/{id}/exec',
    text: 'Run code inside the guest over vsock. stdout, stderr, exit code back.',
  },
  {
    chip: 'DELETE /sandboxes/{id}',
    text: 'Tear the VM down. The overlay is discarded; nothing survives.',
  },
];

export default function HomePage() {
  return (
    <>
      {/* ── nav ────────────────────────────────────────────────── */}
      <header className="w-full">
        <div className="mx-auto flex h-14 w-full max-w-4xl items-center justify-between px-6">
          <div className="flex items-center gap-7">
            <Link href="/" className="flex items-center gap-2.5">
              <InkLogo className="h-5 w-auto" />
              <span className="paper-mono text-[15px] font-medium">emberd</span>
              <span className="paper-mono rounded-full border bd px-2 py-0.5 text-[11px] t-mute">
                v0.1
              </span>
            </Link>
            <nav className="hidden items-center gap-6 sm:flex">
              <Link href="/docs" className="paper-navlink">
                Docs
              </Link>
              <Link href="/docs/roadmap" className="paper-navlink">
                Roadmap
              </Link>
              <Link href="/docs/api-reference" className="paper-navlink">
                API
              </Link>
            </nav>
          </div>
          <div className="flex items-center gap-2.5">
            <a href={GITHUB} className="pill-outline hidden sm:inline-flex">
              GitHub
            </a>
            <Link href="/docs/getting-started" className="pill-primary">
              Get started
            </Link>
          </div>
        </div>
      </header>

      <main className="flex-1">
        {/* ── hero: one stacked column, led by the install command ── */}
        <section className="mx-auto w-full max-w-4xl px-6 pb-8 pt-20 sm:pt-28">
          <h1 className="paper-display rise max-w-3xl text-[34px] font-medium leading-[1.08] sm:text-[52px]">
            Run agent code in a microVM, not a container.
          </h1>
          <p
            className="rise mt-6 max-w-xl text-base leading-[1.6] t-body sm:text-[17px]"
            style={{ animationDelay: '80ms' }}
          >
            Every sandbox gets its own kernel behind KVM. Create one, run
            untrusted code, throw it away — in the time a container takes to
            think about it.
          </p>

          <div className="rise mt-10" style={{ animationDelay: '160ms' }}>
            <div className="flex flex-wrap items-center gap-3">
              <div className="install-block paper-mono">
                <span className="prompt" aria-hidden>
                  $
                </span>
                <span className="cmd">{INSTALL_CMD}</span>
              </div>
              <CopyButton text={INSTALL_CMD} label="Copy" />
            </div>
            <p className="mt-4 text-[13px] t-body">
              Linux + KVM ·{' '}
              <Link href="/docs" className="paper-link-mute">
                read the docs
              </Link>
            </p>
          </div>

          <div
            className="terminal-card rise mt-14 min-w-0"
            style={{ animationDelay: '240ms' }}
          >
            <div className="flex items-center gap-2 px-1 pt-1" aria-hidden>
              <span className="flex gap-1.5">
                <span className="h-3 w-3 rounded-full" style={{ background: 'var(--tl-red)' }} />
                <span className="h-3 w-3 rounded-full" style={{ background: 'var(--tl-yellow)' }} />
                <span className="h-3 w-3 rounded-full" style={{ background: 'var(--tl-green)' }} />
              </span>
              <span className="paper-mono ml-2 text-[11px] t-mute">
                emberd — localhost:7777
              </span>
            </div>
            <div className="paper-mono overflow-x-auto px-1 pb-1 pt-5 text-[13px] leading-[1.8] sm:text-[14px]">
              <Line>
                <Dollar /> curl -X POST :7777/sandboxes
              </Line>
              <Line mute>
                {'{ "id": "sb_c1728b82ac4f" }'}
                <Comment>  # &lt;1 ms from the warm pool</Comment>
              </Line>
              <Gap />
              <Line>
                <Dollar /> curl -X POST :7777/sandboxes/sb_c17.../exec \
              </Line>
              <Line>{`     -d '{"code":"print(6*7)"}'`}</Line>
              <Line mute>{'{ "stdout": "42\\n", "exit_code": 0 }'}</Line>
              <Gap />
              <Line>
                <Dollar /> curl -X DELETE :7777/sandboxes/sb_c17...
              </Line>
              <Line mute>
                204 No Content
                <Comment>  # VM gone, overlay discarded</Comment>
              </Line>
            </div>
          </div>
        </section>

        {/* ── the numbers: no band, no rules — air does the framing ── */}
        <section className="mx-auto w-full max-w-4xl px-6 pt-24 sm:pt-32">
          <dl className="grid grid-cols-2 gap-x-10 gap-y-14 sm:grid-cols-4">
            {specs.map((s) => (
              <div key={s.label} className="flex flex-col gap-3">
                <dt className="paper-mono tnum text-[34px] font-medium leading-none sm:text-[44px]">
                  {s.value}
                </dt>
                <dd className="paper-mono text-[11px] uppercase leading-[1.5] tracking-[0.12em] t-mute">
                  {s.label}
                </dd>
              </div>
            ))}
          </dl>
        </section>

        {/* ── 01 / lifecycle — a horizontal rail, read left to right ─ */}
        <section className="mx-auto w-full max-w-4xl px-6 pt-28">
          <div className="flex flex-wrap items-end justify-between gap-x-8 gap-y-4">
            <div>
              <p className="spec-marker">
                <span className="idx">01</span>
                <span className="lbl">Lifecycle</span>
              </p>
              <h2 className="paper-display mt-5 text-[26px] font-medium leading-[1.2] sm:text-[32px]">
                Create. Exec. Destroy.
              </h2>
            </div>
            <Link href="/docs/api-reference" className="paper-link-mute text-sm">
              Read the API reference →
            </Link>
          </div>
          <p className="mt-5 max-w-xl text-base leading-[1.6] t-body">
            The whole API is three endpoints on one daemon. No fleet
            orchestrator, no YAML, no scheduler — a sandbox is something you
            make, use, and delete.
          </p>

          <ol className="mt-14 grid grid-cols-1 gap-x-8 gap-y-10 sm:grid-cols-3">
            {endpoints.map((e) => (
              <li key={e.chip} className="rail-step pr-4">
                <span className="endpoint-chip w-fit">{e.chip}</span>
                <p className="mt-3 text-sm leading-[1.5] t-body">{e.text}</p>
              </li>
            ))}
          </ol>
        </section>

        {/* ── 02 / speed — the page's one inverted surface, full width ─ */}
        <section className="mx-auto w-full max-w-4xl px-6 pt-28">
          <div className="paper-card-dark p-8 sm:p-12">
            <p className="spec-marker">
              <span className="idx">02</span>
              <span className="lbl" style={{ color: 'var(--on-dark-mute)' }}>
                Speed
              </span>
            </p>
            <h2 className="paper-display mt-5 max-w-lg text-[26px] font-medium leading-[1.2] sm:text-[32px]">
              Fast enough for a tool-call loop.
            </h2>
            <p
              className="mt-5 max-w-lg text-base leading-[1.6]"
              style={{ color: 'var(--on-dark-mute)' }}
            >
              Hardware isolation used to mean waiting for a VM. A warm pool and
              snapshot restore make microVMs cheap enough to hand one to every
              tool call.
            </p>

            <div className="mt-12 grid grid-cols-2 gap-8 sm:max-w-md">
              <div className="flex flex-col gap-2.5">
                <span className="paper-mono tnum text-[38px] font-medium leading-none sm:text-[46px]">
                  &lt;1 ms
                </span>
                <span
                  className="paper-mono text-[11px] uppercase tracking-[0.12em]"
                  style={{ color: 'var(--on-dark-mute)' }}
                >
                  create, warm pool
                </span>
              </div>
              <div className="flex flex-col gap-2.5">
                <span className="paper-mono tnum text-[38px] font-medium leading-none sm:text-[46px]">
                  43 ms
                </span>
                <span
                  className="paper-mono text-[11px] uppercase tracking-[0.12em]"
                  style={{ color: 'var(--on-dark-mute)' }}
                >
                  full round trip
                </span>
              </div>
            </div>

            <div className="mt-12 flex flex-wrap items-end justify-between gap-8">
              <ul className="flex flex-col gap-2.5">
                <li className="paper-check-on-dark">Warm sandboxes handed out before the boot you never see</li>
                <li className="paper-check-on-dark">create blocks on a vsock readiness probe — first exec always works</li>
                <li className="paper-check-on-dark">Measured on the reference host, not estimated</li>
              </ul>
              <Link href="/docs/performance" className="pill-on-dark whitespace-nowrap">
                See the numbers
              </Link>
            </div>
          </div>
        </section>

        {/* ── 03 / local-first — one statement, three vertical-ruled claims ─ */}
        <section className="mx-auto w-full max-w-4xl px-6 pt-28">
          <p className="spec-marker">
            <span className="idx">03</span>
            <span className="lbl">Local-first</span>
          </p>
          <div className="mt-5 flex items-start justify-between gap-8">
            <h2 className="paper-display max-w-xl text-[28px] font-medium leading-[1.15] sm:text-[38px]">
              Your code stays on your machine.
            </h2>
            <LockIcon className="hidden h-16 w-16 flex-none sm:block" />
          </div>

          <div className="mt-14 grid grid-cols-1 gap-x-8 gap-y-8 sm:grid-cols-3">
            {[
              ['One daemon, one binary', 'Runs on your hardware. Nothing to sign up for.'],
              ['No cloud dependency', 'No account, no telemetry, no phone-home.'],
              ['No network by default', 'Sandboxes get no network device unless you add one.'],
            ].map(([title, body]) => (
              <div key={title} className="border-l bd pl-5">
                <p className="paper-mono text-[13px] font-medium">{title}</p>
                <p className="mt-2.5 text-sm leading-[1.5] t-body">{body}</p>
              </div>
            ))}
          </div>
        </section>

        {/* ── closing ──────────────────────────────────────────── */}
        <section className="mx-auto w-full max-w-4xl px-6 pb-24 pt-28">
          <div className="border-t bd pt-16 sm:flex sm:items-end sm:justify-between">
            <div>
              <p className="spec-marker">
                <span className="idx">→</span>
                <span className="lbl">Getting started</span>
              </p>
              <h2 className="paper-display mt-5 text-[28px] font-medium leading-[1.15] sm:text-4xl">
                Boot your first sandbox.
              </h2>
              <p className="paper-mono mt-4 text-[13px] t-body">
                three endpoints · one daemon · runs on your machine
              </p>
            </div>
            <div className="mt-8 flex items-center gap-3 sm:mt-0">
              <a href={GITHUB} className="pill-outline">
                View source
              </a>
              <Link href="/docs/getting-started" className="pill-primary">
                Get started
              </Link>
            </div>
          </div>
        </section>
      </main>

      {/* ── footer ─────────────────────────────────────────────── */}
      <footer>
        <div className="mx-auto flex w-full max-w-4xl flex-wrap items-center justify-between gap-4 px-6 py-8">
          <div className="flex items-center gap-2.5">
            <InkLogo className="h-4 w-auto" />
            <span className="text-xs t-body">© 2026 emberd</span>
          </div>
          <nav className="flex flex-wrap items-center gap-5">
            <Link href="/docs" className="paper-footlink">Docs</Link>
            <Link href="/docs/roadmap" className="paper-footlink">Roadmap</Link>
            <Link href="/docs/design-notes" className="paper-footlink">Design notes</Link>
            <a href={GITHUB} className="paper-footlink">GitHub</a>
          </nav>
        </div>
      </footer>
    </>
  );
}

/* ── terminal helpers ───────────────────────────────────────────── */

function Line({ children, mute = false }: { children: ReactNode; mute?: boolean }) {
  return (
    <div className={mute ? 't-mute' : 't-ink'} style={{ whiteSpace: 'pre' }}>
      {children}
    </div>
  );
}

function Dollar() {
  return <span className="t-ember">$</span>;
}

function Comment({ children }: { children: ReactNode }) {
  return <span className="t-mute">{children}</span>;
}

function Gap() {
  return <div aria-hidden style={{ height: '0.9em' }} />;
}

/* ── marks ──────────────────────────────────────────────────────── */

/** The emberd mark re-inked for the white canvas: ink bars over the
 *  ember-gradient base stripe. public/logo.svg keeps the dark-theme fills. */
function InkLogo({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 134 117" fill="none" className={className} aria-hidden>
      <defs>
        <linearGradient id="ember-ink" x1="66.6" y1="83" x2="66.6" y2="117" gradientUnits="userSpaceOnUse">
          <stop offset="0" stopColor="#ff8a4d" />
          <stop offset="1" stopColor="#df461d" />
        </linearGradient>
      </defs>
      <path d="M0 16.9997L66.672 0.333008L133.333 16.9997V33.6663L66.672 50.333L0 33.6663V16.9997Z" fill="currentColor" />
      <path d="M0 50.333L66.672 66.9997L133.333 50.333V66.9997L66.672 83.6663L0 66.9997V50.333Z" fill="currentColor" />
      <path d="M0 83.6663L66.672 100.333L133.333 83.6663V100.333L66.672 117L0 100.333V83.6663Z" fill="url(#ember-ink)" />
    </svg>
  );
}

/** Stroke-only lock, drawn light — the quiet ornament of the privacy section. */
function LockIcon({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="var(--hairline-strong)"
      strokeWidth="1"
      strokeLinecap="round"
      strokeLinejoin="round"
      className={className}
      aria-hidden
    >
      <rect x="4" y="10.5" width="16" height="10.5" rx="2.5" />
      <path d="M8 10.5V7a4 4 0 0 1 8 0v3.5" />
    </svg>
  );
}
