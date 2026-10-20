import * as React from "react";
import { createPortal } from "react-dom";
import { Link } from "react-router-dom";
import { X, Loader2, ChevronDown, Check, ChevronLeft } from "lucide-react";
import { cn } from "@/lib/utils";
import type { SelectQuickFilter } from "@/lib/node-select-filters";

// ---------- Button ----------
type Variant = "default" | "secondary" | "outline" | "ghost" | "destructive" | "link";
type Size = "default" | "sm" | "lg" | "icon";

const variants: Record<Variant, string> = {
  default: "bg-primary text-primary-foreground shadow-[0_6px_16px_-6px_hsl(var(--primary)/0.6)] hover:bg-primary/90 active:translate-y-px",
  secondary: "bg-secondary text-secondary-foreground hover:bg-accent",
  outline: "border border-border bg-card text-foreground hover:bg-accent",
  ghost: "text-foreground hover:bg-accent",
  destructive: "bg-destructive text-destructive-foreground hover:bg-destructive/90",
  link: "text-primary underline-offset-4 hover:underline",
};
const sizes: Record<Size, string> = {
  default: "h-10 px-5",
  sm: "h-8 px-3.5 text-xs",
  lg: "h-11 px-7 text-base",
  icon: "h-10 w-10",
};

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  loading?: boolean;
}

export const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant = "default", size = "default", loading, children, disabled, ...props }, ref) => (
    <button
      ref={ref}
      disabled={disabled || loading}
      className={cn(
        "inline-flex items-center justify-center gap-1.5 whitespace-nowrap rounded-full text-sm font-semibold transition-all focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-50",
        variants[variant],
        sizes[size],
        className,
      )}
      {...props}
    >
      {loading && <Loader2 className="h-4 w-4 animate-spin" />}
      {children}
    </button>
  ),
);
Button.displayName = "Button";

// ---------- Inputs ----------
export const Input = React.forwardRef<HTMLInputElement, React.InputHTMLAttributes<HTMLInputElement>>(({ className, ...props }, ref) => (
  <input
    ref={ref}
    className={cn(
      "flex h-10 w-full min-w-0 rounded-xl border border-transparent bg-muted px-3.5 py-1 text-sm transition-colors placeholder:text-muted-foreground/70 hover:bg-accent focus:border-primary/40 focus:bg-card focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-primary/25 disabled:cursor-not-allowed disabled:opacity-50",
      className,
    )}
    {...props}
  />
));
Input.displayName = "Input";

export const Textarea = React.forwardRef<HTMLTextAreaElement, React.TextareaHTMLAttributes<HTMLTextAreaElement>>(({ className, ...props }, ref) => (
  <textarea
    ref={ref}
    className={cn(
      "flex min-h-[80px] w-full rounded-xl border border-transparent bg-muted px-3.5 py-2.5 text-sm transition-colors placeholder:text-muted-foreground/70 hover:bg-accent focus:border-primary/40 focus:bg-card focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-primary/25 disabled:opacity-50",
      className,
    )}
    {...props}
  />
));
Textarea.displayName = "Textarea";

// ---------- Select ----------
// A styled replacement for the native <select>: the OS-drawn popup cannot be
// themed, so we render our own listbox in a portal. The API mirrors the native
// element (`value`, `onChange(e.target.value)`, `<option>` children, `disabled`)
// so existing call sites work unchanged.

interface SelectOption {
  value: string;
  label: React.ReactNode;
  text: string;
  disabled?: boolean;
}

function nodeText(n: React.ReactNode): string {
  if (n == null || typeof n === "boolean") return "";
  if (typeof n === "string" || typeof n === "number") return String(n);
  if (Array.isArray(n)) return n.map(nodeText).join("");
  if (React.isValidElement<{ children?: React.ReactNode }>(n)) return nodeText(n.props.children);
  return "";
}

