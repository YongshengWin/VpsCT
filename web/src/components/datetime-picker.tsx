import * as React from "react";
import { createPortal } from "react-dom";
import { CalendarDays, ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight, X } from "lucide-react";
import { cn, parseResetDay } from "@/lib/utils";
import { Button, Select } from "@/components/ui";

// A themed replacement for <input type="datetime-local">. The value format is
// identical to the native control ("YYYY-MM-DDTHH:mm" in local time, "" for
// none) so call sites only swap the element.

const pad = (n: number) => String(n).padStart(2, "0");

export function parseLocalDateTime(v: string): Date | null {
  if (!v) return null;
  const d = new Date(v);
  return Number.isNaN(d.getTime()) ? null : d;
}

export function formatLocalDateTime(d: Date, withTime = true): string {
  const day = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
  return withTime ? `${day}T${pad(d.getHours())}:${pad(d.getMinutes())}` : day;
}

const WEEKDAYS = ["一", "二", "三", "四", "五", "六", "日"];
const HOURS = Array.from({ length: 24 }, (_, i) => i);
const MINUTES = Array.from({ length: 60 }, (_, i) => i);

function sameDay(a: Date, b: Date) {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate();
}

/** 42 cells (6 weeks) starting on the Monday on/before the 1st of the month. */
function monthGrid(year: number, month: number): Date[] {
  const first = new Date(year, month, 1);
  const offset = (first.getDay() + 6) % 7; // Monday = 0
  const start = new Date(year, month, 1 - offset);
  return Array.from({ length: 42 }, (_, i) => new Date(start.getFullYear(), start.getMonth(), start.getDate() + i));
}

interface Pos {
  left: number;
  top: number;
}

export interface DateTimeInputProps {
  value: string;
  onChange: (value: string) => void;
  /** include hour/minute (default true) */
  withTime?: boolean;
  placeholder?: string;
  disabled?: boolean;
  className?: string;
  id?: string;
}

const POP_W = 304;
const POP_H = 420;
const POP_H_DATE = 352;

