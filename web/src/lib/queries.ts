import { QueryClient, queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import type { MessageKey } from "@/i18n/dictionaries";
import { t } from "@/i18n/store";

import { api, errorMessage, isApiError } from "./api";
import type {
  AgentInput,
  SetModerationRequest,
  CreateApiKeyRequest,
  CreateCloudConnectionRequest,
  CreateProjectRequest,
  CreateWebhookRequest,
  SavePresetVersionRequest,
  StorageProfileInput,
  StorageProfilePatch,
  UpdateProjectRequest,
} from "./types";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: false,
      retry: (count, err) => {
        if (isApiError(err) && err.status >= 400 && err.status < 500) return false;
        return count < 2;
      },
    },
    mutations: { retry: false },
  },
});

export const qk = {
  me: ["auth", "me"] as const,
  passkeys: ["auth", "passkeys"] as const,
  projects: ["projects"] as const,
  project: (id: string) => ["projects", id] as const,
  apiKeys: (projectId: string) => ["projects", projectId, "api-keys"] as const,
  presets: (projectId: string) => ["projects", projectId, "presets"] as const,
  modes: (projectId: string) => ["projects", projectId, "modes"] as const,
  storage: ["storage-profiles"] as const,
  system: ["system"] as const,
  audit: ["audit"] as const,
  jobs: ["jobs"] as const,
  assets: (projectId: string) => ["projects", projectId, "assets"] as const,
  asset: (projectId: string, assetId: string) => ["projects", projectId, "assets", assetId] as const,
  analytics: (projectId: string, days: number) => ["projects", projectId, "analytics", days] as const,
  webhooks: (projectId: string) => ["projects", projectId, "webhooks"] as const,
  deliveries: (projectId: string) => ["projects", projectId, "deliveries"] as const,
  signingKeys: (projectId: string) => ["projects", projectId, "signing-keys"] as const,
  cloud: (projectId: string) => ["projects", projectId, "cloud-connections"] as const,
  agents: ["agents"] as const,
  agentSettings: ["agents", "settings"] as const,
  approvals: ["agent-approvals"] as const,
  moderation: (projectId: string) => ["projects", projectId, "moderation"] as const,
};

export const meQuery = queryOptions({
  queryKey: qk.me,
  queryFn: () => api.auth.me(),
  staleTime: 5 * 60_000,
  retry: false,
});

export const projectsQuery = queryOptions({
  queryKey: qk.projects,
  queryFn: () => api.projects.list().then((r) => r.items),
});

export const projectQuery = (id: string) =>
  queryOptions({ queryKey: qk.project(id), queryFn: () => api.projects.get(id) });

export const apiKeysQuery = (projectId: string) =>
  queryOptions({ queryKey: qk.apiKeys(projectId), queryFn: () => api.apiKeys.list(projectId).then((r) => r.items) });

export const modesQuery = (projectId: string) =>
  queryOptions({
    queryKey: qk.modes(projectId),
    queryFn: () => api.projects.modes(projectId),
  });

export const presetsQuery = (projectId: string) =>
  queryOptions({
    queryKey: qk.presets(projectId),
    queryFn: () => api.projects.presets(projectId).then((r) => r.items),
    staleTime: 5 * 60_000,
  });

export const storageQuery = queryOptions({
  queryKey: qk.storage,
  queryFn: () => api.storage.list().then((r) => r.items),
});

export const systemQuery = queryOptions({
  queryKey: qk.system,
  queryFn: () => api.system.get(),
  refetchInterval: 30_000,
});

export const analyticsQuery = (projectId: string, days: number) =>
  queryOptions({ queryKey: qk.analytics(projectId, days), queryFn: () => api.projects.analytics(projectId, days) });

export const assetQuery = (projectId: string, assetId: string) =>
  queryOptions({ queryKey: qk.asset(projectId, assetId), queryFn: () => api.assets.get(projectId, assetId) });

export const webhooksQuery = (projectId: string) =>
  queryOptions({ queryKey: qk.webhooks(projectId), queryFn: () => api.webhooks.list(projectId).then((r) => r.items) });

