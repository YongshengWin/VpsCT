import * as React from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Download, ImagePlus, KeyRound, ShieldCheck, ShieldOff, Trash2 } from "lucide-react";
import { post, put } from "@/lib/api";
import type { User } from "@/lib/types";
import { cn, displayName, fmtDate } from "@/lib/utils";
import { Badge, Button, Card, Dialog, Field, Input, PageHeader, Spinner } from "@/components/ui";
import { AVATAR_PRESETS, Avatar, PresetAvatar, fileToAvatarDataURL } from "@/components/avatar";
import { QR } from "@/pages/nodes";
import { useToast } from "@/components/toast";
import { useAuth } from "@/lib/auth";

export function AccountPage() {
  const { user } = useAuth();
  if (!user) return <Spinner />;
  return (
    <div>
      <PageHeader title="账户设置" />
      <div className="grid gap-4 lg:grid-cols-[1fr_1fr]">
        <div className="space-y-4">
          <ProfileCard user={user} />
          <AvatarCard user={user} />
        </div>
        <div className="space-y-4">
          <TwoFactorCard user={user} />
          <PasswordCard />
        </div>
      </div>
    </div>
  );
}

function ProfileCard({ user }: { user: User }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [nick, setNick] = React.useState(user.nickname ?? "");
  React.useEffect(() => { setNick(user.nickname ?? ""); }, [user.nickname]);
  const m = useMutation({
    mutationFn: () => put<User>("/api/v1/auth/profile", { nickname: nick }),
    onSuccess: (u) => {
      qc.setQueryData(["auth", "me"], u);
      qc.invalidateQueries({ queryKey: ["users"] });
      toast.success("昵称已保存");
    },
    onError: (e) => toast.fromError(e),
  });
  return (
    <Card className="p-5 sm:p-6">
      <div className="flex items-center gap-4">
        <Avatar user={user} size={72} className="ring-4 ring-background shadow-card" />
        <div className="min-w-0">
          <p className="break-words text-lg font-bold">{displayName(user)}</p>
          <p className="text-sm text-muted-foreground">
            {user.username} · {user.role === "admin" ? "管理员" : "普通用户"} · 加入于 {fmtDate(user.created_at, false)}
          </p>
        </div>
      </div>
      <div className="mt-5 grid gap-3 sm:grid-cols-[1fr_auto] sm:items-end">
        <Field label="昵称" hint="展示用，登录仍用账号。留空则显示账号">
          <Input value={nick} onChange={(e) => setNick(e.target.value)} maxLength={32} placeholder={user.username} />
        </Field>
        <Button onClick={() => m.mutate()} loading={m.isPending} disabled={nick.trim() === (user.nickname ?? "").trim()}>保存</Button>
      </div>
    </Card>
  );
}

// ---------- avatar ----------

