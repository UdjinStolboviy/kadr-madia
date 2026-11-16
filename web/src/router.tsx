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

export type ProjectTab = "settings" | "keys" | "presets";
const TABS: ProjectTab[] = ["settings", "keys", "presets"];

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

const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([overviewRoute, projectsRoute, projectDetailRoute, storageRoute, systemRoute, auditRoute]),
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
