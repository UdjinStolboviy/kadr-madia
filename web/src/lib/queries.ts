import { QueryClient, queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api, errorMessage, isApiError } from "./api";
import type {
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

function onErrorToast(title: string) {
  return (e: unknown) => toast.error(title, { description: errorMessage(e) });
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
      toast.success("API key revoked");
      void qc.invalidateQueries({ queryKey: qk.apiKeys(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("Couldn't revoke key"),
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
      toast.success("Job re-queued");
      void qc.invalidateQueries({ queryKey: qk.jobs });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("Couldn't retry job"),
  });
}

export function useCancelJob() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api.jobs.cancel(id),
    onSuccess: () => {
      toast.success("Job cancelled");
      void qc.invalidateQueries({ queryKey: qk.jobs });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("Couldn't cancel job"),
  });
}

export function useDeleteAsset(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (assetId: string) => api.assets.remove(projectId, assetId),
    onSuccess: () => {
      toast.success("Asset deleted");
      void qc.invalidateQueries({ queryKey: qk.assets(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("Couldn't delete asset"),
  });
}

export function useSavePresetVersion(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (v: { slug: string } & SavePresetVersionRequest) =>
      api.projects.savePresetVersion(projectId, v.slug, { params: v.params, description: v.description }),
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: qk.presets(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
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
      toast.success("Webhook endpoint deleted");
      void qc.invalidateQueries({ queryKey: qk.webhooks(projectId) });
      void qc.invalidateQueries({ queryKey: qk.deliveries(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("Couldn't delete endpoint"),
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
      toast.success("Delivery re-queued");
      void qc.invalidateQueries({ queryKey: qk.deliveries(projectId) });
    },
    onError: onErrorToast("Couldn't retry delivery"),
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

export function useRetireSigningKey(projectId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (kid: string) => api.signingKeys.retire(kid),
    onSuccess: () => {
      toast.success("Signing key retired");
      void qc.invalidateQueries({ queryKey: qk.signingKeys(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("Couldn't retire key"),
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
      toast.success("Connection revoked");
      void qc.invalidateQueries({ queryKey: qk.cloud(projectId) });
      void qc.invalidateQueries({ queryKey: qk.audit });
    },
    onError: onErrorToast("Couldn't revoke connection"),
  });
}
