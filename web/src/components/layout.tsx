import * as React from "react";
import { NavLink, Outlet, useLocation, Link } from "react-router-dom";
import {
  LayoutDashboard,
  Server,
  Network,
  Rss,
  Link2,
  Users2,
  FileCode2,
  ScrollText,
  Settings,
  Sun,
  Moon,
  LogOut,
  Menu,
  X,
  UserCog,
  Share2,
  ChevronDown,
  Bot,
  BookOpen,
} from "lucide-react";
import { cn, displayName } from "@/lib/utils";
import { useAuth, useTheme } from "@/lib/auth";
import { LogoMark, Wordmark } from "@/components/logo";
import { Avatar } from "@/components/avatar";
import { CoreUpgradeNotice } from "@/components/core-upgrade";

interface Item {
  to: string;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  admin?: boolean;
}

// Primary navigation shown in the header. Account-level pages live in the
// avatar menu to keep the bar as light as the reference design.
const items: Item[] = [
  { to: "/", label: "总览", icon: LayoutDashboard },
  { to: "/servers", label: "服务器", icon: Server, admin: true },
  { to: "/nodes", label: "节点", icon: Network, admin: true },
  { to: "/externals", label: "订阅导入", icon: Rss, admin: true },
  { to: "/subscriptions", label: "订阅链接", icon: Link2 },
  { to: "/shares", label: "分享", icon: Share2 },
  { to: "/templates", label: "模板", icon: FileCode2, admin: true },
  { to: "/connlog", label: "连接日志", icon: ScrollText, admin: true },
];
const accountItems: Item[] = [
  { to: "/account", label: "账户设置", icon: UserCog },
  { to: "/ai", label: "AI 能力", icon: Bot },
  { to: "/users", label: "用户", icon: Users2, admin: true },
  { to: "/settings", label: "设置", icon: Settings, admin: true },
];

function Brand({ name }: { name: string }) {
  return (
    <Link to="/" className="flex items-center gap-2.5">
      <LogoMark size={32} shadow />
      <Wordmark name={name} className="text-base" />
    </Link>
  );
}

function IconButton({ className, ...props }: React.ButtonHTMLAttributes<HTMLButtonElement>) {
  return <button className={cn("inline-flex h-9 w-9 items-center justify-center rounded-full text-muted-foreground transition-colors hover:bg-accent hover:text-foreground", className)} {...props} />;
}

