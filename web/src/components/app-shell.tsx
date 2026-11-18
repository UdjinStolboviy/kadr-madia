import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import {
  Activity,
  BarChart3,
  Bot,
  Boxes,
  FlaskConical,
  FolderKanban,
  HardDrive,
  Images,
  LayoutDashboard,
  ListChecks,
  LogOut,
  Menu,
  Monitor,
  Moon,
  ScrollText,
  ShieldAlert,
  ShieldCheck,
  Sun,
  type LucideIcon,
} from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { Sheet, SheetContent, SheetDescription, SheetTitle } from "@/components/ui/sheet";
import { LanguageSwitcher } from "@/components/language-switcher";
import { useT } from "@/i18n/react";
import type { MessageKey } from "@/i18n/dictionaries";
import { api } from "@/lib/api";
import { agentSettingsQuery, meQuery, systemQuery } from "@/lib/queries";
import { useTheme, type ThemePref } from "@/lib/theme";
import { cn } from "@/lib/utils";

interface NavItem {
  to:
    | "/"
    | "/projects"
    | "/assets"
    | "/moderation"
    | "/jobs"
    | "/analytics"
    | "/playground"
    | "/storage"
    | "/agents"
    | "/system"
    | "/audit";
  label: MessageKey;
  icon: LucideIcon;
  exact?: boolean;
}

const NAV: NavItem[] = [
  { to: "/", label: "shell.nav.overview", icon: LayoutDashboard, exact: true },
  { to: "/projects", label: "shell.nav.projects", icon: FolderKanban },
  { to: "/assets", label: "shell.nav.assets", icon: Images },
  { to: "/moderation", label: "shell.nav.moderation", icon: ShieldAlert },
  { to: "/jobs", label: "shell.nav.jobs", icon: ListChecks },
  { to: "/analytics", label: "shell.nav.analytics", icon: BarChart3 },
  { to: "/playground", label: "shell.nav.playground", icon: FlaskConical },
  { to: "/storage", label: "shell.nav.storage", icon: HardDrive },
  { to: "/agents", label: "shell.nav.agents", icon: Bot },
  { to: "/system", label: "shell.nav.system", icon: Activity },
  { to: "/audit", label: "shell.nav.audit", icon: ScrollText },
];

export function Logo({ className }: { className?: string }) {
  return (
    <div className={cn("flex items-center gap-2.5", className)}>
      <div className="flex size-7 items-center justify-center rounded-lg bg-brand text-white shadow-sm">
        <Boxes className="size-4" />
      </div>
      <span className="text-[15px] font-semibold tracking-tight">Kadr</span>
    </div>
  );
}