function collectOptions(children: React.ReactNode): SelectOption[] {
  const out: SelectOption[] = [];
  React.Children.forEach(children, (child) => {
    if (!React.isValidElement(child)) return;
    if (child.type === "option") {
      const p = child.props as React.OptionHTMLAttributes<HTMLOptionElement>;
      const text = nodeText(p.children);
      out.push({ value: p.value !== undefined ? String(p.value) : text, label: p.children, text, disabled: p.disabled });
    } else {
      // <optgroup>, fragments, arrays
      out.push(...collectOptions((child.props as { children?: React.ReactNode }).children));
    }
  });
  return out;
}

interface PopPos {
  left: number;
  width: number;
  top?: number;
  bottom?: number;
  maxHeight: number;
}

export type SelectProps = Omit<React.SelectHTMLAttributes<HTMLSelectElement>, "size"> & {
  searchable?: boolean;
  searchPlaceholder?: string;
  quickFilters?: SelectQuickFilter[];
};

export function Select({ className, children, value, defaultValue, onChange, disabled, name, id, title, style, autoFocus, searchable = false, searchPlaceholder = "搜索选项…", quickFilters, "aria-label": ariaLabel }: SelectProps) {
  const allOptions = React.useMemo(() => collectOptions(children), [children]);
  const [search, setSearch] = React.useState("");
  const [filters, setFilters] = React.useState<Record<string, string>>({});
  const hasFilters = Object.values(filters).some(Boolean);
  const terms = search.trim().toLocaleLowerCase().split(/\s+/).filter(Boolean);
  const activeGroups = quickFilters?.filter(g => filters[g.key]).map(g => new Set(g.options.find(c => c.value === filters[g.key])?.values ?? [])) ?? [];
  const options = allOptions.filter(o => (!searchable || terms.every(term => o.text.toLocaleLowerCase().includes(term))) && activeGroups.every(values => values.has(o.value)));
  const nodeValues = new Set(quickFilters?.flatMap(g => g.options.flatMap(c => c.values)));
  const resultCount = options.filter(o => nodeValues.has(o.value)).length;
  const controlled = value !== undefined;
  const [inner, setInner] = React.useState(() => String(defaultValue ?? allOptions[0]?.value ?? ""));
  const current = controlled ? String(value) : inner;
  // like the native element, an unmatched value displays the first option
  const selected = allOptions.find((o) => o.value === current) ?? allOptions[0];

  const [open, setOpen] = React.useState(false);
  const [hl, setHl] = React.useState(-1);
  const [pos, setPos] = React.useState<PopPos | null>(null);
  const btnRef = React.useRef<HTMLButtonElement>(null);
  const listRef = React.useRef<HTMLDivElement>(null);
  const searchRef = React.useRef<HTMLInputElement>(null);
  const typeahead = React.useRef({ s: "", at: 0 });
  const listId = React.useId();

  const place = React.useCallback(() => {
    const el = btnRef.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    const gap = 6;
    const wanted = Math.min(allOptions.length * 38 + 12 + (searchable ? 52 : 0) + (quickFilters?.length ? quickFilters.length * 38 + 32 : 0), quickFilters?.length ? 480 : 320);
    const below = window.innerHeight - r.bottom - gap - 8;
    const above = r.top - gap - 8;
    const up = below < wanted && above > below;
    const maxHeight = Math.max(120, Math.min(wanted, up ? above : below));
    setPos({
      left: Math.max(8, Math.min(r.left, window.innerWidth - Math.max(r.width, 176) - 8)),
      width: Math.max(r.width, 176),
      ...(up ? { bottom: window.innerHeight - r.top + gap } : { top: r.bottom + gap }),
      maxHeight,
    });
  }, [allOptions.length, searchable, quickFilters?.length]);

  const show = () => {
    if (disabled) return;
    setSearch("");
    setFilters({});
    place();
    const i = allOptions.findIndex((o) => o.value === selected?.value && !o.disabled);
    setHl(i >= 0 ? i : allOptions.findIndex((o) => !o.disabled));
    setOpen(true);
  };
  const hide = () => setOpen(false);

  React.useEffect(() => {
    if (open && searchable) searchRef.current?.focus();
  }, [open, searchable]);
  React.useEffect(() => {
    if (searchable) setHl(options.findIndex(o => !o.disabled));
  }, [search, searchable, allOptions, filters]);

  const commit = (v: string) => {
    if (!controlled) setInner(v);
    if (v !== current && onChange) {
      const target = { value: v, name: name ?? "" } as unknown as HTMLSelectElement;
      onChange({ target, currentTarget: target } as unknown as React.ChangeEvent<HTMLSelectElement>);
    }
    hide();
    btnRef.current?.focus();
  };

  // close on outside click / scroll / resize while open
  React.useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node;
      if (!btnRef.current?.contains(t) && !listRef.current?.contains(t)) hide();
    };
    const onScroll = (e: Event) => {
      if (listRef.current && e.target instanceof Node && listRef.current.contains(e.target)) return;
      hide();
    };
    document.addEventListener("mousedown", onDown);
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", hide);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", hide);
    };
  }, [open]);

  // the list may grow wider than the trigger to fit long labels; keep it on screen
  React.useLayoutEffect(() => {
    if (!open || !listRef.current) return;
    const r = listRef.current.getBoundingClientRect();
    const overflow = r.right - (window.innerWidth - 8);
    if (overflow > 0) setPos((p) => (p ? { ...p, left: Math.max(8, p.left - overflow) } : p));
  }, [open, pos?.width]);

  // keep the highlighted row visible
  React.useEffect(() => {
    if (!open || hl < 0) return;
    listRef.current?.querySelector<HTMLElement>(`[data-idx="${hl}"]`)?.scrollIntoView({ block: "nearest" });
  }, [open, hl]);

  const move = (dir: 1 | -1) => {
    if (!options.length) return;
    let i = hl;
    for (let n = 0; n < options.length; n++) {
      i = (i + dir + options.length) % options.length;
      if (!options[i].disabled) break;
    }
    setHl(i);
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLElement>) => {
    if (disabled) return;
    if (!open) {
      if (["ArrowDown", "ArrowUp", "Enter", " "].includes(e.key)) {
        e.preventDefault();
        show();
      }
      return;
    }
    if (e.nativeEvent.isComposing) return;
    const inSearch = e.target === searchRef.current;
    if (inSearch && !["ArrowDown", "ArrowUp", "Enter", "Escape", "Tab"].includes(e.key)) return;
    switch (e.key) {
      case "ArrowDown":
        e.preventDefault();
        move(1);
        break;
      case "ArrowUp":
        e.preventDefault();
        move(-1);
        break;
      case "Home":
        e.preventDefault();
        setHl(options.findIndex((o) => !o.disabled));
        break;
      case "End":
        e.preventDefault();
        setHl(options.length - 1 - [...options].reverse().findIndex((o) => !o.disabled));
        break;
      case "Enter":
      case " ":
        e.preventDefault();
        if (hl >= 0 && options[hl] && !options[hl].disabled) commit(options[hl].value);
        break;
      case "Escape":
        e.preventDefault();
        e.stopPropagation();
        hide();
        btnRef.current?.focus();
        break;
      case "Tab":
        hide();
        break;
      default: {
        if (e.key.length !== 1 || e.metaKey || e.ctrlKey || e.altKey) return;
        const now = Date.now();
        const t = typeahead.current;
        t.s = now - t.at < 600 ? t.s + e.key : e.key;
        t.at = now;
        const q = t.s.toLowerCase();
        const start = t.s.length === 1 ? hl + 1 : hl;
        for (let n = 0; n < options.length; n++) {
          const i = (start + n) % options.length;
          if (!options[i].disabled && options[i].text.toLowerCase().startsWith(q)) {
            setHl(i);
            break;
          }
        }
      }
    }
  };

  return (
    <>
      <button
        ref={btnRef}
        type="button"
        role="combobox"
        id={id}
        title={title}
        style={style}
        autoFocus={autoFocus}
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        disabled={disabled}
        data-state={open ? "open" : "closed"}
        onClick={() => (open ? hide() : show())}
        onKeyDown={onKeyDown}
        className={cn(
          "flex h-10 w-full min-w-0 items-center gap-2 rounded-xl border border-transparent bg-muted px-3.5 py-2 text-left text-sm transition-colors hover:bg-accent focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-primary/25 data-[state=open]:border-primary/40 data-[state=open]:bg-card disabled:cursor-not-allowed disabled:opacity-50",
          className,
        )}
      >
        <span className={cn("min-w-0 flex-1 truncate", !selected && "text-muted-foreground/70")}>{selected?.label ?? ""}</span>
        <ChevronDown className={cn("h-4 w-4 shrink-0 text-muted-foreground transition-transform", open && "rotate-180")} />
      </button>
      {name && <input type="hidden" name={name} value={current} />}
      {open &&
        pos &&
        createPortal(
          <div
            ref={listRef}
            onKeyDown={e => { if (e.key === "Escape" && e.target !== searchRef.current) { e.preventDefault(); e.stopPropagation(); hide(); btnRef.current?.focus(); } }}
            data-floating=""
            className="fixed z-60 flex flex-col animate-fade-up overflow-hidden rounded-2xl border border-border/60 bg-card p-1.5 shadow-lift dark:border-border"
            style={{ left: pos.left, minWidth: pos.width, maxWidth: Math.min(480, window.innerWidth - 16), top: pos.top, bottom: pos.bottom, maxHeight: pos.maxHeight }}
          >
            {searchable && <div className="shrink-0 border-b border-border/60 p-1 pb-2 mb-1">
              <Input ref={searchRef} role="combobox" aria-label={`搜索${ariaLabel ?? "选项"}`} aria-expanded={open} aria-controls={listId} aria-autocomplete="list" aria-activedescendant={options[hl] ? `${listId}-${hl}` : undefined} placeholder={searchPlaceholder} value={search} onChange={e => setSearch(e.target.value)} onKeyDown={onKeyDown} autoComplete="off" />
            </div>}
            {!!quickFilters?.length && <div className="mb-1 shrink-0 border-b border-border/60 px-1 pb-2">
              <div className="max-h-36 overflow-y-auto space-y-1">
                {quickFilters.map(g => <div key={g.key} role="group" aria-label={`${g.label}快捷筛选`} className="flex min-w-0 items-center gap-2">
                  <span className="w-12 shrink-0 text-xs text-muted-foreground">{g.label}</span>
                  <div className="flex min-w-0 flex-1 gap-1 overflow-x-auto py-1">
                    {[{ value: "", label: "全部" }, ...g.options].map(c => <button key={c.value} type="button" aria-pressed={(filters[g.key] ?? "") === c.value} onClick={() => setFilters(f => ({ ...f, [g.key]: f[g.key] === c.value ? "" : c.value }))} className={cn("shrink-0 rounded-full border px-2.5 py-1 text-xs transition-colors focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-primary/25", (filters[g.key] ?? "") === c.value ? "border-primary/30 bg-primary/10 text-primary font-semibold" : "border-border text-muted-foreground hover:bg-accent")}>
                      {c.label}
                    </button>)}
                  </div>
                </div>)}
              </div>
              <div className="mt-1 flex items-center justify-between text-xs text-muted-foreground"><span role="status">{resultCount} 个节点</span><button type="button" disabled={!hasFilters && !search} onClick={() => { setFilters({}); setSearch(""); searchRef.current?.focus(); }} className="text-primary disabled:opacity-40 hover:underline">清除筛选</button></div>
            </div>}
            <div id={listId} role="listbox" aria-label={ariaLabel} className="min-h-0 overflow-auto" aria-activedescendant={options[hl] ? `${listId}-${hl}` : undefined}>
            {options.length === 0 && <div role="status" className="px-3 py-4 text-sm text-muted-foreground">{search || hasFilters ? "没有匹配的选项，请调整筛选条件" : "无选项"}</div>}
            {options.map((o, i) => {
              const isSel = o.value === selected?.value;
              return (
                <div
                  key={`${o.value}-${i}`}
                  id={`${listId}-${i}`}
                  role="option"
                  aria-selected={isSel}
                  aria-disabled={o.disabled || undefined}
                  data-idx={i}
                  onMouseEnter={() => !o.disabled && setHl(i)}
                  onMouseDown={(e) => e.preventDefault()}
                  onClick={() => !o.disabled && commit(o.value)}
                  className={cn(
                    "flex cursor-pointer items-center gap-3 whitespace-nowrap rounded-xl px-3 py-2 text-sm transition-colors",
                    i === hl && "bg-accent",
                    isSel && "font-semibold text-primary",
                    o.disabled && "cursor-not-allowed opacity-50",
                  )}
                >
                  <span className="flex-1">{o.label}</span>
                  {isSel && <Check className="h-4 w-4 shrink-0" />}
                </div>
              );
            })}
            </div>
          </div>,
          document.body,
        )}
    </>
  );
}

