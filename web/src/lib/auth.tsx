import * as React from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post } from "@/lib/api";
import type { User, Meta } from "@/lib/types";
import { loadSession, retrySession } from "@/lib/session";

interface AuthState {
  user: User | null;
  loading: boolean;
  needsSetup: boolean;
  meta?: Meta;
  refresh: () => Promise<void>;
  logout: () => Promise<void>;
}

const Ctx = React.createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const qc = useQueryClient();
  const setup = useQuery({ queryKey: ["auth", "setup"], queryFn: () => get<{ needs_setup: boolean; site_name: string }>("/api/v1/auth/setup") });
  const me = useQuery({
    queryKey: ["auth", "me"],
    queryFn: () => loadSession(() => get<User>("/api/v1/auth/me")),
    retry: retrySession,
  });
  const meta = useQuery({ queryKey: ["meta"], queryFn: () => get<Meta>("/api/v1/meta"), enabled: !!me.data });

  React.useEffect(() => {
    // A delayed response from an older request must not erase a new login.
    const onUnauthorized = () => { void qc.invalidateQueries({ queryKey: ["auth", "me"] }, { cancelRefetch: false }); };
    window.addEventListener("ctlvps:unauthorized", onUnauthorized);
    return () => window.removeEventListener("ctlvps:unauthorized", onUnauthorized);
  }, [qc]);

  React.useEffect(() => {
    if (me.data === null) qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "auth" });
  }, [me.data, qc]);

  const value: AuthState = {
    user: me.data ?? null,
    loading: setup.isLoading || me.isLoading,
    needsSetup: !!setup.data?.needs_setup,
    meta: meta.data,
    refresh: async () => {
      await qc.invalidateQueries({ queryKey: ["auth"] });
    },
    logout: async () => {
      try {
        await post("/api/v1/auth/logout");
      } finally {
        // Update the live ["auth","me"] query first: its observers get notified,
        // RequireAuth redirects to /login and the protected pages unmount.
        // (qc.clear() would detach observers without notifying them, leaving the
        // UI logged in.) Then drop everything else so the next user starts clean.
        qc.setQueryData(["auth", "me"], null);
        qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "auth" });
      }
    },
  };
  if (me.isError && me.data === undefined) return <div role="alert" className="mx-auto mt-20 max-w-md space-y-4 rounded-xl border bg-card p-6">
    <h1 className="text-lg font-semibold">暂时无法确认登录状态</h1>
    <p className="text-sm text-muted-foreground">连接暂时不可用，请重试，无需重新输入密码。</p>
    <button className="rounded-lg bg-primary px-4 py-2 text-primary-foreground disabled:opacity-50" disabled={me.isFetching} onClick={() => void me.refetch()}>{me.isFetching ? "正在重试…" : "重试连接"}</button>
  </div>;
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useAuth() {
  const v = React.useContext(Ctx);
  if (!v) throw new Error("AuthProvider missing");
  return v;
}

export function useIsAdmin() {
  return useAuth().user?.role === "admin";
}

// ---- theme ----
// Implemented as a global store in lib/theme.ts; re-exported here so existing
// `import { useTheme } from "@/lib/auth"` call sites keep working.
export { useTheme } from "@/lib/theme";
export type { Theme } from "@/lib/theme";