export const signingKeysQuery = (projectId: string) =>
  queryOptions({
    queryKey: qk.signingKeys(projectId),
    queryFn: () => api.signingKeys.list(projectId).then((r) => r.items),
  });

export const cloudQuery = (projectId: string) =>
  queryOptions({ queryKey: qk.cloud(projectId), queryFn: () => api.cloud.list(projectId).then((r) => r.items) });

export const agentsQuery = queryOptions({
  queryKey: qk.agents,
  queryFn: () => api.agents.list().then((r) => r.items),
});

export const agentSettingsQuery = queryOptions({
  queryKey: qk.agentSettings,
  queryFn: () => api.agents.settings(),
  refetchInterval: 30_000,
});

export const approvalsQuery = queryOptions({
  queryKey: qk.approvals,
  queryFn: () => api.approvals.list({ limit: 200 }).then((r) => r.items),
  refetchInterval: 15_000,
});

function onErrorToast(titleKey: MessageKey) {
  return (e: unknown) => toast.error(t(titleKey), { description: errorMessage(e) });
}

// ---- Mutations --------------------------------------------------------------

export function useCreateProject() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateProjectRequest) => api.projects.create(body),
    onSuccess: (p) => {
      qc.setQueryData(qk.project(p.id), p);
      void qc.invalidateQueries({ queryKey: qk.projects, exact: true });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
  });
}

export function useUpdateProject(id: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdateProjectRequest) => api.projects.update(id, body),
    onSuccess: (p) => {
      qc.setQueryData(qk.project(id), p);
      void qc.invalidateQueries({ queryKey: qk.projects, exact: true });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
  });
}

export function useCreateApiKey(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateApiKeyRequest) => api.apiKeys.create(projectId, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.apiKeys(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
  });
}

export function useRevokeApiKey(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (keyId: string) => api.apiKeys.revoke(keyId),
    onSuccess: () => {
      toast.success(t("common.toast.keyRevoked"));
      void qc.invalidateQueries({ queryKey: qk.apiKeys(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("common.toast.keyRevokeFailed"),
  });
}

export function useSaveStorageProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id?: string; create?: StorageProfileInput; patch?: StorageProfilePatch }) =>
      v.id ? api.storage.update(v.id, v.patch ?? {}) : api.storage.create(v.create!),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.storage });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
  });
}

export function useDeleteStorageProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.storage.remove(id),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.storage });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
  });
}

export function useTestStorageProfile() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.storage.test(id),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: qk.storage });
    },
  });
}

export function useRetryJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.jobs.retry(id),
    onSuccess: () => {
      toast.success(t("common.toast.jobRetried"));
      void qc.invalidateQueries({ queryKey: qk.jobs });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("common.toast.jobRetryFailed"),
  });
}

export function useCancelJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.jobs.cancel(id),
    onSuccess: () => {
      toast.success(t("common.toast.jobCancelled"));
      void qc.invalidateQueries({ queryKey: qk.jobs });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("common.toast.jobCancelFailed"),
  });
}

export function useDeleteAsset(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (assetId: string) => api.assets.remove(projectId, assetId),
    onSuccess: () => {
      toast.success(t("common.toast.assetDeleted"));
      void qc.invalidateQueries({ queryKey: qk.assets(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("common.toast.assetDeleteFailed"),
  });
}

export function useSavePresetVersion(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { slug: string } & SavePresetVersionRequest) =>
      api.projects.savePresetVersion(projectId, v.slug, { params: v.params, description: v.description }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.presets(projectId) });
      void qc.invalidateQueries({ queryKey: qk.modes(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
  });
}

export function useResetPreset(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (slug: string) => api.projects.resetPreset(projectId, slug),
    onSuccess: (p) => {
      toast.success(t("common.toast.presetReset", { slug: p.slug }));
      void qc.invalidateQueries({ queryKey: qk.presets(projectId) });
      void qc.invalidateQueries({ queryKey: qk.modes(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("common.toast.presetResetFailed"),
  });
}

export function useCreateWebhook(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateWebhookRequest) => api.webhooks.create(projectId, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.webhooks(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
  });
}

export function useDeleteWebhook(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.webhooks.remove(id),
    onSuccess: () => {
      toast.success(t("common.toast.endpointDeleted"));
      void qc.invalidateQueries({ queryKey: qk.webhooks(projectId) });
      void qc.invalidateQueries({ queryKey: qk.deliveries(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("common.toast.endpointDeleteFailed"),
  });
}

export function useTestWebhook(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.webhooks.test(id),
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: qk.deliveries(projectId) });
    },
  });
}