export function Switch({ checked, onChange, disabled, label, size = "md", "aria-label": ariaLabel }: { checked: boolean; onChange: (v: boolean) => void; disabled?: boolean; label?: string; size?: "sm" | "md"; "aria-label"?: string }) {
  const sm = size === "sm";
  return (
    <label className={cn("inline-flex cursor-pointer items-center gap-2 select-none", disabled && "opacity-50")}>
      <button
        type="button"
        role="switch"
        aria-label={ariaLabel}
        aria-checked={checked}
        disabled={disabled}
        onClick={() => onChange(!checked)}
        className={cn(
          "relative inline-flex shrink-0 items-center rounded-full transition-colors focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background",
          sm ? "h-4 w-7" : "h-6 w-11",
          checked ? "bg-primary" : "bg-muted-foreground/30",
        )}
      >
        <span className={cn("inline-block rounded-full bg-white shadow-xs transition-transform", sm ? "h-3 w-3" : "h-5 w-5", checked ? (sm ? "translate-x-3" : "translate-x-5") : "translate-x-0.5")} />
      </button>
      {label && <span className="text-sm">{label}</span>}
    </label>
  );
}

export function Label({ className, children, hint, ...props }: React.LabelHTMLAttributes<HTMLLabelElement> & { hint?: string }) {
  return (
    <label className={cn("mb-1.5 block text-sm font-medium leading-snug", className)} {...props}>
      {children}
      {hint && <span className="ml-2 text-xs font-normal text-muted-foreground">{hint}</span>}
    </label>
  );
}

