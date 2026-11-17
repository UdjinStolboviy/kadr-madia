import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";

import { projectsQuery } from "./queries";

/**
 * Resolves the active project for project-scoped pages. `value` is the project id from the URL
 * (may be undefined); once projects load the first one is selected automatically.
 */
export function useActiveProject(value: string | undefined, onChange: (id: string) => void) {
  const projects = useQuery(projectsQuery);
  const list = projects.data ?? [];
  const active = list.find((p) => p.id === value) ?? list[0];
  const activeId = active?.id;
  useEffect(() => {
    if (activeId && activeId !== value) onChange(activeId);
  }, [activeId, value, onChange]);
  return { projects, list, active };
}