function AvatarCard({ user }: { user: User }) {
  const qc = useQueryClient();
  const toast = useToast();
  const fileRef = React.useRef<HTMLInputElement>(null);
  const m = useMutation({
    mutationFn: (avatar: string) => put<User>("/api/v1/auth/avatar", { avatar }),
    onSuccess: (u) => {
      qc.setQueryData(["auth", "me"], u);
      qc.invalidateQueries({ queryKey: ["users"] });
    },
    onError: (e) => toast.fromError(e),
  });
  const onFile = async (f: File | undefined) => {
    if (!f) return;
    try {
      m.mutate(await fileToAvatarDataURL(f));
    } catch (e) {
      toast.fromError(e);
    }
  };
  const current = user.avatar ?? "";
  const uploaded = current.startsWith("/");
  return (
    <Card className="p-5 sm:p-6">
      <p className="mb-2 text-sm font-medium">头像</p>
      <div className="grid grid-cols-6 gap-2 sm:grid-cols-8">
        <button
          type="button"
          onClick={() => m.mutate("")}
          title="首字母"
          className={cn("flex aspect-square items-center justify-center rounded-full ring-offset-2 ring-offset-card transition hover:scale-105", current === "" && "ring-2 ring-primary")}
        >
          <Avatar user={{ username: user.username, nickname: user.nickname, display_name: user.display_name, avatar: "" }} size={40} />
        </button>
        {AVATAR_PRESETS.map((p) => {
          const v = `preset:${p.id}`;
          return (
            <button key={p.id} type="button" onClick={() => m.mutate(v)} title={p.label} className={cn("aspect-square rounded-full ring-offset-2 ring-offset-card transition hover:scale-105", current === v && "ring-2 ring-primary")}>
              <PresetAvatar preset={p} size={40} className="h-full w-full" />
            </button>
          );
        })}
      </div>
      <div className="mt-4 flex flex-wrap items-center gap-2">
        <input ref={fileRef} type="file" accept="image/png,image/jpeg,image/webp,image/gif" className="hidden" onChange={(e) => { void onFile(e.target.files?.[0]); e.target.value = ""; }} />
        <Button variant="outline" size="sm" onClick={() => fileRef.current?.click()} loading={m.isPending}>
          <ImagePlus className="h-4 w-4" /> 上传图片
        </Button>
        {uploaded && (
          <Button variant="ghost" size="sm" onClick={() => m.mutate("")}>
            <Trash2 className="h-4 w-4" /> 移除上传的图片
          </Button>
        )}
        <span className="text-xs text-muted-foreground">自动裁成正方形并缩放到 256px</span>
      </div>
    </Card>
  );
}

// ---------- password ----------

function PasswordCard() {
  const toast = useToast();
  const [f, setF] = React.useState({ old: "", n1: "", n2: "" });
  const m = useMutation({
    mutationFn: () => {
      if (f.n1 !== f.n2) throw new Error("两次输入的新密码不一致");
      return post("/api/v1/auth/password", { old_password: f.old, new_password: f.n1 });
    },
    onSuccess: () => {
      toast.success("密码已修改");
      setF({ old: "", n1: "", n2: "" });
    },
    onError: (e) => toast.fromError(e),
  });
  return (
    <Card className="p-5 sm:p-6">
      <div className="mb-4 flex items-center gap-2">
        <KeyRound className="h-4 w-4 text-muted-foreground" />
        <h2 className="font-semibold">修改密码</h2>
      </div>
      <form
        className="grid gap-3 sm:grid-cols-3"
        onSubmit={(e) => {
          e.preventDefault();
          m.mutate();
        }}
      >
        <Field label="当前密码"><Input type="password" value={f.old} onChange={(e) => setF({ ...f, old: e.target.value })} autoComplete="current-password" required /></Field>
        <Field label="新密码"><Input type="password" value={f.n1} onChange={(e) => setF({ ...f, n1: e.target.value })} autoComplete="new-password" minLength={8} required /></Field>
        <Field label="确认新密码"><Input type="password" value={f.n2} onChange={(e) => setF({ ...f, n2: e.target.value })} autoComplete="new-password" required /></Field>
        <div className="sm:col-span-3">
          <Button type="submit" variant="outline" loading={m.isPending}>修改密码</Button>
        </div>
      </form>
    </Card>
  );
}

// ---------- two-factor ----------

