import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, Outlet, useNavigate, useRouterState } from "@tanstack/react-router";
import {
  Activity,
  BarChart3,
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
  ShieldCheck,
  Sun,
  Webhook,
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
import { api } from "@/lib/api";
import { meQuery, systemQuery } from "@/lib/queries";
import { useTheme, type ThemePref } from "@/lib/theme";
import { cn } from "@/lib/utils";

interface NavItem {
  to: "/" | "/projects" | "/storage" | "/system" | "/audit";
  label: string;
  icon: LucideIcon;
  exact?: boolean;
}

const NAV: NavItem[] = [
  { to: "/", label: "Overview", icon: LayoutDashboard, exact: true },
  { to: "/projects", label: "Projects", icon: FolderKanban },
  { to: "/storage", label: "Storage", icon: HardDrive },
  { to: "/system", label: "System", icon: Activity },
  { to: "/audit", label: "Audit log", icon: ScrollText },
];

const SOON: { label: string; icon: LucideIcon; milestone: string }[] = [
  { label: "Assets", icon: Images, milestone: "M4" },
  { label: "Jobs", icon: ListChecks, milestone: "M2" },
  { label: "Webhooks", icon: Webhook, milestone: "M1" },
  { label: "Analytics", icon: BarChart3, milestone: "M4" },
  { label: "Playground", icon: FlaskConical, milestone: "M4" },
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
  return (
    <nav className="flex flex-1 flex-col gap-6 overflow-y-auto px-3 py-4" aria-label="Main">
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
              {item.label}
            </Link>
          </li>
        ))}
      </ul>
      <div>
        <p className="px-2.5 pb-1.5 text-[11px] font-medium tracking-wider text-muted-foreground/70 uppercase">
          Coming soon
        </p>
        <ul className="grid gap-0.5">
          {SOON.map((item) => (
            <li key={item.label}>
              <span
                aria-disabled="true"
                className="flex h-8 cursor-not-allowed items-center gap-2.5 rounded-md px-2.5 text-sm text-muted-foreground/60 select-none"
              >
                <item.icon className="size-4" />
                <span className="flex-1">{item.label}</span>
                <span className="rounded border px-1 font-mono text-[10px] leading-4 text-muted-foreground/70">
                  {item.milestone}
                </span>
              </span>
            </li>
          ))}
        </ul>
      </div>
    </nav>
  );
}

function SidebarFooter() {
  const { data } = useQuery(systemQuery);
  return (
    <div className="border-t px-5 py-3 text-xs text-muted-foreground">
      {data ? (
        <span className="font-mono">
          {data.version}
          {data.dev && <span className="ml-2 rounded bg-warning/15 px-1 text-warning">dev</span>}
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
    toast.success("Signed out");
    void navigate({ to: "/login", replace: true });
  }

  return (
    <div className="flex items-center gap-1">
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon-sm" aria-label="Theme">
            <ThemeIcon theme={theme} />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end" className="w-36">
          <DropdownMenuRadioGroup value={theme} onValueChange={(v) => setTheme(v as ThemePref)}>
            <DropdownMenuRadioItem value="light">
              <Sun /> Light
            </DropdownMenuRadioItem>
            <DropdownMenuRadioItem value="dark">
              <Moon /> Dark
            </DropdownMenuRadioItem>
            <DropdownMenuRadioItem value="system">
              <Monitor /> System
            </DropdownMenuRadioItem>
          </DropdownMenuRadioGroup>
        </DropdownMenuContent>
      </DropdownMenu>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" className="h-8 gap-2 px-1.5 sm:px-2" aria-label="Account menu">
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
              <ShieldCheck /> Security
              {data && !data.admin.totp_enabled && (
                <span className="ml-auto text-[11px] text-warning">2FA off</span>
              )}
            </Link>
          </DropdownMenuItem>
          <DropdownMenuSeparator />
          <DropdownMenuItem onSelect={() => void logout()}>
            <LogOut /> Sign out
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
  const [mobileOpen, setMobileOpen] = useState(false);

  return (
    <div className="flex min-h-svh">
      <a
        href="#main"
        className="sr-only z-50 rounded-md bg-background px-3 py-2 text-sm focus:not-sr-only focus:fixed focus:top-2 focus:left-2"
      >
        Skip to content
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
          <SheetTitle className="sr-only">Navigation</SheetTitle>
          <SheetDescription className="sr-only">Main navigation</SheetDescription>
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
            aria-label="Open navigation"
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
