import * as React from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { get, post } from "@/lib/api";
import type { User, Meta } from "@/lib/types";

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
    queryFn: async () => {
      try {
        return await get<User>("/api/v1/auth/me");
      } catch {
        return null;
      }
    },
    retry: false,
  });
  const meta = useQuery({ queryKey: ["meta"], queryFn: () => get<Meta>("/api/v1/meta"), enabled: !!me.data });

  React.useEffect(() => {
    const onUnauthorized = () => { qc.setQueryData(["auth", "me"], null); qc.removeQueries({ predicate: (q) => q.queryKey[0] !== "auth" }); };
    window.addEventListener("ctlvps:unauthorized", onUnauthorized);
    return () => window.removeEventListener("ctlvps:unauthorized", onUnauthorized);
  }, [qc]);

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
