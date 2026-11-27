package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"

	"github.com/google/uuid"

	"github.com/ombg/portfolio-tracker/services/api/internal/auth"
)

type ctxKey int

const userIDKey ctxKey = iota

// UserID returns the signed-in user set by requireUser.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(userIDKey).(uuid.UUID)
	return id, ok
}

type authHandlers struct {
	store  *auth.Store
	signer *auth.Signer
	secure bool
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *authHandlers) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || req.Email == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "email and password are required")
		return
	}
	sess, err := a.store.Login(r.Context(), req.Email, req.Password)
	if errors.Is(err, auth.ErrInvalidCredentials) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "email or password is incorrect")
		return
	}
	if err != nil {
		slog.Error("login failed", "error", err) // never log the email or password
		writeError(w, http.StatusInternalServerError, "internal", "login is unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CookieName,
		Value:    a.signer.Sign(sess.ID),
		Path:     "/",
		Expires:  sess.ExpiresAt,
		HttpOnly: true,
		Secure:   a.secure,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *authHandlers) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		if id, err := a.signer.Parse(c.Value); err == nil {
			if err := a.store.Revoke(r.Context(), id); err != nil {
				slog.Error("revoke session failed", "error", err)
				writeError(w, http.StatusInternalServerError, "internal", "logout failed")
				return
			}
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.secure, SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *authHandlers) me(w http.ResponseWriter, r *http.Request) {
	userID, _ := UserID(r.Context())
	p, err := a.store.Profile(r.Context(), userID)
	if errors.Is(err, auth.ErrNoSession) {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in required")
		return
	}
	if err != nil {
		slog.Error("load profile failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "profile is unavailable")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// requireUser resolves the session cookie and puts the user id in the request context.
func (a *authHandlers) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(auth.CookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in required")
			return
		}
		sessionID, err := a.signer.Parse(c.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "sign in required")
			return
		}
		userID, err := a.store.UserID(r.Context(), sessionID)
		if errors.Is(err, auth.ErrNoSession) {
			writeError(w, http.StatusUnauthorized, "unauthenticated", "session expired, sign in again")
			return
		}
		if err != nil {
			slog.Error("resolve session failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal", "session check failed")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey, userID)))
	})
}

// requireJSONForWrites rejects state-changing requests that are not application/json.
// Browsers cannot send that content type cross-site without a CORS preflight, which this
// API never grants, so together with SameSite=Lax cookies it blocks CSRF (D-33).
// File uploads are multipart/form-data, which a cross-site form can send, so they must also
// carry UploadHeader, which no form can set and no cross-site fetch can send unpreflighted (D-36).
func requireJSONForWrites(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
			upload := r.Method == http.MethodPost && r.URL.Path == "/api/imports" && mt == "multipart/form-data" &&
				r.Header.Get(UploadHeader) == UploadHeaderValue
			if err != nil || (mt != "application/json" && !upload) {
				writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "requests must be application/json")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
