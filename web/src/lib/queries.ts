import { QueryClient, queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api, errorMessage, isApiError } from "./api";
import type {
  CreateApiKeyRequest,
  CreateProjectRequest,
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
  projects: ["projects"] as const,
  project: (id: string) => ["projects", id] as const,
  apiKeys: (projectId: string) => ["projects", projectId, "api-keys"] as const,
  presets: (projectId: string) => ["projects", projectId, "presets"] as const,
  storage: ["storage-profiles"] as const,
  system: ["system"] as const,
  audit: ["audit"] as const,
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
