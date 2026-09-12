// Login captcha: a stateless HMAC-signed cookie carrying a simple arithmetic
// answer — generated fresh on every GET /admin/login and consumed (cleared) on
// every POST /admin/login attempt regardless of outcome, so a bot has to reload
// the login page between attempts instead of replaying one solved challenge in a
// tight loop. No external service, no API key — consistent with this server's
// no-third-party-dependency admin panel.
package handler

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const captchaCookieName = "licadmin_captcha"
const captchaTTL = 3 * time.Minute

type captchaSigner struct {
	secret []byte
}

func newCaptchaSigner(secret string) *captchaSigner {
	return &captchaSigner{secret: []byte(secret)}
}

type captchaChallenge struct {
	Question string
	Answer   int
}

func newCaptchaChallenge() captchaChallenge {
	a, b := randDigit(), randDigit()
	return captchaChallenge{Question: fmt.Sprintf("%d + %d", a, b), Answer: a + b}
}

func randDigit() int {
	n, err := rand.Int(rand.Reader, big.NewInt(9))
	if err != nil {
		return 4 // crypto/rand failing here would mean the whole process is unhealthy; keep login usable
	}
	return int(n.Int64()) + 1
}

func (s *captchaSigner) sign(answer int) string {
	exp := time.Now().Add(captchaTTL).Unix()
	payload := strconv.Itoa(answer) + "." + strconv.FormatInt(exp, 10)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return payload + "." + sig
}

// verify checks the cookie's signature and expiry, then compares its embedded
// answer against the user-submitted one.
func (s *captchaSigner) verify(token, submitted string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	answerStr, expStr, sig := parts[0], parts[1], parts[2]
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(answerStr + "." + expStr))
	expectedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(sig), []byte(expectedSig)) != 1 {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	submitted = strings.TrimSpace(submitted)
	return subtle.ConstantTimeCompare([]byte(answerStr), []byte(submitted)) == 1
}

func (s *captchaSigner) setCookie(w http.ResponseWriter, answer int) {
	http.SetCookie(w, &http.Cookie{
		Name:     captchaCookieName,
		Value:    s.sign(answer),
		Path:     "/admin/login",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(captchaTTL.Seconds()),
	})
}

// clearCookie runs after every login attempt, success or failure, so a solved
// challenge can never be replayed — the next attempt must load a fresh page first.
func (s *captchaSigner) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     captchaCookieName,
		Value:    "",
		Path:     "/admin/login",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *captchaSigner) verifyRequest(r *http.Request) bool {
	cookie, err := r.Cookie(captchaCookieName)
	if err != nil {
		return false
	}
	return s.verify(cookie.Value, r.FormValue("captcha_answer"))
}