export function Field({ label, hint, children, className }: { label: string; hint?: string; children: React.ReactNode; className?: string }) {
  // The hint is helper text under the control (not inline in the label), so labels
  // stay one line, grid rows align, and long hints wrap without being cut.
  return (
    <div className={className}>
      <Label>{label}</Label>
      {children}
      {hint && <p className="mt-1.5 text-xs leading-snug text-muted-foreground">{hint}</p>}
    </div>
  );
}

// ---------- Card ----------
export function Card({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  // min-w-0: a card is often a grid/flex item holding `truncate` text, which
  // otherwise inflates the item's min-width:auto and overflows on phones.
  return <div className={cn("min-w-0 rounded-2xl border border-border/60 bg-card text-card-foreground shadow-card dark:border-border", className)} {...props} />;
}
export function CardHeader({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("flex flex-col space-y-1 p-5 sm:p-6", className)} {...props} />;
}
export function CardTitle({ className, ...props }: React.HTMLAttributes<HTMLHeadingElement>) {
  return <h3 className={cn("text-base font-bold leading-none tracking-tight", className)} {...props} />;
}
export function CardDescription({ className, ...props }: React.HTMLAttributes<HTMLParagraphElement>) {
  return <p className={cn("text-sm text-muted-foreground", className)} {...props} />;
}
export function CardContent({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return <div className={cn("p-5 pt-0 sm:p-6 sm:pt-0", className)} {...props} />;
}

/** Pastel feature surface (hero blocks, callouts) in the spirit of the reference design. */
export type Tint = "peach" | "lavender" | "rose" | "mint" | "sky" | "sand";
const tintBg: Record<Tint, string> = {
  peach: "bg-tint-peach",
  lavender: "bg-tint-lavender",
  rose: "bg-tint-rose",
  mint: "bg-tint-mint",
  sky: "bg-tint-sky",
  sand: "bg-tint-sand",
};
export function TintCard({ tint = "peach", className, ...props }: React.HTMLAttributes<HTMLDivElement> & { tint?: Tint }) {
  return <div className={cn("relative overflow-hidden rounded-2xl text-foreground", tintBg[tint], className)} {...props} />;
}

// ---------- Badge ----------
type BadgeVariant = "default" | "secondary" | "outline" | "success" | "warning" | "destructive" | "info";
const badgeVariants: Record<BadgeVariant, string> = {
  default: "bg-primary/10 text-primary",
  secondary: "bg-secondary text-secondary-foreground",
  outline: "border border-border bg-card text-foreground",
  success: "bg-emerald-500/12 text-emerald-700 dark:bg-emerald-400/15 dark:text-emerald-300",
  warning: "bg-amber-500/15 text-amber-700 dark:bg-amber-400/15 dark:text-amber-300",
  destructive: "bg-rose-500/12 text-rose-700 dark:bg-rose-400/15 dark:text-rose-300",
  info: "bg-sky-500/12 text-sky-700 dark:bg-sky-400/15 dark:text-sky-300",
};
export function Badge({ className, variant = "default", ...props }: React.HTMLAttributes<HTMLSpanElement> & { variant?: BadgeVariant }) {
  return <span className={cn("inline-flex items-center rounded-full px-2.5 py-0.5 text-2xs font-semibold leading-4 whitespace-nowrap", badgeVariants[variant], className)} {...props} />;
}

// ---------- Dialog ----------
// Width should follow the content: a single form column reads best at "sm",
// a long single column (textareas, lists) at "md", and only genuinely
// two-column content (code side by side, editors) needs "lg". `wide` is the
// legacy alias for "lg".
export type DialogSize = "sm" | "md" | "lg" | "xl";
const dialogWidths: Record<DialogSize, string> = { sm: "sm:max-w-lg", md: "sm:max-w-2xl", lg: "sm:max-w-4xl", xl: "sm:max-w-6xl" };

export function Dialog({
  open,
  onClose,
  title,
  description,
  children,
  footer,
  wide,
  size,
  fill,
}: {
  open: boolean;
  onClose: () => void;
  title: string;
  description?: string;
  children: React.ReactNode;
  footer?: React.ReactNode;
  wide?: boolean;
  size?: DialogSize;
  /** 内容自己滚动（左右分栏），弹窗撑满高度、外层不再滚。 */
  fill?: boolean;
}) {
  React.useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onClose();
    document.addEventListener("keydown", onKey);
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = "";
    };
  }, [open, onClose]);
  if (!open) return null;
  return (
    <div className="fixed inset-0 z-50 flex items-end justify-center sm:items-center sm:p-4">
      <div className="absolute inset-0 bg-slate-900/40 backdrop-blur-xs" onClick={onClose} />
      <div
        role="dialog"
        aria-modal="true"
        className={cn(
          "relative flex min-h-0 w-full animate-fade-up flex-col rounded-t-3xl bg-card shadow-pop sm:rounded-3xl",
          fill ? "h-[92vh] max-h-[92vh]" : "max-h-[92vh]",
          dialogWidths[size ?? (wide ? "lg" : "sm")],
        )}
      >
        <div className="flex shrink-0 items-start justify-between gap-4 px-6 pt-6 pb-3">
          <div className="min-w-0">
            <h2 className="text-lg font-bold">{title}</h2>
            {description && <p className="mt-1 text-sm leading-6 text-muted-foreground">{description}</p>}
          </div>
          <button onClick={onClose} className="-mr-2 -mt-2 shrink-0 rounded-full p-2 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground" aria-label="关闭">
            <X className="h-4 w-4" />
          </button>
        </div>
        <div className={cn("min-h-0 flex-1 px-6 pb-5 pt-3", fill ? "flex flex-col overflow-hidden" : "overflow-y-auto")}>{children}</div>
        {/* pb via calc(): the `.safe-bottom` utility would zero out pb-6 on desktop */}
        {footer && <div className="flex shrink-0 flex-wrap items-center justify-end gap-2 border-t border-border/60 px-6 pt-4 pb-[calc(1.5rem+env(safe-area-inset-bottom))] dark:border-border">{footer}</div>}
      </div>
    </div>
  );
}

