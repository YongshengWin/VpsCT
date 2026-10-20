import * as React from "react";
import { Link, useLocation, useSearchParams } from "react-router-dom";
import { ArrowLeft, ArrowRight, ChevronDown, ChevronRight, List, Search, X } from "lucide-react";
import { manualDocuments, manualHref, manualTopics, searchManual, type ManualBlock, type ManualDocument } from "@/lib/manual";
import { useAuth } from "@/lib/auth";
import { cn } from "@/lib/utils";
import { Input } from "@/components/ui";

function Block({ block }: { block: ManualBlock }) {
  switch (block.kind) {
    case "text": return <p className="text-sm leading-7 text-muted-foreground">{block.text}</p>;
    case "list": return <ul className="list-disc space-y-2 pl-5 text-sm leading-7 text-muted-foreground">{block.items.map(item => <li key={item}>{item}</li>)}</ul>;
    case "steps": return <ol className="list-decimal space-y-3 pl-5 text-sm leading-7 marker:font-medium">{block.items.map(item => <li key={item} className="pl-1">{item}</li>)}</ol>;
    case "note": return <aside className="border-l-4 border-primary/40 bg-primary/5 px-4 py-3"><p className="text-sm font-semibold">说明 · {block.title}</p><p className="mt-1 text-sm leading-7 text-muted-foreground">{block.text}</p></aside>;
    case "table": return <div className="overflow-x-auto rounded-md border"><table className="w-full min-w-104 text-left text-sm"><thead className="bg-muted/50"><tr>{block.headers.map(header => <th key={header} scope="col" className="px-3 py-2.5 font-medium">{header}</th>)}</tr></thead><tbody>{block.rows.map((row, i) => <tr key={i} className="border-t align-top">{row.map((cell, j) => j === 0 ? <th key={j} scope="row" className="px-3 py-3 font-medium leading-6">{cell}</th> : <td key={j} className="px-3 py-3 leading-6 text-muted-foreground">{cell}</td>)}</tr>)}</tbody></table></div>;
  }
}

function Directory({ current, section, query, onNavigate }: { current: ManualDocument; section: string; query: string; onNavigate: () => void }) {
  const directoryID = React.useId();
  const [openTopics, setOpenTopics] = React.useState([current.topic.id]);
  const [openChapters, setOpenChapters] = React.useState([current.number]);
  React.useEffect(() => {
    setOpenTopics(previous => previous.includes(current.topic.id) ? previous : [...previous, current.topic.id]);
    setOpenChapters(previous => previous.includes(current.number) ? previous : [...previous, current.number]);
  }, [current.topic.id, current.number]);
  const toggle = (set: React.Dispatch<React.SetStateAction<string[]>>, id: string) => set(previous => previous.includes(id) ? previous.filter(value => value !== id) : [...previous, id]);
  const matches = query.trim() ? searchManual(query) : null;
  return <nav aria-label="使用手册目录" className="text-sm">
    {matches ? <>
      <p className="px-2 py-2 text-xs text-muted-foreground" aria-live="polite">找到 {matches.length} 篇文档</p>
      <ol className="space-y-1">{matches.map(doc => <li key={doc.number}><Link to={manualHref(doc.topic.id, doc.chapter.id)} onClick={onNavigate} className="block rounded px-2 py-2 leading-6 hover:bg-muted"><span className="block text-xs text-muted-foreground">{doc.topicNumber} {doc.topic.title}</span>{doc.number} {doc.chapter.title}</Link></li>)}</ol>
      {!matches.length && <p className="px-2 py-4 text-xs leading-6 text-muted-foreground">没有匹配的文档。可搜索“线路”“模板”“配额”或“应用失败”。</p>}
    </> : <ol className="space-y-1">{manualTopics.map((topic, ti) => {
      const expanded = openTopics.includes(topic.id);
      const topicID = `${directoryID}-topic-${topic.id}`;
      return <li key={topic.id}>
        <button type="button" aria-expanded={expanded} aria-controls={topicID} onClick={() => toggle(setOpenTopics, topic.id)} className="flex w-full items-center gap-1.5 rounded px-2 py-2 text-left font-semibold leading-6 hover:bg-muted focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-primary/40">
          {expanded ? <ChevronDown className="h-3.5 w-3.5 shrink-0" aria-hidden="true" /> : <ChevronRight className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />}{ti + 1} {topic.title}
        </button>
        <ol id={topicID} hidden={!expanded} className="ml-3 border-l border-border/80 pl-1">{manualDocuments.filter(doc => doc.topic.id === topic.id).map(doc => {
          const selected = current.number === doc.number;
          const expandedChapter = openChapters.includes(doc.number);
          const chapterID = `${directoryID}-chapter-${doc.topic.id}-${doc.chapter.id}`;
          return <li key={doc.number}>
            <div className={cn("flex items-start rounded", selected && "bg-primary/5")}>
              <button type="button" aria-label={`${expandedChapter ? "折叠" : "展开"} ${doc.number} ${doc.chapter.title}`} aria-expanded={expandedChapter} aria-controls={chapterID} onClick={() => toggle(setOpenChapters, doc.number)} className="mt-1 shrink-0 rounded p-1.5 text-muted-foreground hover:bg-muted focus-visible:ring-2 focus-visible:ring-primary/40">{expandedChapter ? <ChevronDown className="h-3 w-3" /> : <ChevronRight className="h-3 w-3" />}</button>
              <Link to={manualHref(topic.id, doc.chapter.id)} onClick={onNavigate} aria-current={selected ? "page" : undefined} className={cn("min-w-0 flex-1 py-2 pr-2 text-xs leading-5 hover:text-primary", selected ? "font-semibold text-primary" : "text-muted-foreground")}>{doc.number} {doc.chapter.title}</Link>
            </div>
            <ol id={chapterID} hidden={!expandedChapter} className="ml-3 border-l border-border/70 pl-2">{doc.sections.map(item => <li key={item.id}><Link to={manualHref(topic.id, doc.chapter.id, item.id)} onClick={onNavigate} aria-current={selected && section === item.anchor ? "location" : undefined} className={cn("block rounded px-2 py-1.5 text-xs leading-5 transition-colors", selected && section === item.anchor ? "font-semibold text-primary" : "text-muted-foreground hover:text-primary")}>{item.number} {item.title}</Link></li>)}</ol>
          </li>;
        })}</ol>
      </li>;
    })}</ol>}
  </nav>;
}

