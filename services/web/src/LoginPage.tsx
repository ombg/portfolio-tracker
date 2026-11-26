import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ApiError, api } from "./api";

// Login succeeded but the next request had no session: the browser dropped the cookie (D-34).
class SessionNotKept extends Error {
  constructor() {
    super(
      "Your password was accepted, but the browser did not keep the session cookie, so you are not signed in. " +
        "If the app is served over plain HTTP, the server needs COOKIE_SECURE=false.",
    );
  }
}

export function LoginPage() {
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const login = useMutation({
    mutationFn: async () => {
      await api.login(email, password);
      // Confirm the browser kept the session cookie; otherwise the app would silently show this page again.
      try {
        return await api.me();
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) throw new SessionNotKept();
        throw e;
      }
    },
    onSuccess: (profile) => qc.setQueryData(["me"], profile),
  });

  function submit(e: FormEvent) {
    e.preventDefault();
    login.mutate();
  }

  const error =
    login.error instanceof ApiError && login.error.code === "invalid_credentials"
      ? "Email or password is incorrect."
      : login.error?.message;

  return (
    <main className="login">
      <h1>Sign in</h1>
      <form onSubmit={submit}>
        <label>
          Email
          <input type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.target.value)} />
        </label>
        <label>
          Password
          <input
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {error && (
          <p className="error" role="alert">
            {error}
          </p>
        )}
        <button type="submit" disabled={login.isPending}>
          Sign in
        </button>
      </form>
    </main>
  );
}
