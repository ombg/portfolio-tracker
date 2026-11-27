import { useEffect, useState } from "react";

// Hash routes keep the SPA a static bundle behind Caddy: #/accounts, #/imports, #/imports/<id>.
export type Route = { page: "accounts" } | { page: "imports" } | { page: "import"; id: string };

export function parseRoute(hash: string): Route {
  const parts = hash.replace(/^#\/?/, "").split("/").filter(Boolean);
  if (parts[0] === "imports" && parts[1]) return { page: "import", id: parts[1] };
  if (parts[0] === "imports") return { page: "imports" };
  return { page: "accounts" };
}

export function href(route: Route): string {
  if (route.page === "import") return `#/imports/${route.id}`;
  return `#/${route.page}`;
}

export function navigate(route: Route) {
  window.location.hash = href(route);
}

export function useRoute(): Route {
  const [route, setRoute] = useState(() => parseRoute(window.location.hash));
  useEffect(() => {
    const onChange = () => setRoute(parseRoute(window.location.hash));
    window.addEventListener("hashchange", onChange);
    return () => window.removeEventListener("hashchange", onChange);
  }, []);
  return route;
}
