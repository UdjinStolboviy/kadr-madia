import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";

import { LocaleBoundary } from "@/i18n/react";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";
import { setUnauthorizedHandler } from "@/lib/api";
import { queryClient } from "@/lib/queries";
import { ThemeProvider } from "@/lib/theme";
import { router } from "@/router";

import "./index.css";

// Session expired mid-use → drop cached data and go to the login screen.
setUnauthorizedHandler(() => {
  const loc = router.state.location;
  if (loc.pathname === "/login") return;
  queryClient.clear();
  void router.navigate({ to: "/login", search: { redirect: loc.href }, replace: true });
});

async function bootstrap() {
  // Statically replaced by Vite: in a production build without VITE_MOCK this
  // branch is dead code and the mock module is never bundled.
  if (import.meta.env.VITE_MOCK === "1") {
    const { installMockApi } = await import("./mocks/server");
    installMockApi();
  }

  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <ThemeProvider>
        <QueryClientProvider client={queryClient}>
          <TooltipProvider delayDuration={300}>
            <LocaleBoundary>
              <RouterProvider router={router} />
              <Toaster position="bottom-right" richColors closeButton />
            </LocaleBoundary>
          </TooltipProvider>
        </QueryClientProvider>
      </ThemeProvider>
    </StrictMode>,
  );
}

void bootstrap();
