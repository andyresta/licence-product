// Admin session handling — a stateless signed cookie (HMAC-SHA256 over
// "<admin_user_id>.<expiry-unix>"), not a server-side session store. Single-process,
// single-admin-scale tooling: this is simpler than adding a sessions table and
// survives a server restart without logging everyone out.
package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const sessionCookieName = "licadmin_session"
const sessionTTL = 12 * time.Hour

type sessionSigner struct {
	secret []byte
}

func newSessionSigner(secret string) *sessionSigner {
	return &sessionSigner{secret: []byte(secret)}
}

func (s *sessionSigner) sign(adminUserID string) string {
	exp := time.Now().Add(sessionTTL).Unix()
	payload := adminUserID + "." + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

func (s *sessionSigner) verify(token string) (adminUserID string, ok bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", false
	}
	adminUserID, expStr, sig := parts[0], parts[1], parts[2]
	payload := adminUserID + "." + expStr
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expectedSig)) != 1 {
		return "", false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return "", false
	}
	return adminUserID, true
}

func (s *sessionSigner) setCookie(w http.ResponseWriter, adminUserID string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    s.sign(adminUserID),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
}

func (s *sessionSigner) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *sessionSigner) adminFromRequest(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil {
		return "", false
	}
	return s.verify(cookie.Value)
}

type contextKey string

const adminUserIDKey contextKey = "admin_user_id"

func (s *sessionSigner) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		adminUserID, ok := s.adminFromRequest(r)
		if !ok {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), adminUserIDKey, adminUserID))
		next(w, r)
	}
}

func adminUserIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(adminUserIDKey).(string)
	return id
}