export function DateTimeInput({ value, onChange, withTime = true, placeholder, disabled, className, id }: DateTimeInputProps) {
  const selected = React.useMemo(() => parseLocalDateTime(value), [value]);
  const [open, setOpen] = React.useState(false);
  const [pos, setPos] = React.useState<Pos | null>(null);
  const [view, setView] = React.useState(() => {
    const d = selected ?? new Date();
    return { year: d.getFullYear(), month: d.getMonth() };
  });
  const [focusDay, setFocusDay] = React.useState<Date>(() => selected ?? new Date());
  const wrapRef = React.useRef<HTMLDivElement>(null);
  const popRef = React.useRef<HTMLDivElement>(null);
  const today = new Date();

  const place = React.useCallback(() => {
    const el = wrapRef.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    const gap = 6;
    const h = withTime ? POP_H : POP_H_DATE;
    const fitsBelow = r.bottom + gap + h + 8 <= window.innerHeight;
    const fitsAbove = r.top - gap - h >= 8;
    let top = r.bottom + gap;
    if (!fitsBelow) {
      // prefer above when it fits; otherwise clamp inside the viewport
      top = fitsAbove ? r.top - gap - h : Math.max(8, window.innerHeight - h - 8);
    }
    setPos({ left: Math.max(8, Math.min(r.left, window.innerWidth - POP_W - 8)), top });
  }, [withTime]);

  const show = () => {
    if (disabled) return;
    const d = selected ?? new Date();
    setView({ year: d.getFullYear(), month: d.getMonth() });
    setFocusDay(d);
    place();
    setOpen(true);
  };
  const hide = React.useCallback(() => setOpen(false), []);

  // outside click / escape / scroll / resize
  React.useEffect(() => {
    if (!open) return;
    const inside = (t: EventTarget | null) => {
      if (!(t instanceof Node)) return false;
      if (wrapRef.current?.contains(t) || popRef.current?.contains(t)) return true;
      // nested floating layers (the hour/minute selects) live in their own portals
      return t instanceof Element && !!t.closest("[data-floating]");
    };
    const onDown = (e: MouseEvent) => {
      if (!inside(e.target)) hide();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") hide();
    };
    const onScroll = (e: Event) => {
      if (!inside(e.target)) hide();
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", place);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", place);
    };
  }, [open, hide, place]);

  const emit = (d: Date) => onChange(formatLocalDateTime(d, withTime));

  const pickDay = (d: Date) => {
    const base = selected ?? new Date(today.getFullYear(), today.getMonth(), today.getDate(), 0, 0);
    const next = new Date(d.getFullYear(), d.getMonth(), d.getDate(), withTime ? base.getHours() : 0, withTime ? base.getMinutes() : 0);
    emit(next);
    setFocusDay(next);
    setView({ year: next.getFullYear(), month: next.getMonth() });
    if (!withTime) hide();
  };

  const setTime = (h: number, m: number) => {
    const base = selected ?? today;
    emit(new Date(base.getFullYear(), base.getMonth(), base.getDate(), h, m));
  };

  const shiftMonth = (n: number) => {
    const d = new Date(view.year, view.month + n, 1);
    setView({ year: d.getFullYear(), month: d.getMonth() });
  };

  const moveFocus = (days: number) => {
    const d = new Date(focusDay.getFullYear(), focusDay.getMonth(), focusDay.getDate() + days);
    setFocusDay(d);
    setView({ year: d.getFullYear(), month: d.getMonth() });
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLButtonElement>) => {
    if (disabled) return;
    if (!open) {
      if (["ArrowDown", "ArrowUp", "Enter", " "].includes(e.key)) {
        e.preventDefault();
        show();
      }
      return;
    }
    switch (e.key) {
      case "ArrowLeft":
        e.preventDefault();
        moveFocus(-1);
        break;
      case "ArrowRight":
        e.preventDefault();
        moveFocus(1);
        break;
      case "ArrowUp":
        e.preventDefault();
        moveFocus(-7);
        break;
      case "ArrowDown":
        e.preventDefault();
        moveFocus(7);
        break;
      case "PageUp":
        e.preventDefault();
        shiftMonth(-1);
        break;
      case "PageDown":
        e.preventDefault();
        shiftMonth(1);
        break;
      case "Enter":
      case " ":
        e.preventDefault();
        pickDay(focusDay);
        break;
      case "Tab":
        hide();
        break;
    }
  };

  const cells = React.useMemo(() => monthGrid(view.year, view.month), [view.year, view.month]);
  const display = selected ? formatLocalDateTime(selected, withTime).replace("T", " ") : "";

  return (
    <div ref={wrapRef} className={cn("relative", className)}>
      <button
        type="button"
        id={id}
        disabled={disabled}
        aria-haspopup="dialog"
        aria-expanded={open}
        data-state={open ? "open" : "closed"}
        onClick={() => (open ? hide() : show())}
        onKeyDown={onKeyDown}
        className={cn(
          "flex h-10 w-full items-center gap-2.5 rounded-xl border border-transparent bg-muted px-3.5 text-left text-sm tabular-nums transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/25 data-[state=open]:border-primary/40 data-[state=open]:bg-card disabled:cursor-not-allowed disabled:opacity-50",
          selected && "pr-9",
        )}
      >
        <CalendarDays className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className={cn("min-w-0 flex-1 truncate", !selected && "text-muted-foreground/70")}>{display || placeholder || (withTime ? "选择日期和时间" : "选择日期")}</span>
      </button>
      {selected && !disabled && (
        <button
          type="button"
          aria-label="清除"
          onClick={() => {
            onChange("");
            hide();
          }}
          className="absolute right-2 top-1/2 -translate-y-1/2 rounded-full p-1 text-muted-foreground transition-colors hover:bg-foreground/10 hover:text-foreground"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      )}

      {open &&
        pos &&
        createPortal(
          <div
            ref={popRef}
            role="dialog"
            data-floating=""
            className="fixed z-[60] animate-fade-up rounded-2xl border border-border/60 bg-card p-3 shadow-lift dark:border-border"
            style={{ left: pos.left, top: pos.top, width: POP_W }}
          >
            {/* month navigation */}
            <div className="flex items-center justify-between">
              <div className="flex">
                <NavButton onClick={() => shiftMonth(-12)} label="上一年">
                  <ChevronsLeft className="h-4 w-4" />
                </NavButton>
                <NavButton onClick={() => shiftMonth(-1)} label="上个月">
                  <ChevronLeft className="h-4 w-4" />
                </NavButton>
              </div>
              <span className="text-sm font-bold tabular-nums">
                {view.year} 年 {view.month + 1} 月
              </span>
              <div className="flex">
                <NavButton onClick={() => shiftMonth(1)} label="下个月">
                  <ChevronRight className="h-4 w-4" />
                </NavButton>
                <NavButton onClick={() => shiftMonth(12)} label="下一年">
                  <ChevronsRight className="h-4 w-4" />
                </NavButton>
              </div>
            </div>

            {/* weekday header */}
            <div className="mt-2 grid grid-cols-7 text-center text-2xs font-medium text-muted-foreground">
              {WEEKDAYS.map((w) => (
                <span key={w} className="py-1">
                  {w}
                </span>
              ))}
            </div>

            {/* days */}
            <div role="grid" className="grid grid-cols-7 gap-y-0.5">
              {cells.map((d) => {
                const outside = d.getMonth() !== view.month;
                const isSel = !!selected && sameDay(d, selected);
                const isToday = sameDay(d, today);
                const isFocus = sameDay(d, focusDay);
                return (
                  <button
                    key={d.getTime()}
                    type="button"
                    role="gridcell"
                    aria-selected={isSel}
                    tabIndex={-1}
                    onClick={() => pickDay(d)}
                    onMouseDown={(e) => e.preventDefault()}
                    className={cn(
                      "mx-auto flex h-9 w-9 items-center justify-center rounded-full text-sm tabular-nums transition-colors hover:bg-accent",
                      outside && "text-muted-foreground/40",
                      isToday && !isSel && "font-bold text-primary",
                      isFocus && !isSel && "ring-2 ring-primary/30",
                      isSel && "bg-primary font-semibold text-primary-foreground shadow-[0_6px_14px_-6px_hsl(var(--primary)/0.7)] hover:bg-primary",
                    )}
                  >
                    {d.getDate()}
                  </button>
                );
              })}
            </div>

            {/* time */}
            {withTime && (
              <div className="mt-3 flex items-center gap-2 border-t border-border/60 pt-3 dark:border-border">
                <span className="mr-auto text-xs font-medium text-muted-foreground">时间</span>
                <Select className="min-h-8 w-24 py-1 text-xs" value={pad(selected?.getHours() ?? 0)} onChange={(e) => setTime(Number(e.target.value), selected?.getMinutes() ?? 0)} aria-label="小时">
                  {HOURS.map((h) => (
                    <option key={h} value={pad(h)}>
                      {pad(h)} 时
                    </option>
                  ))}
                </Select>
                <Select className="min-h-8 w-24 py-1 text-xs" value={pad(selected?.getMinutes() ?? 0)} onChange={(e) => setTime(selected?.getHours() ?? 0, Number(e.target.value))} aria-label="分钟">
                  {MINUTES.map((m) => (
                    <option key={m} value={pad(m)}>
                      {pad(m)} 分
                    </option>
                  ))}
                </Select>
              </div>
            )}

            {/* actions */}
            <div className="mt-3 flex items-center justify-between">
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="text-muted-foreground"
                onClick={() => {
                  onChange("");
                  hide();
                }}
              >
                清除
              </Button>
              <div className="flex gap-1">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    const n = new Date();
                    emit(n);
                    setFocusDay(n);
                    setView({ year: n.getFullYear(), month: n.getMonth() });
                  }}
                >
                  {withTime ? "现在" : "今天"}
                </Button>
                <Button type="button" size="sm" onClick={hide}>
                  确定
                </Button>
              </div>
            </div>
          </div>,
          document.body,
        )}
    </div>
  );
}

