import * as React from "react";
import { QueryClient, QueryClientProvider, useQueryClient } from "@tanstack/react-query";
import { BrowserRouter, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { AuthProvider, useAuth } from "@/lib/auth";
import { ToastProvider } from "@/components/toast";
import { Layout } from "@/components/layout";
import { Spinner } from "@/components/ui";
import { ApiError } from "@/lib/api";
import { LoginPage } from "@/pages/login";
import { DashboardPage } from "@/pages/dashboard";
import { ServersPage, ServerDetailPage } from "@/pages/servers";
import { NodesPage } from "@/pages/nodes";
import { ExternalsPage } from "@/pages/externals";
import { SubscriptionsPage, SubscriptionEditorPage } from "@/pages/subscriptions";
import { SharesPage, ShareDetailPage } from "@/pages/shares";
import { TemplatesPage } from "@/pages/templates";
import { ConnlogPage } from "@/pages/connlog";
import { UsersPage } from "@/pages/users";
import { SettingsPage } from "@/pages/settings";
import { AccountPage } from "@/pages/account";

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (count, err) => !(err instanceof ApiError && err.status >= 400 && err.status < 500) && count < 2,
      staleTime: 5_000,
      refetchOnWindowFocus: true,
    },
  },
});

function RequireAuth({ children, admin }: { children: React.ReactNode; admin?: boolean }) {
  const { user, loading } = useAuth();
  const loc = useLocation();
  if (loading) return <Spinner className="min-h-screen" />;
  if (!user) return <Navigate to="/login" replace state={{ from: loc.pathname }} />;
  if (admin && user.role !== "admin") return <Navigate to="/" replace />;
  return <>{children}</>;
}

function LoginRoute() {
  const { user, loading } = useAuth();
  if (loading) return <Spinner className="min-h-screen" />;
  if (user) return <Navigate to="/" replace />;
  return <LoginPage />;
}

// Live updates: subscribe to SSE and invalidate affected queries.
function EventStream() {
  const qc = useQueryClient();
  const { user } = useAuth();
  React.useEffect(() => {
    if (!user) return;
    let es: EventSource | null = null;
    let timer: number | undefined;
    // coalesce bursts (many agents heartbeating) into one refetch per key every few seconds
    const pending = new Set<string>();
    let flush: number | undefined;
    const inval = (keys: string[][]) => {
      keys.forEach((k) => pending.add(JSON.stringify(k)));
      if (flush) return;
      flush = window.setTimeout(() => {
        pending.forEach((k) => qc.invalidateQueries({ queryKey: JSON.parse(k) }));
        pending.clear();
        flush = undefined;
      }, 3000);
    };
    const connect = () => {
      es = new EventSource("/api/v1/events");
      es.addEventListener("agent.heartbeat", () => inval([["servers"], ["dashboard"]]));
      es.addEventListener("agent.enrolled", () => inval([["servers"], ["dashboard"]]));
      es.addEventListener("agent.applied", () => inval([["servers"], ["nodes"]]));
      es.addEventListener("share.changed", () => inval([["shares"], ["subscriptions"], ["nodes"], ["dashboard"]]));
      es.addEventListener("quota", () => inval([["servers"], ["shares"], ["dashboard"]]));
      es.onerror = () => {
        es?.close();
        timer = window.setTimeout(connect, 5000);
      };
    };
    connect();
    return () => {
      es?.close();
      if (timer) window.clearTimeout(timer);
      if (flush) window.clearTimeout(flush);
    };
  }, [qc, user]);
  return null;
}

// Last line of defence: a render error in one page must not blank the whole app.
class ErrorBoundary extends React.Component<{ children: React.ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null };
  static getDerivedStateFromError(error: Error) {
    return { error };
  }
  componentDidCatch(error: Error, info: React.ErrorInfo) {
    console.error("render error", error, info.componentStack);
  }
  render() {
    if (!this.state.error) return this.props.children;
    return (
      <div className="flex min-h-screen flex-col items-center justify-center gap-3 p-6 text-center">
        <h1 className="text-lg font-semibold">页面渲染出错</h1>
        <pre className="max-w-xl overflow-auto rounded-md border bg-muted/40 p-3 text-left text-xs">{String(this.state.error?.message || this.state.error)}</pre>
        <div className="flex gap-2">
          <button className="rounded-md border px-3 py-1.5 text-sm" onClick={() => this.setState({ error: null })}>重试</button>
          <button className="rounded-md border px-3 py-1.5 text-sm" onClick={() => { window.location.href = "/"; }}>返回总览</button>
        </div>
      </div>
    );
  }
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <BrowserRouter>
          <AuthProvider>
            <EventStream />
            <ErrorBoundary>
            <Routes>
              <Route path="/login" element={<LoginRoute />} />
              <Route element={<RequireAuth><Layout /></RequireAuth>}>
                <Route index element={<DashboardPage />} />
                <Route path="servers" element={<RequireAuth admin><ServersPage /></RequireAuth>} />
                <Route path="servers/:id" element={<RequireAuth admin><ServerDetailPage /></RequireAuth>} />
                <Route path="nodes" element={<RequireAuth admin><NodesPage /></RequireAuth>} />
                <Route path="externals" element={<RequireAuth admin><ExternalsPage /></RequireAuth>} />
                <Route path="subscriptions" element={<SubscriptionsPage />} />
                <Route path="subscriptions/new" element={<RequireAuth admin><SubscriptionEditorPage /></RequireAuth>} />
                <Route path="subscriptions/:id/edit" element={<RequireAuth admin><SubscriptionEditorPage /></RequireAuth>} />
                <Route path="shares" element={<SharesPage />} />
                <Route path="shares/:id" element={<ShareDetailPage />} />
                <Route path="templates" element={<RequireAuth admin><TemplatesPage /></RequireAuth>} />
                <Route path="connlog" element={<RequireAuth admin><ConnlogPage /></RequireAuth>} />
                <Route path="account" element={<AccountPage />} />
                <Route path="users" element={<RequireAuth admin><UsersPage /></RequireAuth>} />
                <Route path="settings" element={<RequireAuth admin><SettingsPage /></RequireAuth>} />
                <Route path="*" element={<Navigate to="/" replace />} />
              </Route>
            </Routes>
            </ErrorBoundary>
          </AuthProvider>
        </BrowserRouter>
      </ToastProvider>
    </QueryClientProvider>
  );
}
