import { useState, type FormEvent } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ApiError, api } from "./api";

export function LoginPage() {
  const qc = useQueryClient();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const login = useMutation({
    mutationFn: () => api.login(email, password),
    onSuccess: () => qc.invalidateQueries({ queryKey: ["me"] }),
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
