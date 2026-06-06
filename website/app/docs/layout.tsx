import { source } from '@/lib/source';
import { DocsLayout } from 'fumadocs-ui/layouts/docs';
import { Banner } from 'fumadocs-ui/components/banner';
import { baseOptions } from '@/lib/layout.shared';

export default function Layout({ children }: LayoutProps<'/docs'>) {
  return (
    <>
      <Banner
        // bump this id when the message changes to re-show it to past visitors
        id="emberd-pre-alpha-2026-06"
        className="bg-[#241708] text-[0.82rem] font-medium text-[#f2b65a]"
      >
        <span className="text-[#ff7a3f]">●</span>
        <span className="ml-2">
          Pre-alpha — APIs, wire formats, and behavior may change without notice.
          Expect breaking changes; use with caution.
        </span>
      </Banner>
      <DocsLayout tree={source.getPageTree()} {...baseOptions()}>
        {children}
      </DocsLayout>
    </>
  );
}
