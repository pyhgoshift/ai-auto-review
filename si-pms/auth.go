package main

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	pbkdf2Iter     = 210000
	sessionCookie  = "pms_session"
	sessionTTL     = 12 * time.Hour
	maxFailedLogin = 5
	lockDuration   = 10 * time.Minute
)

type User struct {
	ID         int64
	Login      string
	Name       string
	Role       string // admin | viewer
	MustChange bool
	CSRF       string
}

func (u *User) IsAdmin() bool { return u.Role == "admin" }

func randToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func hashPassword(pw string) string {
	salt := make([]byte, 16)
	rand.Read(salt)
	key, err := pbkdf2.Key(sha256.New, pw, salt, pbkdf2Iter, 32)
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", pbkdf2Iter,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func checkPassword(stored, pw string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	var iter int
	fmt.Sscanf(parts[1], "%d", &iter)
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[2])
	want, err2 := base64.RawStdEncoding.DecodeString(parts[3])
	if err1 != nil || err2 != nil || iter <= 0 {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(want))
	return err == nil && hmac.Equal(got, want)
}

func validPassword(pw string) error {
	if len([]rune(pw)) < 10 {
		return fmt.Errorf("비밀번호는 10자 이상이어야 합니다")
	}
	kinds := 0
	for _, set := range []string{"abcdefghijklmnopqrstuvwxyz", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "0123456789"} {
		if strings.ContainsAny(pw, set) {
			kinds++
		}
	}
	if strings.IndexFunc(pw, func(r rune) bool {
		return !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789", r)
	}) >= 0 {
		kinds++
	}
	if kinds < 3 {
		return fmt.Errorf("영문 대·소문자, 숫자, 특수문자 중 3종 이상을 조합해 주세요")
	}
	return nil
}

// login은 성공 시 세션 토큰을 돌려준다. 연속 실패하면 일정 시간 잠근다.
func (a *App) login(login, pw string) (string, error) {
	var id int64
	var hash, lockedUntil string
	var failed int
	err := a.db.QueryRow(`SELECT id, pw_hash, failed, locked_until FROM users WHERE login = ?`, login).Scan(&id, &hash, &failed, &lockedUntil)
	if err != nil {
		checkPassword("pbkdf2-sha256$1$AAAA$AAAA", pw) // 존재하지 않는 계정도 응답 시간을 비슷하게
		return "", fmt.Errorf("아이디 또는 비밀번호가 올바르지 않습니다")
	}
	if lockedUntil != "" && lockedUntil > nowTS() {
		return "", fmt.Errorf("로그인 실패가 반복되어 %s까지 잠겼습니다", lockedUntil)
	}
	if !checkPassword(hash, pw) {
		failed++
		lock := ""
		if failed >= maxFailedLogin {
			lock = time.Now().Add(lockDuration).Format("2006-01-02 15:04:05")
			failed = 0
		}
		a.db.Exec(`UPDATE users SET failed = ?, locked_until = ? WHERE id = ?`, failed, lock, id)
		return "", fmt.Errorf("아이디 또는 비밀번호가 올바르지 않습니다")
	}
	a.db.Exec(`UPDATE users SET failed = 0, locked_until = '' WHERE id = ?`, id)
	token := randToken(32)
	_, err = a.db.Exec(`INSERT INTO sessions(token_hash, user_id, csrf, expires_at) VALUES(?, ?, ?, ?)`,
		sha256Hex(token), id, randToken(24), time.Now().Add(sessionTTL).Format("2006-01-02 15:04:05"))
	a.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, nowTS())
	return token, err
}

func (a *App) currentUser(r *http.Request) *User {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	u := &User{}
	err = a.db.QueryRow(`SELECT u.id, u.login, u.name, u.role, u.must_change, s.csrf
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ? AND s.expires_at > ?`, sha256Hex(c.Value), nowTS()).
		Scan(&u.ID, &u.Login, &u.Name, &u.Role, &u.MustChange, &u.CSRF)
	if err != nil {
		return nil
	}
	return u
}

