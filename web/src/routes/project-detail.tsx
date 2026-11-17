import { useQuery } from "@tanstack/react-query";
import { getRouteApi, Link } from "@tanstack/react-router";
import { ChevronLeft } from "lucide-react";

import { CopyButton, PageHeader, StatusBadge } from "@/components/common";
import { ApiKeysTab } from "@/components/project/api-keys-tab";
import { CloudTab } from "@/components/project/cloud-tab";
import { ModesTab } from "@/components/project/modes-tab";
import { PresetsTab } from "@/components/project/presets-tab";
import { SettingsTab } from "@/components/project/settings-tab";
import { SigningKeysTab } from "@/components/project/signing-keys-tab";
import { WebhooksTab } from "@/components/project/webhooks-tab";
import { EnvBadge } from "@/components/project-form";
import { ErrorState } from "@/components/states";
import { Skeleton } from "@/components/ui/skeleton";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { isApiError } from "@/lib/api";
import { projectQuery } from "@/lib/queries";
import type { ProjectTab } from "@/router";

const route = getRouteApi("/app/projects/$projectId");

export default function ProjectDetailPage() {
  const { projectId } = route.useParams();
  const { tab = "settings" } = route.useSearch();
  const navigate = route.useNavigate();
  const project = useQuery(projectQuery(projectId));

  const back = (
    <Link
      to="/projects"
      className="mb-2 inline-flex items-center gap-1 rounded-sm text-sm text-muted-foreground outline-none hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50"
    >
      <ChevronLeft className="size-4" /> Projects
    </Link>
  );

  if (project.isPending) {
    return (
      <>
        {back}
        <Skeleton className="h-8 w-56" />
        <Skeleton className="mt-2 h-4 w-32" />
        <Skeleton className="mt-8 h-9 w-72" />
        <Skeleton className="mt-4 h-64 w-full" />
      </>
    );
  }

  if (project.isError) {
    return (
      <>
        {back}
        <ErrorState
          title={isApiError(project.error) && project.error.status === 404 ? "Project not found" : "Couldn't load project"}
          error={project.error}
          onRetry={() => void project.refetch()}
        />
      </>
    );
  }

  const p = project.data;

  return (
    <>
      <PageHeader
        title={
          <span className="flex flex-wrap items-center gap-2.5">
            {p.name}
            <EnvBadge env={p.environment} />
            {p.disabled && <StatusBadge tone="neutral">disabled</StatusBadge>}
          </span>
        }
        description={
          <span className="inline-flex items-center gap-1 font-mono text-xs">
            {p.slug}
            <span className="text-muted-foreground/50">·</span>
            {p.id}
            <CopyButton value={p.id} label="Copy project ID" size="icon-xs" />
          </span>
        }
      >
        {back}
      </PageHeader>

      <Tabs value={tab} onValueChange={(v) => void navigate({ search: { tab: v as ProjectTab }, replace: true })}>
        <TabsList variant="line" className="mb-4 w-full justify-start overflow-x-auto border-b pb-0">
          <TabsTrigger className="flex-none px-3" value="settings">Settings</TabsTrigger>
          <TabsTrigger className="flex-none px-3" value="keys">API keys</TabsTrigger>
          <TabsTrigger className="flex-none px-3" value="presets">Presets</TabsTrigger>
          <TabsTrigger className="flex-none px-3" value="modes">Compression modes</TabsTrigger>
          <TabsTrigger className="flex-none px-3" value="webhooks">Webhooks</TabsTrigger>
          <TabsTrigger className="flex-none px-3" value="signing">Signing keys</TabsTrigger>
          <TabsTrigger className="flex-none px-3" value="cloud">Cloud</TabsTrigger>
        </TabsList>
        <TabsContent value="settings">
          <SettingsTab project={p} />
        </TabsContent>
        <TabsContent value="keys">
          <ApiKeysTab project={p} />
        </TabsContent>
        <TabsContent value="presets">
          <PresetsTab projectId={p.id} />
        </TabsContent>
        <TabsContent value="modes">
          <ModesTab project={p} />
        </TabsContent>
        <TabsContent value="webhooks">
          <WebhooksTab project={p} />
        </TabsContent>
        <TabsContent value="signing">
          <SigningKeysTab project={p} />
        </TabsContent>
        <TabsContent value="cloud">
          <CloudTab project={p} />
        </TabsContent>
      </Tabs>
    </>
  );
}