function TwoFactorCard({ user }: { user: User }) {
  const [dlg, setDlg] = React.useState<null | "enable" | "disable" | "recovery">(null);
  const on = user.totp_enabled;
  return (
    <Card className="p-5 sm:p-6">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="flex items-start gap-3">
          <span className={cn("mt-0.5 flex h-9 w-9 shrink-0 items-center justify-center rounded-xl", on ? "bg-tint-mint text-emerald-600 dark:text-emerald-300" : "bg-muted text-muted-foreground")}>
            {on ? <ShieldCheck className="h-5 w-5" /> : <ShieldOff className="h-5 w-5" />}
          </span>
          <div>
            <div className="flex items-center gap-2">
              <h2 className="font-semibold">两步验证</h2>
              <Badge variant={on ? "success" : "outline"}>{on ? "已开启" : "未开启"}</Badge>
            </div>
            <p className="mt-1 text-sm text-muted-foreground">
              {on ? "登录时除密码外还需输入验证器 App 的 6 位动态码。" : "使用 Google Authenticator、1Password 等验证器 App，为登录增加一道动态码。"}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          {on ? (
            <>
              <Button variant="outline" size="sm" onClick={() => setDlg("recovery")}>重新生成恢复码</Button>
              <Button variant="ghost" size="sm" className="text-rose-600" onClick={() => setDlg("disable")}>关闭</Button>
            </>
          ) : (
            <Button size="sm" onClick={() => setDlg("enable")}>开启两步验证</Button>
          )}
        </div>
      </div>
      <EnableDialog open={dlg === "enable"} onClose={() => setDlg(null)} />
      <FactorDialog
        open={dlg === "disable"}
        onClose={() => setDlg(null)}
        title="关闭两步验证"
        description="输入密码和当前验证码（或一个恢复码）以确认。"
        action="/api/v1/auth/2fa/disable"
        submitLabel="关闭"
        destructive
      />
      <FactorDialog
        open={dlg === "recovery"}
        onClose={() => setDlg(null)}
        title="重新生成恢复码"
        description="旧的恢复码会立即失效。请输入密码和当前验证码。"
        action="/api/v1/auth/2fa/recovery"
        submitLabel="生成"
      />
    </Card>
  );
}

function EnableDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [step, setStep] = React.useState<1 | 2 | 3>(1);
  const [password, setPassword] = React.useState("");
  const [code, setCode] = React.useState("");
  const [setup, setSetup] = React.useState<{ secret: string; otpauth_url: string } | null>(null);
  const [codes, setCodes] = React.useState<string[]>([]);
  React.useEffect(() => {
    if (open) {
      setStep(1);
      setPassword("");
      setCode("");
      setSetup(null);
      setCodes([]);
    }
  }, [open]);
  const start = useMutation({
    mutationFn: () => post<{ secret: string; otpauth_url: string }>("/api/v1/auth/2fa/setup", { password }),
    onSuccess: (r) => {
      setSetup(r);
      setStep(2);
    },
    onError: (e) => toast.fromError(e),
  });
  const enable = useMutation({
    mutationFn: () => post<{ recovery_codes: string[] }>("/api/v1/auth/2fa/enable", { code }),
    onSuccess: (r) => {
      setCodes(r.recovery_codes);
      setStep(3);
      qc.invalidateQueries({ queryKey: ["auth", "me"] });
      qc.invalidateQueries({ queryKey: ["users"] });
    },
    onError: (e) => toast.fromError(e),
  });
  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    if (step === 1) start.mutate();
    else if (step === 2) enable.mutate();
  };
  const footer =
    step === 3 ? (
      <Button onClick={onClose}>我已保存恢复码</Button>
    ) : (
      <>
        <Button variant="outline" onClick={onClose}>取消</Button>
        <Button type="submit" form="enable-2fa" loading={start.isPending || enable.isPending}>{step === 1 ? "下一步" : "开启"}</Button>
      </>
    );
  return (
    <Dialog open={open} onClose={step === 3 ? onClose : onClose} title="开启两步验证" description={step === 1 ? "先验证密码" : step === 2 ? "用验证器 App 扫码，然后输入显示的 6 位数字" : "两步验证已开启"} footer={footer}>
      <form id="enable-2fa" onSubmit={submit} className="space-y-4">
        {step === 1 && (
          <Field label="当前密码">
            <Input type="password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required />
          </Field>
        )}
        {step === 2 && setup && (
          <>
            <QR text={setup.otpauth_url} className="mt-0 w-44" />
            <div className="rounded-xl bg-muted px-3 py-2 text-center">
              <p className="text-2xs uppercase tracking-wider text-muted-foreground">无法扫码时手动输入密钥</p>
              <p className="mono mt-1 break-all text-sm font-semibold tracking-wider">{setup.secret.replace(/(.{4})/g, "$1 ").trim()}</p>
            </div>
            <Field label="验证码">
              <Input autoFocus inputMode="numeric" pattern="[0-9 ]*" maxLength={7} placeholder="123456" value={code} onChange={(e) => setCode(e.target.value)} className="mono text-center text-lg tracking-[0.3em]" required />
            </Field>
          </>
        )}
        {step === 3 && <RecoveryCodes codes={codes} />}
      </form>
    </Dialog>
  );
}

