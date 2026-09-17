import * as React from "react";
import { ApiError, post } from "@/lib/api";
import { useAuth, useTheme } from "@/lib/auth";
import { Button, Card, Field, Input } from "@/components/ui";
import { LogoMark, Wordmark } from "@/components/logo";
import { ArrowLeft, Moon, Sun } from "lucide-react";

type LoginResult = { requires_2fa?: boolean; challenge?: string } | null;

export function LoginPage() {
  const { needsSetup, refresh } = useAuth();
  const { isDark, toggle } = useTheme();
  const [error, setError] = React.useState("");
  const [loading, setLoading] = React.useState(false);
  // second factor
  const [challenge, setChallenge] = React.useState("");
  const [code, setCode] = React.useState("");

  const run = async (fn: () => Promise<void>) => {
    setError("");
    setLoading(true);
    try {
      await fn();
    } catch (err) {
      setError(err instanceof Error ? err.message : "登录失败");
    } finally {
      setLoading(false);
    }
  };

  const submit = (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault();
    // Read what is actually in the inputs, including password-manager autofill.
    const fields = new FormData(e.currentTarget);
    const username = String(fields.get("username") ?? "");
    const password = String(fields.get("password") ?? "");
    const confirm = String(fields.get("confirm") ?? "");
    const setupToken = String(fields.get("setup_token") ?? "");
    if (needsSetup && password !== confirm) {
      setError("两次输入的密码不一致");
      return;
    }
    void run(async () => {
      const r = await post<LoginResult>(needsSetup ? "/api/v1/auth/setup" : "/api/v1/auth/login", { username, password, ...(needsSetup ? { setup_token: setupToken.trim() } : {}) });
      if (r?.requires_2fa && r.challenge) {
        setChallenge(r.challenge);
        setCode("");
        return;
      }
      await refresh();
    });
  };

  const submitCode = (e: React.FormEvent) => {
    e.preventDefault();
    void run(async () => {
      try {
        await post("/api/v1/auth/login/2fa", { challenge, code: code.trim() });
      } catch (err) {
        // an expired/burnt challenge means starting over with the password
        if (err instanceof ApiError && err.code === "challenge_expired") setChallenge("");
        throw err;
      }
      await refresh();
    });
  };

  const back = () => {
    setChallenge("");
    setCode("");
    setError("");
  };

  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden px-4 py-10">
      {/* soft pastel backdrop */}
      <div aria-hidden className="pointer-events-none absolute -left-32 -top-32 h-96 w-96 rounded-full bg-tint-peach blur-3xl" />
      <div aria-hidden className="pointer-events-none absolute -bottom-40 -right-24 h-[28rem] w-[28rem] rounded-full bg-tint-lavender blur-3xl" />
      <div aria-hidden className="pointer-events-none absolute bottom-10 left-1/3 h-64 w-64 rounded-full bg-tint-sky blur-3xl" />

      <button onClick={toggle} className="absolute right-4 top-4 rounded-full p-2 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground" aria-label="切换主题">
        {isDark ? <Sun className="h-5 w-5" /> : <Moon className="h-5 w-5" />}
      </button>

      <Card className="relative w-full max-w-sm animate-fade-up p-7 shadow-lift sm:p-9">
        <div className="mb-8 text-center">
          <LogoMark size={56} shadow className="mx-auto" />
          <h1 className="mt-5 text-2xl">
            <Wordmark />
          </h1>
          <p className="mt-1.5 text-sm text-muted-foreground">
            {challenge ? "输入验证器 App 显示的 6 位动态码" : needsSetup ? "首次使用，请创建管理员账号" : "登录到控制台"}
          </p>
        </div>

        {challenge ? (
          <form onSubmit={submitCode} className="space-y-4">
            <Field label="验证码" hint="也可以输入一个恢复码（xxxx-xxxx）">
              <Input autoFocus inputMode="numeric" autoComplete="one-time-code" placeholder="123456" value={code} onChange={(e) => setCode(e.target.value)} className="mono text-center text-lg tracking-[0.3em]" required />
            </Field>
            {error && <p className="rounded-xl bg-rose-500/10 px-3 py-2 text-sm text-rose-600 dark:text-rose-300">{error}</p>}
            <Button type="submit" size="lg" className="mt-2 w-full" loading={loading}>
              验证并登录
            </Button>
            <button type="button" onClick={back} className="mx-auto flex items-center gap-1 text-sm text-muted-foreground hover:text-foreground">
              <ArrowLeft className="h-4 w-4" /> 返回重新输入密码
            </button>
          </form>
        ) : (
          <form onSubmit={submit} className="space-y-4">
            {needsSetup && (
              <Field label="初始化令牌" hint="从安装服务器的数据目录读取 setup-token 文件，创建管理员后自动失效。">
                <Input name="setup_token" aria-label="初始化令牌" type="password" autoComplete="off" required />
              </Field>
            )}
            <Field label="用户名">
              <Input name="username" autoFocus autoComplete="username" required />
            </Field>
            <Field label="密码" hint={needsSetup ? "至少 8 位" : undefined}>
              <Input name="password" type="password" autoComplete={needsSetup ? "new-password" : "current-password"} required minLength={needsSetup ? 8 : undefined} />
            </Field>
            {needsSetup && (
              <Field label="确认密码">
                <Input name="confirm" type="password" autoComplete="new-password" required />
              </Field>
            )}
            {error && <p className="rounded-xl bg-rose-500/10 px-3 py-2 text-sm text-rose-600 dark:text-rose-300">{error}</p>}
            <Button type="submit" size="lg" className="mt-2 w-full" loading={loading}>
              {needsSetup ? "创建管理员并登录" : "登录"}
            </Button>
          </form>
        )}
      </Card>
    </div>
  );
}