export function ManualPage() {
  const [params] = useSearchParams();
  const location = useLocation();
  const { user } = useAuth();
  const topic = manualTopics.find(item => item.id === params.get("topic")) ?? manualTopics[0];
  const current = manualDocuments.find(doc => doc.topic.id === topic.id && doc.chapter.id === params.get("chapter")) ?? manualDocuments.find(doc => doc.topic.id === topic.id)!;
  const index = manualDocuments.indexOf(current), previous = manualDocuments[index - 1], next = manualDocuments[index + 1];
  const [query, setQuery] = React.useState("");
  const [mobileOpen, setMobileOpen] = React.useState(false);
  const [activeSection, setActiveSection] = React.useState(current.sections[0]?.anchor ?? "");
  const title = React.useRef<HTMLHeadingElement>(null);
  const content = React.useRef<HTMLDivElement>(null);
  const firstRender = React.useRef(true);
  const navigationSection = React.useRef<string | null>(null);
  const scrollIntent = React.useRef(false);
  const sectionRefs = React.useRef(new Map<string, HTMLHeadingElement>());
  const onNavigate = () => { setQuery(""); setMobileOpen(false); };
  const links = current.chapter.links?.filter(link => !link.admin || user?.role === "admin") ?? [];

  // Chapter and section URLs remain stable across refresh and browser history.
  // Scrolling changes the highlighted section, without creating history entries.
  React.useEffect(() => {
    if (firstRender.current && !location.hash) { firstRender.current = false; return; }
    firstRender.current = false;
    const anchor = current.sections.find(item => `#${item.anchor}` === location.hash)?.anchor;
    navigationSection.current = anchor ?? null;
    scrollIntent.current = false;
    setActiveSection(anchor ?? current.sections[0]?.anchor ?? "");
    const frame = requestAnimationFrame(() => {
      const target = anchor ? sectionRefs.current.get(anchor) : title.current;
      target?.focus({ preventScroll: true });
      const pane = content.current;
      if (pane && window.matchMedia("(min-width: 1024px)").matches) {
        const top = anchor && target ? pane.scrollTop + target.getBoundingClientRect().top - pane.getBoundingClientRect().top - 24 : 0;
        pane.scrollTo({ top: Math.max(0, top), behavior: "instant" });
      } else {
        target?.scrollIntoView({ block: "start", behavior: "instant" });
      }
    });
    return () => cancelAnimationFrame(frame);
  }, [current.number, location.hash, location.key]);
  React.useEffect(() => {
    let frame = 0;
    const update = () => {
      frame = 0;
      if (navigationSection.current) return;
      let anchor = current.sections[0]?.anchor ?? "";
      const threshold = content.current && window.matchMedia("(min-width: 1024px)").matches ? content.current.getBoundingClientRect().top + 40 : 132;
      for (const item of current.sections) {
        const heading = sectionRefs.current.get(item.anchor);
        if (heading && heading.getBoundingClientRect().top <= threshold) anchor = item.anchor;
      }
      setActiveSection(anchor);
    };
    const onScroll = () => {
      if (scrollIntent.current) navigationSection.current = null;
      if (!frame) frame = requestAnimationFrame(update);
    };
    const onIntent = () => { scrollIntent.current = true; };
    const onKey = (event: KeyboardEvent) => {
      if (["ArrowUp", "ArrowDown", "PageUp", "PageDown", "Home", "End", " "].includes(event.key)) onIntent();
    };
    update();
    const pane = content.current;
    window.addEventListener("scroll", onScroll, { passive: true });
    window.addEventListener("resize", onScroll);
    pane?.addEventListener("scroll", onScroll, { passive: true });
    pane?.addEventListener("wheel", onIntent, { passive: true });
    pane?.addEventListener("touchmove", onIntent, { passive: true });
    pane?.addEventListener("pointerdown", onIntent);
    pane?.addEventListener("keydown", onKey);
    return () => {
      window.removeEventListener("scroll", onScroll);
      window.removeEventListener("resize", onScroll);
      pane?.removeEventListener("scroll", onScroll);
      pane?.removeEventListener("wheel", onIntent);
      pane?.removeEventListener("touchmove", onIntent);
      pane?.removeEventListener("pointerdown", onIntent);
      pane?.removeEventListener("keydown", onKey);
      if (frame) cancelAnimationFrame(frame);
    };
  }, [current]);

  const search = <div className="relative"><Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" aria-hidden="true" /><Input aria-label="搜索文档目录与正文" placeholder="搜索文档…" value={query} onChange={event => setQuery(event.target.value)} className="h-9 rounded-md pl-8 pr-8 text-xs" />{query && <button type="button" aria-label="清除文档搜索" onClick={() => setQuery("")} className="absolute right-1 top-1 rounded p-1.5 text-muted-foreground hover:bg-muted"><X className="h-4 w-4" /></button>}</div>;
  return <div className="lg:flex lg:min-h-0 lg:flex-1 lg:flex-col">
    <div className="mb-3 flex justify-end lg:hidden">
      <button type="button" aria-expanded={mobileOpen} aria-controls="mobile-document-directory" onClick={() => setMobileOpen(value => !value)} className="inline-flex items-center gap-2 rounded-md border px-3 py-2 text-sm lg:hidden"><List className="h-4 w-4" />文档目录</button>
    </div>
    <div className="grid min-h-[calc(100dvh-7rem)] items-start border-t bg-card lg:min-h-0 lg:flex-1 lg:overflow-hidden lg:grid-cols-[216px_minmax(0,1fr)_144px] xl:grid-cols-[232px_minmax(0,1fr)_160px]">
      <aside className="hidden min-w-0 border-r px-3 py-5 lg:block lg:h-full lg:overflow-y-auto lg:overscroll-contain">
        <p className="mb-3 px-2 text-sm font-semibold">文档目录</p>{search}<div className="mt-4"><Directory current={current} section={activeSection} query={query} onNavigate={onNavigate} /></div>
      </aside>
      {mobileOpen && <aside id="mobile-document-directory" className="min-w-0 border-b p-4 lg:hidden">{search}<div className="mt-3 max-h-[60dvh] overflow-y-auto"><Directory current={current} section={activeSection} query={query} onNavigate={onNavigate} /></div></aside>}
      <div ref={content} aria-label="文档正文" tabIndex={0} className="min-w-0 px-4 py-6 outline-hidden sm:px-7 lg:h-full lg:overflow-y-auto lg:overscroll-contain">
        <article>
          <nav aria-label="文档位置" className="mb-5 flex flex-wrap items-center gap-1.5 text-xs leading-5 text-muted-foreground"><Link to="/manual" onClick={onNavigate} className="hover:text-primary">产品文档</Link><ChevronRight className="h-3 w-3" aria-hidden="true" /><span>{current.topicNumber} {current.topic.title}</span><ChevronRight className="h-3 w-3" aria-hidden="true" /><span>{current.number} {current.chapter.title}</span></nav>
          <h1 ref={title} tabIndex={-1} className="scroll-mt-24 text-2xl font-bold leading-10 outline-hidden">{current.number} {current.chapter.title}</h1>
          <p className="mt-4 text-sm leading-7 text-muted-foreground">{current.chapter.summary}</p>
          <details className="mt-6 border-y py-3 text-sm lg:hidden"><summary className="cursor-pointer font-medium">本文目录</summary><nav aria-label="本文目录" className="mt-3 space-y-2">{current.sections.map(item => <Link key={item.id} to={manualHref(topic.id, current.chapter.id, item.id)} onClick={onNavigate} aria-current={activeSection === item.anchor ? "location" : undefined} className={cn("block rounded px-2 py-1.5 text-xs leading-5", activeSection === item.anchor ? "font-semibold text-primary" : "text-muted-foreground hover:text-primary")}>{item.number} {item.title}</Link>)}</nav></details>
          <div className="mt-8 space-y-10">{current.sections.map(item => <section key={item.anchor} aria-labelledby={item.anchor}>
            <h2 id={item.anchor} ref={element => { if (element) sectionRefs.current.set(item.anchor, element); else sectionRefs.current.delete(item.anchor); }} tabIndex={-1} className="mb-4 scroll-mt-24 text-lg font-semibold leading-8 outline-hidden">{item.number} {item.title}</h2>
            <div className="space-y-4">{item.blocks.map((block, i) => <Block key={i} block={block} />)}</div>
          </section>)}</div>
          {links.length > 0 && <section className="mt-10 border-t pt-5"><h2 className="mb-3 text-sm font-semibold">相关操作</h2><div className="flex flex-wrap gap-x-5 gap-y-3">{links.map(link => <Link key={link.to} to={link.to} className="inline-flex items-center gap-1 text-sm text-primary hover:underline">{link.label}<ArrowRight className="h-3.5 w-3.5" aria-hidden="true" /></Link>)}</div></section>}
        </article>
        <nav aria-label="相邻文档" className="mt-10 grid grid-cols-2 gap-4 border-t pt-5">{previous ? <Link to={manualHref(previous.topic.id, previous.chapter.id)} onClick={onNavigate} className="group text-sm"><span className="flex items-center gap-1 text-xs text-muted-foreground"><ArrowLeft className="h-3 w-3" />上一篇</span><span className="mt-2 block leading-6 group-hover:text-primary">{previous.number} {previous.chapter.title}</span></Link> : <span />}{next && <Link to={manualHref(next.topic.id, next.chapter.id)} onClick={onNavigate} className="group text-right text-sm"><span className="flex items-center justify-end gap-1 text-xs text-muted-foreground">下一篇<ArrowRight className="h-3 w-3" /></span><span className="mt-2 block leading-6 group-hover:text-primary">{next.number} {next.chapter.title}</span></Link>}</nav>
      </div>
      <aside className="hidden overflow-y-auto py-6 pr-3 lg:block lg:h-full lg:overscroll-contain"><nav aria-label="本文目录" className="border-l pl-3"><p className="mb-3 text-xs font-semibold">本文目录</p><ol className="space-y-3">{current.sections.map(item => <li key={item.id}><Link to={manualHref(topic.id, current.chapter.id, item.id)} onClick={onNavigate} aria-current={activeSection === item.anchor ? "location" : undefined} className={cn("block rounded px-2 py-1.5 text-xs leading-5 transition-colors", activeSection === item.anchor ? "font-semibold text-primary" : "text-muted-foreground hover:text-primary")}>{item.number} {item.title}</Link></li>)}</ol></nav></aside>
    </div>
  </div>;
}