export function Confirm({
  open,
  onClose,
  onConfirm,
  title,
  description,
  destructive,
  loading,
}: {
  open: boolean;
  onClose: () => void;
  onConfirm: () => void;
  title: string;
  description?: string;
  destructive?: boolean;
  loading?: boolean;
}) {
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      footer={
        <>
          <Button variant="outline" onClick={onClose}>
            取消
          </Button>
          <Button variant={destructive ? "destructive" : "default"} onClick={onConfirm} loading={loading}>
            确认
          </Button>
        </>
      }
    >
      <p className="text-sm text-muted-foreground">{description}</p>
    </Dialog>
  );
}

// ---------- Tabs ----------
export function Tabs<T extends string>({ value, onChange, items, className, variant = "pill" }: { value: T; onChange: (v: T) => void; items: { value: T; label: React.ReactNode }[]; className?: string; variant?: "pill" | "underline" }) {
  return (
    <div role={variant === "underline" ? "navigation" : undefined} aria-label={variant === "underline" ? "页面分区" : undefined} className={cn(variant === "underline" ? "flex w-full gap-5 overflow-x-auto border-b border-border text-muted-foreground" : "no-scrollbar inline-flex h-10 max-w-full items-center gap-1 overflow-x-auto rounded-full bg-muted p-1 text-muted-foreground", className)}>
      {items.map((it) => (
        <button
          key={it.value}
          type="button"
          aria-current={variant === "underline" && value === it.value ? "page" : undefined}
          onClick={() => onChange(it.value)}
          className={cn(
            variant === "underline" ? "inline-flex min-h-12 shrink-0 items-center justify-center whitespace-nowrap border-b-2 px-1 text-sm font-semibold transition-colors focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-primary/25" : "inline-flex h-8 items-center justify-center whitespace-nowrap rounded-full px-3.5 text-sm font-medium transition-all",
            variant === "underline" ? (value === it.value ? "border-primary text-primary" : "border-transparent hover:border-border hover:text-foreground") : (value === it.value ? "bg-card text-foreground shadow-xs" : "hover:text-foreground"),
          )}
        >
          {it.label}
        </button>
      ))}
    </div>
  );
}

