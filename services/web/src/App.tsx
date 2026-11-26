import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ApiError, api } from "./api";
import { LoginPage } from "./LoginPage";
import { AccountsPage } from "./AccountsPage";

export function App() {
  const qc = useQueryClient();
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
        <span className="spacer" />
        <span>{me.data.display_name}</span>
        <button type="button" onClick={() => logout.mutate()} disabled={logout.isPending}>
          Sign out
        </button>
      </header>
      <main>
        <AccountsPage />
      </main>
    </>
  );
}