function HeaderHint({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="group relative flex items-center">
    {children}
    <span role="tooltip" className="pointer-events-none invisible absolute right-0 top-full mt-2 translate-y-1 whitespace-nowrap rounded-md border border-border/70 bg-card px-2.5 py-1.5 text-xs font-medium text-foreground opacity-0 shadow-md transition-[opacity,transform] duration-100 group-hover:visible group-focus-within:visible group-hover:translate-y-0 group-hover:opacity-100 group-focus-within:translate-y-0 group-focus-within:opacity-100">{label}</span>
  </div>;
}

export function Layout() {
  const { user, logout, meta } = useAuth();
  const { isDark, toggle } = useTheme();
  const [open, setOpen] = React.useState(false);
  const [menu, setMenu] = React.useState(false);
  const menuRef = React.useRef<HTMLDivElement>(null);
  const loc = useLocation();
  React.useEffect(() => {
    setOpen(false);
    setMenu(false);
  }, [loc.pathname]);
  React.useEffect(() => {
    if (!menu) return;
    const onDown = (e: MouseEvent) => {
      if (!menuRef.current?.contains(e.target as Node)) setMenu(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [menu]);

  const isAdmin = user?.role === "admin";
  const isManual = loc.pathname === "/manual" || loc.pathname === "/manual/";
  const visible = items.filter((i) => !i.admin || isAdmin);
  const account = accountItems.filter((i) => !i.admin || isAdmin);
  const mobileItems = visible.slice(0, 4);
  const siteName = meta?.site_name ?? "VpsCT";

  const drawerNav = (
    <nav className="flex flex-1 flex-col gap-0.5 px-3">
      {[...visible, ...account].map((it) => (
        <NavLink
          key={it.to}
          to={it.to}
          end={it.to === "/"}
          className={({ isActive }) =>
            cn(
              "flex items-center gap-3 rounded-xl px-3 py-2.5 text-sm font-medium transition-colors",
              isActive ? "bg-primary/10 text-primary" : "text-muted-foreground hover:bg-accent hover:text-foreground",
            )
          }
        >
          <it.icon className="h-4 w-4" />
          {it.label}
        </NavLink>
      ))}
    </nav>
  );

  return (
    <div className={cn("flex min-h-screen flex-col", isManual && "lg:h-dvh lg:min-h-0 lg:overflow-hidden")}>
      {/* header */}
      <header className="sticky top-0 z-30 border-b border-border/70 bg-card/85 backdrop-blur-md dark:border-border">
        <div className="px-4 sm:px-6">
        <div className="mx-auto flex h-16 w-full max-w-6xl items-center gap-4">
          <button onClick={() => setOpen(true)} className="-ml-2 rounded-full p-2 hover:bg-accent lg:hidden" aria-label="菜单">
            <Menu className="h-5 w-5" />
          </button>
          <Brand name={siteName} />

          {/* desktop nav */}
          <nav className="no-scrollbar mx-auto hidden min-w-0 flex-1 items-center justify-center gap-1 overflow-x-auto xl:gap-2 lg:flex">
            {visible.map((it) => (
              <NavLink
                key={it.to}
                to={it.to}
                end={it.to === "/"}
                className={({ isActive }) =>
                  cn(
                    "whitespace-nowrap rounded-full px-3 py-2 text-sm font-semibold transition-colors",
                    isActive ? "bg-primary/12 text-primary" : "text-muted-foreground hover:bg-accent hover:text-foreground",
                  )
                }
              >
                {it.label}
              </NavLink>
            ))}
          </nav>

          <div className="ml-auto flex items-center gap-1 lg:ml-0">
            <HeaderHint label="使用手册"><NavLink
              to="/manual"
              aria-label="使用手册"
              className={({ isActive }) => cn("inline-flex h-9 w-9 shrink-0 items-center justify-center rounded-full transition-colors hover:bg-accent focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-primary/40", isActive ? "text-primary" : "text-muted-foreground hover:text-foreground")}
            >
              <BookOpen className="h-[18px] w-[18px]" aria-hidden="true" />
            </NavLink></HeaderHint>
            <HeaderHint label="切换主题"><IconButton onClick={toggle} aria-label="切换主题">
              {isDark ? <Sun className="h-[18px] w-[18px]" /> : <Moon className="h-[18px] w-[18px]" />}
            </IconButton></HeaderHint>
            {/* account menu (desktop) */}
            <div ref={menuRef} className="relative hidden lg:block">
              <button
                onClick={() => setMenu((v) => !v)}
                className={cn("flex items-center gap-2 rounded-full py-1 pl-1 pr-2.5 text-sm transition-colors hover:bg-accent", menu && "bg-accent")}
                aria-haspopup="menu"
                aria-expanded={menu}
              >
                {user && <Avatar user={user} size={28} />}
                <span className="max-w-48 truncate font-medium">{displayName(user)}</span>
                <ChevronDown className={cn("h-3.5 w-3.5 text-muted-foreground transition-transform", menu && "rotate-180")} />
              </button>
              {menu && (
                <div role="menu" className="absolute right-0 top-full mt-2 w-56 animate-fade-up overflow-hidden rounded-2xl border border-border/60 bg-card p-1.5 shadow-lift dark:border-border">
                  <div className="flex items-center gap-3 px-3 py-2">
                    {user && <Avatar user={user} size={36} />}
                    <div className="min-w-0">
                      <p className="wrap-break-word text-sm font-semibold">{displayName(user)}</p>
                      <p className="text-xs text-muted-foreground">{displayName(user) !== user?.username ? `${user?.username} · ` : ""}{isAdmin ? "管理员" : "普通用户"}</p>
                    </div>
                  </div>
                  <div className="my-1 h-px bg-border/70" />
                  {account.map((it) => (
                    <NavLink
                      key={it.to}
                      to={it.to}
                      role="menuitem"
                      className={({ isActive }) =>
                        cn("flex items-center gap-2.5 rounded-xl px-3 py-2 text-sm transition-colors hover:bg-accent", isActive ? "text-primary" : "text-foreground")
                      }
                    >
                      <it.icon className="h-4 w-4" />
                      {it.label}
                    </NavLink>
                  ))}
                  <div className="my-1 h-px bg-border/70" />
                  <button onClick={logout} role="menuitem" className="flex w-full items-center gap-2.5 rounded-xl px-3 py-2 text-sm text-foreground transition-colors hover:bg-accent">
                    <LogOut className="h-4 w-4" />
                    退出登录
                  </button>
                </div>
              )}
            </div>
          </div>
        </div>
        </div>
      </header>

      {/* mobile drawer */}
      {open && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="absolute inset-0 bg-slate-900/40 backdrop-blur-xs" onClick={() => setOpen(false)} />
          <aside className="absolute inset-y-0 left-0 flex w-72 flex-col bg-card shadow-pop">
            <div className="flex h-16 items-center justify-between px-4">
              <Brand name={siteName} />
              <button onClick={() => setOpen(false)} className="rounded-full p-2 hover:bg-accent" aria-label="关闭">
                <X className="h-5 w-5" />
              </button>
            </div>
            {drawerNav}
            <div className="border-t border-border/70 p-3">
              <div className="flex items-center gap-3 rounded-xl px-2 py-1.5">
                {user && <Avatar user={user} size={36} />}
                <div className="min-w-0 flex-1">
                  <p className="wrap-break-word text-sm font-semibold">{displayName(user)}</p>
                  <p className="text-xs text-muted-foreground">{displayName(user) !== user?.username ? `${user?.username} · ` : ""}{isAdmin ? "管理员" : "普通用户"}</p>
                </div>
                <IconButton onClick={logout} title="退出登录" aria-label="退出登录">
                  <LogOut className="h-4 w-4" />
                </IconButton>
              </div>
            </div>
          </aside>
        </div>
      )}

      <main className={cn("flex-1 px-4 pb-24 pt-6 sm:px-6 sm:pt-8 lg:pb-10", isManual && "lg:min-h-0 lg:overflow-hidden lg:pb-8")}>
        <div className={cn("mx-auto w-full max-w-6xl animate-fade-up", isManual && "lg:flex lg:h-full lg:min-h-0 lg:flex-col")}>
          {isAdmin && !(loc.pathname === "/settings" && new URLSearchParams(loc.search).get("tab") === "cores") && <CoreUpgradeNotice />}
          <Outlet />
        </div>
      </main>

      {/* mobile bottom nav */}
      <nav className="fixed inset-x-0 bottom-0 z-30 grid grid-cols-5 border-t border-border/70 bg-card/95 backdrop-blur-sm safe-bottom lg:hidden">
        {mobileItems.map((it) => (
          <NavLink
            key={it.to}
            to={it.to}
            end={it.to === "/"}
            className={({ isActive }) => cn("flex flex-col items-center gap-1 py-2.5 text-2xs font-medium", isActive ? "text-primary" : "text-muted-foreground")}
          >
            <it.icon className="h-5 w-5" />
            {it.label}
          </NavLink>
        ))}
        <button onClick={() => setOpen(true)} className="flex flex-col items-center gap-1 py-2.5 text-2xs font-medium text-muted-foreground">
          <Menu className="h-5 w-5" />
          更多
        </button>
      </nav>
    </div>
  );
}
