import { useQuery } from "@tanstack/react-query";

import { meQuery } from "./queries";

/** Role of the signed-in admin: owners and admins may change things, viewers only read. */
export function useRole() {
  const { data } = useQuery(meQuery);
  const role = data?.admin.role ?? "viewer";
  return { role, canManage: role === "owner" || role === "admin", isOwner: role === "owner" };
}