// ---------- Table ----------
export function Table({ className, ...props }: React.TableHTMLAttributes<HTMLTableElement>) {
  return (
    <div className="w-full overflow-x-auto rounded-2xl border border-border/60 bg-card shadow-card dark:border-border">
      <table className={cn("w-full caption-bottom text-sm", className)} {...props} />
    </div>
  );
}
export function Th({ className, ...props }: React.ThHTMLAttributes<HTMLTableCellElement>) {
  return <th className={cn("h-11 bg-muted/50 px-4 text-left align-middle text-xs font-semibold text-muted-foreground whitespace-nowrap first:rounded-tl-2xl last:rounded-tr-2xl", className)} {...props} />;
}
export function Td({ className, ...props }: React.TdHTMLAttributes<HTMLTableCellElement>) {
  return <td className={cn("px-4 py-3 align-middle", className)} {...props} />;
}
export const Tr = React.forwardRef<HTMLTableRowElement, React.HTMLAttributes<HTMLTableRowElement>>(({ className, ...props }, ref) => (
  <tr ref={ref} className={cn("border-b border-border/60 transition-colors last:border-0 hover:bg-muted/40 dark:border-border", className)} {...props} />
));
Tr.displayName = "Tr";

// ---------- Misc ----------
export function Empty({ title, description, action, compact = false }: { title: string; description?: string; action?: React.ReactNode; compact?: boolean }) {
  return (
    <div className={cn("flex flex-col border border-dashed border-border bg-card/60", compact ? "items-start rounded-xl px-4 py-4 text-left" : "items-center justify-center rounded-2xl px-6 py-14 text-center")}>
      <p className="font-semibold">{title}</p>
      {description && <p className="mt-1 max-w-md text-sm text-muted-foreground">{description}</p>}
      {action && <div className={compact ? "mt-3" : "mt-5"}>{action}</div>}
    </div>
  );
}