/** Password + code confirmation used by disable / regenerate-recovery. */
function FactorDialog({ open, onClose, title, description, action, submitLabel, destructive }: { open: boolean; onClose: () => void; title: string; description: string; action: string; submitLabel: string; destructive?: boolean }) {
  const qc = useQueryClient();
  const toast = useToast();
  const [password, setPassword] = React.useState("");
  const [code, setCode] = React.useState("");
  const [codes, setCodes] = React.useState<string[] | null>(null);
  React.useEffect(() => {
    if (open) {
      setPassword("");
      setCode("");
      setCodes(null);
    }
  }, [open]);
  const m = useMutation({
    mutationFn: () => post<{ recovery_codes?: string[] } | undefined>(action, { password, code }),
    onSuccess: (r) => {
      qc.invalidateQueries({ queryKey: ["auth", "me"] });
      qc.invalidateQueries({ queryKey: ["users"] });
      if (r?.recovery_codes) setCodes(r.recovery_codes);
      else {
        toast.success("两步验证已关闭");
        onClose();
      }
    },
    onError: (e) => toast.fromError(e),
  });
  const id = `factor-${action.replace(/\W/g, "")}`;
  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      description={codes ? "新的恢复码只显示这一次" : description}
      footer={
        codes ? (
          <Button onClick={onClose}>我已保存恢复码</Button>
        ) : (
          <>
            <Button variant="outline" onClick={onClose}>取消</Button>
            <Button type="submit" form={id} variant={destructive ? "destructive" : "default"} loading={m.isPending}>{submitLabel}</Button>
          </>
        )
      }
    >
      {codes ? (
        <RecoveryCodes codes={codes} />
      ) : (
        <form
          id={id}
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            m.mutate();
          }}
        >
          <Field label="当前密码"><Input type="password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required /></Field>
          <Field label="验证码或恢复码"><Input value={code} onChange={(e) => setCode(e.target.value)} placeholder="123456 或 xxxx-xxxx" className="mono" autoComplete="one-time-code" required /></Field>
        </form>
      )}
    </Dialog>
  );
}

function RecoveryCodes({ codes }: { codes: string[] }) {
  const toast = useToast();
  const [copied, setCopied] = React.useState(false);
  const text = codes.join("\n");
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      toast.error("复制失败，请手动选择文本");
    }
  };
  const download = () => {
    const blob = new Blob([`VpsCT 两步验证恢复码\n每个只能使用一次\n\n${text}\n`], { type: "text/plain" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = "vpsct-recovery-codes.txt";
    a.click();
    setTimeout(() => URL.revokeObjectURL(a.href), 1000);
  };
  return (
    <div>
      <p className="mb-3 text-sm text-muted-foreground">手机丢失时，可用下面任意一个恢复码代替验证码登录。每个只能用一次，请离线保存。</p>
      <div className="mono grid grid-cols-2 gap-2 rounded-xl bg-muted p-3 text-sm font-semibold tracking-wider">
        {codes.map((c) => (
          <span key={c} className="rounded-md bg-card px-2 py-1.5 text-center">{c}</span>
        ))}
      </div>
      <div className="mt-3 flex gap-2">
        <Button variant="outline" size="sm" onClick={copy}>{copied ? <Check className="h-4 w-4" /> : <Copy className="h-4 w-4" />} 复制</Button>
        <Button variant="outline" size="sm" onClick={download}><Download className="h-4 w-4" /> 下载</Button>
      </div>
    </div>
  );
}
