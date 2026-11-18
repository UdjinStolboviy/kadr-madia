import {
  createRootRouteWithContext,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Outlet,
  redirect,
} from "@tanstack/react-router";
import type { QueryClient } from "@tanstack/react-query";

import { AppShell } from "@/components/app-shell";
import { NotFound, RouteError } from "@/components/states";
import { isApiError } from "@/lib/api";
import { meQuery, queryClient } from "@/lib/queries";

interface RouterContext {
  queryClient: QueryClient;
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: () => <Outlet />,
  notFoundComponent: NotFound,
});

function safeRedirect(v: unknown): string | undefined {
  return typeof v === "string" && v.startsWith("/") && !v.startsWith("//") ? v : undefined;
}

export const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  validateSearch: (s: Record<string, unknown>): { redirect?: string } => ({ redirect: safeRedirect(s.redirect) }),
  beforeLoad: async ({ context, search }) => {
    // Already signed in → skip the login form.
    const me = await context.queryClient.fetchQuery(meQuery).catch(() => null);
    if (me) throw redirect({ to: search.redirect ?? "/" });
  },
  component: lazyRouteComponent(() => import("@/routes/login")),
});

const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  beforeLoad: async ({ context, location }) => {
    try {
      const session = await context.queryClient.ensureQueryData(meQuery);
      return { session };
    } catch (e) {
      if (isApiError(e) && e.status !== 401) throw e;
      throw redirect({
        to: "/login",
        search: { redirect: location.pathname === "/" ? undefined : location.href },
      });
    }
  },
  component: AppShell,
  errorComponent: RouteError,
});

const overviewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  component: lazyRouteComponent(() => import("@/routes/overview")),
});

const projectsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/projects",
  validateSearch: (s: Record<string, unknown>): { new?: boolean } => (s.new === true || s.new === "1" ? { new: true } : {}),
  component: lazyRouteComponent(() => import("@/routes/projects")),
});

export type ProjectTab = "settings" | "keys" | "presets" | "modes" | "webhooks" | "signing" | "cloud";
const TABS: ProjectTab[] = ["settings", "keys", "presets", "modes", "webhooks", "signing", "cloud"];

export const projectDetailRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/projects/$projectId",
  validateSearch: (s: Record<string, unknown>): { tab?: ProjectTab } =>
    TABS.includes(s.tab as ProjectTab) ? { tab: s.tab as ProjectTab } : {},
  component: lazyRouteComponent(() => import("@/routes/project-detail")),
});

const storageRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/storage",
  validateSearch: (s: Record<string, unknown>): { new?: boolean } => (s.new === true || s.new === "1" ? { new: true } : {}),
  component: lazyRouteComponent(() => import("@/routes/storage")),
});

const systemRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/system",
  component: lazyRouteComponent(() => import("@/routes/system")),
});

const auditRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/audit",
  component: lazyRouteComponent(() => import("@/routes/audit")),
});

/** Search schema shared by the pages that operate on one project. */
function projectSearch(s: Record<string, unknown>): { project?: string } {
  return typeof s.project === "string" && s.project ? { project: s.project } : {};
}

const jobsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/jobs",
  component: lazyRouteComponent(() => import("@/routes/jobs")),
});

const assetsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/assets",
  validateSearch: projectSearch,
  component: lazyRouteComponent(() => import("@/routes/assets")),
});

const analyticsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/analytics",
  validateSearch: (s: Record<string, unknown>): { project?: string; days?: 7 | 30 | 90 } => {
    const days = Number(s.days);
    return { ...projectSearch(s), ...(days === 7 || days === 90 ? { days } : {}) };
  },
  component: lazyRouteComponent(() => import("@/routes/analytics")),
});

const playgroundRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/playground",
  validateSearch: (s: Record<string, unknown>): { project?: string; preset?: string } => ({
    ...projectSearch(s),
    ...(typeof s.preset === "string" && s.preset ? { preset: s.preset } : {}),
  }),
  component: lazyRouteComponent(() => import("@/routes/playground")),
});

export type AgentsTab = "agents" | "approvals" | "activity";
const AGENT_TABS: AgentsTab[] = ["agents", "approvals", "activity"];

const agentsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/agents",
  validateSearch: (s: Record<string, unknown>): { tab?: AgentsTab } =>
    AGENT_TABS.includes(s.tab as AgentsTab) ? { tab: s.tab as AgentsTab } : {},
  component: lazyRouteComponent(() => import("@/routes/agents")),
});

export type ModerationFilter = "unreviewed" | "flagged" | "approved" | "rejected" | "all";
const MODERATION_FILTERS: ModerationFilter[] = ["unreviewed", "flagged", "approved", "rejected", "all"];

const moderationRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/moderation",
  validateSearch: (s: Record<string, unknown>): { project?: string; status?: ModerationFilter } => ({
    ...projectSearch(s),
    ...(MODERATION_FILTERS.includes(s.status as ModerationFilter) ? { status: s.status as ModerationFilter } : {}),
  }),
  component: lazyRouteComponent(() => import("@/routes/moderation")),
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([
    overviewRoute,
    projectsRoute,
    projectDetailRoute,
    assetsRoute,
    jobsRoute,
    analyticsRoute,
    playgroundRoute,
    storageRoute,
    systemRoute,
    auditRoute,
    agentsRoute,
    moderationRoute,
  ]),
]);

export const router = createRouter({
  routeTree,
  context: { queryClient },
  defaultPreload: "intent",
  defaultPreloadStaleTime: 0,
  defaultErrorComponent: RouteError,
  defaultPendingMinMs: 0,
  scrollRestoration: true,
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}