export function Spinner({ className }: { className?: string }) {
  return (
    <div className={cn("flex items-center justify-center py-12", className)}>
      <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
    </div>
  );
}

export function Progress({ value, className, tone }: { value: number; className?: string; tone?: "ok" | "warn" | "bad" }) {
  const pct = Math.max(0, Math.min(100, value || 0));
  const color = tone === "bad" || pct >= 95 ? "bg-rose-500" : tone === "warn" || pct >= 80 ? "bg-amber-500" : "bg-primary";
  return (
    <div className={cn("h-2 w-full overflow-hidden rounded-full bg-foreground/6 dark:bg-white/10", className)}>
      <div className={cn("h-full rounded-full transition-all", color)} style={{ width: `${pct}%` }} />
    </div>
  );
}

const statTints: Record<Tint, string> = {
  peach: "bg-tint-peach text-primary",
  lavender: "bg-tint-lavender text-violet-600 dark:text-violet-300",
  rose: "bg-tint-rose text-rose-600 dark:text-rose-300",
  mint: "bg-tint-mint text-emerald-600 dark:text-emerald-300",
  sky: "bg-tint-sky text-sky-600 dark:text-sky-300",
  sand: "bg-tint-sand text-amber-700 dark:text-amber-300",
};
export function Stat({ label, value, sub, icon, tint = "peach" }: { label: string; value: React.ReactNode; sub?: React.ReactNode; icon?: React.ReactNode; tint?: Tint }) {
  return (
    <Card className="flex items-center gap-4 p-4 sm:p-5">
      {icon && <span className={cn("flex h-11 w-11 shrink-0 items-center justify-center rounded-2xl [&>svg]:h-5 [&>svg]:w-5", statTints[tint])}>{icon}</span>}
      <div className="min-w-0">
        <p className="text-xs font-medium text-muted-foreground">{label}</p>
        <p className="mt-0.5 truncate text-2xl font-bold tabular-nums leading-tight">{value}</p>
        {sub && <p className="mt-0.5 text-xs text-muted-foreground">{sub}</p>}
      </div>
    </Card>
  );
}

