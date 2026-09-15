import * as React from "react";
import { MoreHorizontal } from "lucide-react";
import { Button } from "@/components/ui";

interface Action {
  label: string;
  icon: React.ReactNode;
  onClick: () => void;
  disabled?: boolean;
  destructive?: boolean;
}

export function ServerActions({ items }: { items: Action[] }) {
  const [open, setOpen] = React.useState(false);
  const root = React.useRef<HTMLDivElement>(null);
  const trigger = React.useRef<HTMLButtonElement>(null);
  const menu = React.useRef<HTMLDivElement>(null);
  const id = React.useId();
  const close = () => { setOpen(false); trigger.current?.focus(); };

  React.useEffect(() => {
    if (!open) return;
    menu.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
    const outside = (e: PointerEvent) => { if (!root.current?.contains(e.target as Node)) setOpen(false); };
    document.addEventListener("pointerdown", outside);
    return () => document.removeEventListener("pointerdown", outside);
  }, [open]);

  function navigate(e: React.KeyboardEvent) {
    if (e.key === "Escape") { e.preventDefault(); close(); return; }
    if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(e.key)) return;
    e.preventDefault();
    const buttons = [...(menu.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") ?? [])];
    const current = buttons.indexOf(document.activeElement as HTMLButtonElement);
    const next = e.key === "Home" ? 0 : e.key === "End" ? buttons.length - 1 : (current + (e.key === "ArrowDown" ? 1 : -1) + buttons.length) % buttons.length;
    buttons[next]?.focus();
  }

  return <div ref={root} className="relative" onBlur={(e) => { if (!e.currentTarget.contains(e.relatedTarget)) setOpen(false); }}>
    <Button ref={trigger} size="sm" variant="outline" aria-haspopup="menu" aria-expanded={open} aria-controls={open ? id : undefined} onClick={() => setOpen(!open)} onKeyDown={(e) => { if (e.key === "ArrowDown") { e.preventDefault(); setOpen(true); } }}>
      <MoreHorizontal className="h-4 w-4" /> 更多
    </Button>
    {open && <div ref={menu} id={id} role="menu" aria-label="服务器更多操作" onKeyDown={navigate} className="absolute right-0 top-full z-30 mt-2 w-56 rounded-2xl border border-border/60 bg-card p-1.5 shadow-lift">
      {items.map((item) => <button key={item.label} type="button" role="menuitem" disabled={item.disabled} className={`flex w-full items-center gap-2.5 rounded-xl px-3 py-2.5 text-left text-sm transition-colors hover:bg-accent focus-visible:bg-accent focus-visible:outline-none disabled:opacity-50 ${item.destructive ? "text-destructive" : "text-foreground"}`} onClick={() => { close(); item.onClick(); }}>
        {item.icon}{item.label}
      </button>)}
    </div>}
  </div>;
}
