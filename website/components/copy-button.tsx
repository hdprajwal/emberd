'use client';

import { useState } from 'react';
import { Check, Copy } from 'lucide-react';

/** Copy-to-clipboard control. Pass `label` for the pill form used by the
 *  landing's install block; omit it for the bare icon. */
export function CopyButton({ text, label }: { text: string; label?: string }) {
  const [copied, setCopied] = useState(false);

  const icon = copied ? (
    <Check className="h-4 w-4" strokeWidth={2} aria-hidden />
  ) : (
    <Copy className="h-4 w-4" strokeWidth={1.75} aria-hidden />
  );

  return (
    <button
      type="button"
      aria-label={`Copy to clipboard: ${text}`}
      data-copied={copied}
      className={
        label
          ? 'copy-pill paper-mono'
          : 't-mute transition-colors hover:text-[var(--ink)]'
      }
      onClick={() => {
        void navigator.clipboard.writeText(text);
        setCopied(true);
        setTimeout(() => setCopied(false), 1600);
      }}
    >
      {icon}
      {label ? <span>{copied ? 'Copied' : label}</span> : null}
    </button>
  );
}
