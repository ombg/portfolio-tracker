import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api } from "./api";
import { LoginPage } from "./LoginPage";
import { AccountsPage } from "./AccountsPage";
import { ImportsPage } from "./ImportsPage";
import { ImportPreview } from "./ImportPreview";
import { HoldingsPage } from "./HoldingsPage";
import { SecurityPage } from "./SecurityPage";
import { MarketPage } from "./MarketPage";
import { href, useRoute } from "./route";

export function App() {
  const qc = useQueryClient();
  const route = useRoute();
  const me = useQuery({ queryKey: ["me"], queryFn: api.me });
  const logout = useMutation({
    mutationFn: api.logout,
    onSuccess: () => qc.clear(),
  });

  if (me.isPending) return <p className="status">Loading…</p>;
  if (me.error) {
    if (me.error instanceof ApiError && me.error.status === 401) return <LoginPage />;
    // Not a login problem: say so instead of showing an empty app (NFR-4).
    return (
      <p className="status error" role="alert">
        The server is not reachable: {me.error.message}
      </p>
    );
  }

  return (
    <>
      <header className="topbar">
        <strong>Portfolio Tracker</strong>
        <nav aria-label="Main">
          <a href={href({ page: "accounts" })} aria-current={route.page === "accounts" ? "page" : undefined}>
            Accounts
          </a>
          <a
            href={href({ page: "holdings" })}
            aria-current={route.page === "holdings" || route.page === "security" ? "page" : undefined}
          >
            Holdings
          </a>
          <a href={href({ page: "market" })} aria-current={route.page === "market" ? "page" : undefined}>
            Market data
          </a>
          <a
            href={href({ page: "imports" })}
            aria-current={route.page === "imports" || route.page === "import" ? "page" : undefined}
          >
            Imports
          </a>
        </nav>
        <span className="spacer" />
        <span>{me.data.display_name}</span>
        <button type="button" onClick={() => logout.mutate()} disabled={logout.isPending}>
          Sign out
        </button>
      </header>
      <main>
        {route.page === "accounts" && <AccountsPage />}
        {route.page === "imports" && <ImportsPage />}
        {route.page === "import" && <ImportPreview key={route.id} id={route.id} />}
        {route.page === "holdings" && <HoldingsPage />}
        {route.page === "security" && <SecurityPage key={route.id} id={route.id} />}
        {route.page === "market" && <MarketPage />}
      </main>
    </>
  );
}