export function BackLink({ to, children }: { to: string; children: React.ReactNode }) {
  return (
    <Link
      to={to}
      aria-label={`返回${children}`}
      title={`返回${children}`}
      className="inline-flex h-10 w-10 shrink-0 items-center justify-center rounded-xl border border-border bg-card text-foreground shadow-xs transition-colors hover:bg-accent"
    >
      <ChevronLeft className="h-5 w-5" />
    </Link>
  );
}

export function PageHeader({
  title,
  description,
  actions,
  back,
}: {
  title: string;
  description?: string;
  actions?: React.ReactNode;
  back?: { to: string; label: string };
}) {
  return (
    <div className="mb-6 flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex min-w-0 items-start gap-3">
        {back && (
          <span className="mt-0.5">
            <BackLink to={back.to}>{back.label}</BackLink>
          </span>
        )}
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight sm:text-3xl">{title}</h1>
          {description && <p className="mt-1.5 text-sm text-muted-foreground">{description}</p>}
        </div>
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  );
}

/** Small section heading in the style of the reference ("신차 ›"). */
export function SectionTitle({ children, action, className }: { children: React.ReactNode; action?: React.ReactNode; className?: string }) {
  return (
    <div className={cn("mb-3 flex items-center justify-between gap-3", className)}>
      <h2 className="text-base font-bold tracking-tight">{children}</h2>
      {action}
    </div>
  );
}

export function Code({ children, className }: { children: React.ReactNode; className?: string }) {
  return <code className={cn("mono rounded-md bg-muted px-1.5 py-0.5 text-xs", className)}>{children}</code>;
}

export function Pre({ children, className }: { children: React.ReactNode; className?: string }) {
  return <pre className={cn("mono max-h-[60vh] overflow-auto rounded-xl bg-muted/70 p-4 text-xs leading-relaxed", className)}>{children}</pre>;
}
