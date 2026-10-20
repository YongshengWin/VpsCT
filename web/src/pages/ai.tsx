import { APIKeysCard, AgentConnectionCard } from "@/components/api-keys";
import { PageHeader, Spinner } from "@/components/ui";
import { useAuth } from "@/lib/auth";

export function AIPage() {
  const { user } = useAuth();
  if (!user) return <Spinner />;
  return (
    <div>
      <PageHeader title="AI 能力" description="创建 API key、安装 Skill，让 AI agent 接入 VpsCT。" />
      <div className="space-y-4">
        <APIKeysCard user={user} />
        <AgentConnectionCard />
      </div>
    </div>
  );
}