type ctxKey struct{}

func userFrom(r *http.Request) *User {
	u, _ := r.Context().Value(ctxKey{}).(*User)
	return u
}

// requireUser는 로그인과 CSRF를 검사한다. viewer는 조회(GET)만 할 수 있다.
func (a *App) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := a.currentUser(r)
		if u == nil {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		if r.Method == http.MethodPost {
			if !hmac.Equal([]byte(r.FormValue("csrf")), []byte(u.CSRF)) {
				http.Error(w, "요청이 만료되었습니다. 화면을 새로고침한 뒤 다시 시도해 주세요.", http.StatusForbidden)
				return
			}
			if !u.IsAdmin() && r.URL.Path != "/password" {
				http.Error(w, "조회 전용 계정입니다.", http.StatusForbidden)
				return
			}
		}
		if u.MustChange && r.URL.Path != "/password" && r.URL.Path != "/logout" {
			http.Redirect(w, r, "/password", http.StatusSeeOther)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

func (a *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return a.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !userFrom(r).IsAdmin() {
			http.Error(w, "관리자만 사용할 수 있습니다.", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

func (a *App) setSessionCookie(w http.ResponseWriter, token string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: a.secureCookie, SameSite: http.SameSiteLaxMode,
	})
}

func (a *App) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "login", "로그인", map[string]any{"Next": r.URL.Query().Get("next")})
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	login := strings.TrimSpace(r.FormValue("login"))
	token, err := a.login(login, r.FormValue("password"))
	if err != nil {
		a.audit(login, clientIP(r), "login_fail", "", "")
		a.renderErr(w, r, "login", "로그인", err.Error(), map[string]any{"Next": r.FormValue("next")})
		return
	}
	a.audit(login, clientIP(r), "login", "", "")
	a.setSessionCookie(w, token, int(sessionTTL.Seconds()))
	next := r.FormValue("next")
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		a.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, sha256Hex(c.Value))
	}
	a.setSessionCookie(w, "", -1)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (a *App) handlePasswordPage(w http.ResponseWriter, r *http.Request) {
	a.render(w, r, "password", "비밀번호 변경", nil)
}

func (a *App) handlePassword(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	var hash string
	a.db.QueryRow(`SELECT pw_hash FROM users WHERE id = ?`, u.ID).Scan(&hash)
	if !checkPassword(hash, r.FormValue("current")) {
		a.renderErr(w, r, "password", "비밀번호 변경", "현재 비밀번호가 올바르지 않습니다", nil)
		return
	}
	pw := r.FormValue("new")
	if pw != r.FormValue("confirm") {
		a.renderErr(w, r, "password", "비밀번호 변경", "새 비밀번호 확인이 일치하지 않습니다", nil)
		return
	}
	if err := validPassword(pw); err != nil {
		a.renderErr(w, r, "password", "비밀번호 변경", err.Error(), nil)
		return
	}
	a.db.Exec(`UPDATE users SET pw_hash = ?, must_change = 0 WHERE id = ?`, hashPassword(pw), u.ID)
	a.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND csrf != ?`, u.ID, u.CSRF)
	a.audit(u.Login, clientIP(r), "password_change", "", "")
	redirectMsg(w, r, "/", "비밀번호를 변경했습니다")
}

// ensureAdmin은 사용자가 한 명도 없으면 초기 관리자 계정을 만들고 임시 비밀번호를 돌려준다.
func (a *App) ensureAdmin() (string, error) {
	var n int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil || n > 0 {
		return "", err
	}
	pw := randToken(12)
	_, err := a.db.Exec(`INSERT INTO users(login, name, role, pw_hash, must_change, created_at) VALUES('admin', '관리자', 'admin', ?, 1, ?)`,
		hashPassword(pw), nowTS())
	return pw, err
}