function NavLinks({ onNavigate }: { onNavigate?: () => void }) {
  const t = useT();
  const settings = useQuery({ ...agentSettingsQuery, retry: false });
  const pending = settings.data?.pending_approvals ?? 0;
  return (
    <nav className="flex flex-1 flex-col gap-6 overflow-y-auto px-3 py-4" aria-label={t("shell.mainNav")}>
      <ul className="grid gap-0.5">
        {NAV.map((item) => (
          <li key={item.to}>
            <Link
              to={item.to}
              onClick={onNavigate}
              activeOptions={{ exact: item.exact ?? false }}
              className="group flex h-8 items-center gap-2.5 rounded-md px-2.5 text-sm text-sidebar-foreground/80 transition-colors outline-none hover:bg-accent hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring/50 data-[status=active]:bg-accent data-[status=active]:font-medium data-[status=active]:text-foreground"
            >
              <item.icon className="size-4 text-muted-foreground group-data-[status=active]:text-foreground" />
              {t(item.label)}
              {item.to === "/agents" && pending > 0 && (
                <span
                  className="ml-auto rounded-full bg-warning px-1.5 text-[11px] font-semibold text-white tabular-nums"
                  title={t("shell.pendingApprovals", { count: pending })}
                  aria-label={t("shell.pendingApprovals", { count: pending })}
                >
                  {pending}
                </span>
              )}
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  );
}

function SidebarFooter() {
  const t = useT();
  const { data } = useQuery(systemQuery);
  return (
    <div className="border-t px-5 py-3 text-xs text-muted-foreground">
      {data ? (
        <span className="font-mono">
          {data.version}
          {data.dev && <span className="ml-2 rounded bg-warning/15 px-1 text-warning">{t("shell.dev")}</span>}
        </span>
      ) : (
        <span>&nbsp;</span>
      )}
    </div>
  );
}

function ThemeIcon({ theme }: { theme: ThemePref }) {
  if (theme === "light") return <Sun />;
  if (theme === "dark") return <Moon />;
  return <Monitor />;
}

function UserMenu() {
  const t = useT();
  const { data } = useQuery(meQuery);
  const { theme, setTheme } = useTheme();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const email = data?.admin.email ?? "";
  const initial = email.charAt(0).toUpperCase() || "?";

  async function logout() {
    try {
      await api.auth.logout();
    } catch {
      // Even if the server call fails, drop the local session.
    }
    qc.clear();
    toast.success(t("shell.signedOut"));
    void navigate({ to: "/login", replace: true });
  }

  return (
    <div className="flex items-center gap-1">
      <LanguageSwitcher />
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label={t("shell.theme")}>
            <ThemeIcon theme={theme} />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-36">
          <DropdownMenuRadioGroup value={theme} onValueChange={(v) => setTheme(v as ThemePref)}>
            <DropdownMenuRadioItem value="light">
              <Sun /> {t("shell.themeLight")}
            </DropdownMenuRadioItem>
            <DropdownMenuRadioItem value="dark">
              <Moon /> {t("shell.themeDark")}
            </DropdownMenuRadioItem>
            <DropdownMenuRadioItem value="system">
              <Monitor /> {t("shell.themeSystem")}
            </DropdownMenuRadioItem>
          </DropdownMenuRadioGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" className="h-8 gap-2 px-1.5 sm:px-2" aria-label={t("shell.accountMenu")}>
            <span className="flex size-6 items-center justify-center rounded-full bg-brand/15 text-xs font-semibold text-brand">
              {initial}
            </span>
            <span className="hidden max-w-[16rem] truncate text-sm font-normal sm:inline">{email}</span>
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-60">
          <DropdownMenuLabel className="font-normal">
            <div className="truncate text-sm font-medium">{email}</div>
            <div className="text-xs text-muted-foreground capitalize">{data?.admin.role}</div>
          </DropdownMenuLabel>
          <DropdownMenuSeparator />
          <DropdownMenuItem asChild>
            <Link to="/system" hash="security">
              <ShieldCheck /> {t("shell.security")}
              {data && !data.admin.totp_enabled && (
                <span className="ml-auto text-[11px] text-warning">{t("shell.twoFaOff")}</span>
              )}
            </Link>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => void logout()}>
            <LogOut /> {t("shell.signOut")}
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>
  );
}

function TopProgress() {
  const loading = useRouterState({ select: (s) => s.status === "pending" });
  return (
    <div
      aria-hidden
      className={cn(
        "pointer-events-none absolute inset-x-0 top-0 h-0.5 origin-left bg-brand transition-all duration-500",
        loading ? "scale-x-75 opacity-100" : "scale-x-100 opacity-0",
      )}
    />
  );
}

export function AppShell() {
  const t = useT();
  const [mobileOpen, setMobileOpen] = useState(false);

  return (
    <div className="flex min-h-svh">
      <a
        href="#main"
        className="sr-only z-50 rounded-md bg-background px-3 py-2 text-sm focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        {t("shell.skipToContent")}
      </a>
      <aside className="sticky top-0 hidden h-svh w-60 shrink-0 flex-col border-r bg-sidebar lg:flex">
        <div className="flex h-14 items-center px-5">
          <Link to="/" className="rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring/50">
            <Logo />
          </Link>
        </div>
        <NavLinks />
        <SidebarFooter />
      </aside>

      <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
        <SheetContent side="left" className="w-64 gap-0 bg-sidebar p-0">
          <SheetTitle className="sr-only">{t("shell.navigation")}</SheetTitle>
          <SheetDescription className="sr-only">{t("shell.mainNavDesc")}</SheetDescription>
          <div className="flex h-14 items-center px-5">
            <Logo />
          </div>
          <NavLinks onNavigate={() => setMobileOpen(false)} />
          <SidebarFooter />
        </SheetContent>
      </Sheet>

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex h-14 items-center gap-2 border-b bg-background/80 px-3 backdrop-blur supports-[backdrop-filter]:bg-background/70 sm:px-6">
          <TopProgress />
          <Button
            variant="ghost"
            size="icon-sm"
            className="lg:hidden"
            aria-label={t("shell.openNav")}
            onClick={() => setMobileOpen(true)}
          >
            <Menu />
          </Button>
          <Link to="/" className="lg:hidden">
            <Logo />
          </Link>
          <div className="flex-1" />
          <UserMenu />
        </header>
        <main id="main" className="mx-auto w-full max-w-6xl flex-1 px-4 py-6 sm:px-6 sm:py-8 lg:px-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