export function useRetryDelivery(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.webhooks.retryDelivery(id),
    onSuccess: () => {
      toast.success(t("common.toast.deliveryRetried"));
      void qc.invalidateQueries({ queryKey: qk.deliveries(projectId) });
    },
    onError: onErrorToast("common.toast.deliveryRetryFailed"),
  });
}

export function useRotateSigningKey(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.signingKeys.rotate(projectId),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.signingKeys(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
  });
}

export function usePublishSigningKeys(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: () => api.signingKeys.publish(),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.signingKeys(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
  });
}

export function useRetireSigningKey(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (kid: string) => api.signingKeys.retire(kid),
    onSuccess: () => {
      toast.success(t("common.toast.signingKeyRetired"));
      void qc.invalidateQueries({ queryKey: qk.signingKeys(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("common.toast.signingKeyRetireFailed"),
  });
}

export function useCreateCloudConnection(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateCloudConnectionRequest) => api.cloud.create(projectId, body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.cloud(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
  });
}

export function useRevokeCloudConnection(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.cloud.revoke(id),
    onSuccess: () => {
      toast.success(t("common.toast.connectionRevoked"));
      void qc.invalidateQueries({ queryKey: qk.cloud(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("common.toast.connectionRevokeFailed"),
  });
}

// ---- AI agents --------------------------------------------------------------

function invalidateAgents(qc: ReturnType<typeof useQueryClient>) {
  void qc.invalidateQueries({ queryKey: qk.agents });
  void qc.invalidateQueries({ queryKey: qk.audit });
}

export function useCreateAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: AgentInput) => api.agents.create(body),
    onSuccess: () => invalidateAgents(qc),
  });
}

export function useUpdateAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; body: AgentInput }) => api.agents.update(v.id, v.body),
    onSuccess: () => {
      toast.success(t("agents.toast.saved"));
      invalidateAgents(qc);
    },
  });
}

export function useRotateAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.agents.rotate(id),
    onSuccess: () => invalidateAgents(qc),
    onError: onErrorToast("agents.toast.rotateFailed"),
  });
}

export function useRevokeAgent() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.agents.revoke(id),
    onSuccess: () => {
      toast.success(t("agents.toast.revoked"));
      invalidateAgents(qc);
      void qc.invalidateQueries({ queryKey: qk.approvals });
    },
    onError: onErrorToast("agents.toast.revokeFailed"),
  });
}

export function useSetAgentsEnabled() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (enabled: boolean) => api.agents.setEnabled(enabled),
    onSuccess: (s) => {
      qc.setQueryData(qk.agentSettings, s);
      toast.success(s.enabled ? t("agents.toast.enabled") : t("agents.toast.disabled"));
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("agents.toast.switchFailed"),
  });
}

export function useDecideApproval() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { id: string; approve: boolean; note?: string }) =>
      v.approve ? api.approvals.approve(v.id, v.note) : api.approvals.reject(v.id, v.note),
    onSuccess: (a) => {
      if (a.status === "executed") toast.success(t("agents.toast.approved"));
      else if (a.status === "failed") toast.error(t("agents.toast.approvedFailed", { status: a.result_status ?? 0 }));
      else toast.success(t("agents.toast.rejected"));
      void qc.invalidateQueries({ queryKey: qk.approvals });
      void qc.invalidateQueries({ queryKey: qk.agentSettings });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("agents.toast.decideFailed"),
  });
}

export function useSetModeration(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { assetId: string; body: SetModerationRequest }) => api.moderation.set(projectId, v.assetId, v.body),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.moderation(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("moderation.toast.failed"),
  });
}