const RESET_POP_W = 304;
const RESET_POP_H = 400;

export interface ResetDayInputProps {
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  disabled?: boolean;
  className?: string;
  id?: string;
}

/** Pick 1–28, or last day of month (29/30/31 all store as 31). 0 = 不重置. */
export function ResetDayInput({ value, onChange, placeholder = "不重置", disabled, className, id }: ResetDayInputProps) {
  const day = parseResetDay(value);
  const [open, setOpen] = React.useState(false);
  const [pos, setPos] = React.useState<Pos | null>(null);
  const wrapRef = React.useRef<HTMLDivElement>(null);
  const popRef = React.useRef<HTMLDivElement>(null);
  const today = new Date();
  const view = { year: today.getFullYear(), month: today.getMonth() };
  const cells = React.useMemo(() => monthGrid(view.year, view.month), [view.year, view.month]);

  const place = React.useCallback(() => {
    const el = wrapRef.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    const gap = 6;
    const h = RESET_POP_H;
    const fitsBelow = r.bottom + gap + h + 8 <= window.innerHeight;
    const fitsAbove = r.top - gap - h >= 8;
    let top = r.bottom + gap;
    if (!fitsBelow) {
      top = fitsAbove ? r.top - gap - h : Math.max(8, window.innerHeight - h - 8);
    }
    setPos({ left: Math.max(8, Math.min(r.left, window.innerWidth - RESET_POP_W - 8)), top });
  }, []);

  const hide = React.useCallback(() => setOpen(false), []);
  const show = () => {
    if (disabled) return;
    place();
    setOpen(true);
  };

  React.useEffect(() => {
    if (!open) return;
    const inside = (t: EventTarget | null) => {
      if (!(t instanceof Node)) return false;
      return !!(wrapRef.current?.contains(t) || popRef.current?.contains(t));
    };
    const onDown = (e: MouseEvent) => {
      if (!inside(e.target)) hide();
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") hide();
    };
    const onScroll = (e: Event) => {
      if (!inside(e.target)) hide();
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", place);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", place);
    };
  }, [open, hide, place]);

  const pick = (n: number) => {
    if (n < 1 || n > 28) return;
    onChange(String(n));
    hide();
  };
  const pickLast = () => {
    onChange("31");
    hide();
  };
  const clear = () => {
    onChange("0");
    hide();
  };
  const lastDay = day >= 29;
  const display = lastDay ? "每月最后一天" : day > 0 ? `每月 ${day} 日` : "";

  return (
    <div ref={wrapRef} className={cn("relative", className)}>
      <button
        type="button"
        id={id}
        disabled={disabled}
        aria-haspopup="dialog"
        aria-expanded={open}
        data-state={open ? "open" : "closed"}
        onClick={() => (open ? hide() : show())}
        className={cn(
          "flex h-10 w-full items-center gap-2.5 rounded-xl border border-transparent bg-muted px-3.5 text-left text-sm tabular-nums transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/25 data-[state=open]:border-primary/40 data-[state=open]:bg-card disabled:cursor-not-allowed disabled:opacity-50",
          day > 0 && "pr-9",
        )}
      >
        <CalendarDays className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className={cn("min-w-0 flex-1 truncate", day <= 0 && "text-muted-foreground/70")}>{display || placeholder}</span>
      </button>
      {day > 0 && !disabled && (
        <button
          type="button"
          aria-label="清除"
          onClick={clear}
          className="absolute right-2 top-1/2 -translate-y-1/2 rounded-full p-1 text-muted-foreground transition-colors hover:bg-foreground/10 hover:text-foreground"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      )}

      {open &&
        pos &&
        createPortal(
          <div
            ref={popRef}
            role="dialog"
            data-floating=""
            className="fixed z-[60] animate-fade-up rounded-2xl border border-border/60 bg-card p-3 shadow-lift dark:border-border"
            style={{ left: pos.left, top: pos.top, width: RESET_POP_W }}
          >
            <p className="px-1 text-sm font-bold">每月哪一天重置</p>
            <div className="mt-2 grid grid-cols-7 text-center text-2xs font-medium text-muted-foreground">
              {WEEKDAYS.map((w) => (
                <span key={w} className="py-1">
                  {w}
                </span>
              ))}
            </div>
            <div role="grid" className="grid grid-cols-7 gap-y-0.5">
              {cells.map((d) => {
                const n = d.getDate();
                const outside = d.getMonth() !== view.month;
                const past28 = !outside && n >= 29;
                const allowed = !outside && n <= 28;
                const isSel = allowed && !lastDay && day === n;
                const isToday = allowed && n === today.getDate();
                return (
                  <button
                    key={d.getTime()}
                    type="button"
                    role="gridcell"
                    aria-selected={isSel}
                    disabled={!allowed}
                    tabIndex={-1}
                    onClick={() => allowed && pick(n)}
                    onMouseDown={(e) => e.preventDefault()}
                    className={cn(
                      "mx-auto flex h-9 w-9 items-center justify-center rounded-full text-sm tabular-nums transition-colors",
                      allowed && "hover:bg-accent",
                      (!allowed || outside || past28) && "cursor-not-allowed text-muted-foreground/35",
                      isToday && !isSel && "font-bold text-primary",
                      isSel && "bg-primary font-semibold text-primary-foreground shadow-[0_6px_14px_-6px_hsl(var(--primary)/0.7)] hover:bg-primary",
                    )}
                  >
                    {n}
                  </button>
                );
              })}
            </div>
            <Button
              type="button"
              variant={lastDay ? "default" : "outline"}
              size="sm"
              className="mt-3 w-full"
              onClick={pickLast}
            >
              每月最后一天
            </Button>
            <div className="mt-2 flex items-center justify-between">
              <Button type="button" variant="ghost" size="sm" className="text-muted-foreground" onClick={clear}>
                不重置
              </Button>
              <div className="flex gap-1">
                <Button type="button" variant="ghost" size="sm" onClick={() => (today.getDate() > 28 ? pickLast() : pick(today.getDate()))}>
                  今天
                </Button>
                <Button type="button" size="sm" onClick={hide}>
                  确定
                </Button>
              </div>
            </div>
          </div>,
          document.body,
        )}
    </div>
  );
}

function NavButton({ onClick, label, children }: { onClick: () => void; label: string; children: React.ReactNode }) {
  return (
    <button type="button" aria-label={label} onClick={onClick} onMouseDown={(e) => e.preventDefault()} className="rounded-full p-1.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground">
      {children}
    </button>
  );
}
