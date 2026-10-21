package main

import (
	"database/sql"
	"embed"
	"html/template"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

//go:embed web/templates/*.html
var templateFS embed.FS

//go:embed web/static
var staticFS embed.FS

type App struct {
	db           *sql.DB
	dataDir      string
	pages        map[string]*template.Template
	secureCookie bool
}

type View struct {
	Title   string
	User    *User
	Msg     string
	Err     string
	Project string
	Path    string
	D       map[string]any
}

var funcs = template.FuncMap{
	"statusLabel":      func(s string) string { return statusLabel[s] },
	"docKindLabel":     func(s string) string { return docKindLabel[s] },
	"assetStatusLabel": func(s string) string { return assetStatusLabel[s] },
	"lines": func(s string) []string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				out = append(out, l)
			}
		}
		return out
	},
	"date":      func(s string) string { return firstN(s, 10) },
	"hasPrefix": strings.HasPrefix,
}

func (a *App) loadTemplates() {
	a.pages = map[string]*template.Template{}
	names, _ := fs.Glob(templateFS, "web/templates/*.html")
	for _, n := range names {
		base := strings.TrimSuffix(path.Base(n), ".html")
		if base == "layout" {
			continue
		}
		a.pages[base] = template.Must(template.New("layout.html").Funcs(funcs).
			ParseFS(templateFS, "web/templates/layout.html", n))
	}
}

func (a *App) view(r *http.Request, title string, d map[string]any) *View {
	return &View{
		Title:   title,
		User:    userFrom(r),
		Msg:     r.URL.Query().Get("msg"),
		Project: a.setting("project_name"),
		Path:    r.URL.Path,
		D:       d,
	}
}

func (a *App) render(w http.ResponseWriter, r *http.Request, page, title string, d map[string]any) {
	a.renderView(w, page, a.view(r, title, d), http.StatusOK)
}

func (a *App) renderErr(w http.ResponseWriter, r *http.Request, page, title, msg string, d map[string]any) {
	v := a.view(r, title, d)
	v.Err = msg
	a.renderView(w, page, v, http.StatusBadRequest)
}

func (a *App) renderView(w http.ResponseWriter, page string, v *View, status int) {
	t, ok := a.pages[page]
	if !ok {
		http.Error(w, "unknown page "+page, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.Execute(w, v); err != nil {
		log.Printf("render %s: %v", page, err)
	}
}

func (a *App) fail(w http.ResponseWriter, err error) {
	log.Printf("error: %v", err)
	http.Error(w, "처리 중 오류가 발생했습니다: "+err.Error(), http.StatusInternalServerError)
}

func redirectMsg(w http.ResponseWriter, r *http.Request, to, msg string) {
	if msg != "" {
		sep := "?"
		if strings.Contains(to, "?") {
			sep = "&"
		}
		to += sep + "msg=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, to, http.StatusSeeOther)
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func form(r *http.Request, key string) string { return strings.TrimSpace(r.FormValue(key)) }

// securityHeaders는 모든 응답에 기본 보안 헤더를 붙이고, 다른 출처에서 온 POST를 막는다.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
		h.Set("Cache-Control", "no-store")
		if r.Method == http.MethodPost {
			if o := r.Header.Get("Origin"); o != "" && o != "null" {
				if u, err := url.Parse(o); err != nil || u.Host != r.Host {
					http.Error(w, "허용되지 않은 요청 출처입니다.", http.StatusForbidden)
					return
				}
			}
			r.Body = http.MaxBytesReader(w, r.Body, 20<<20)
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "web/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))

	mux.HandleFunc("GET /login", a.handleLoginPage)
	mux.HandleFunc("POST /login", a.handleLogin)
	mux.HandleFunc("POST /logout", a.handleLogout)
	mux.HandleFunc("GET /password", a.requireUser(a.handlePasswordPage))
	mux.HandleFunc("POST /password", a.requireUser(a.handlePassword))

	// 투입 인력 본인용 (로그인 없이 개인 링크로 접근)
	mux.HandleFunc("GET /s/{token}", a.handleSelfPage)
	mux.HandleFunc("POST /s/{token}", a.handleSelfSubmit)

	mux.HandleFunc("GET /{$}", a.requireUser(a.handleDashboard))

	mux.HandleFunc("GET /people", a.requireUser(a.handlePeople))
	mux.HandleFunc("GET /people/new", a.requireAdmin(a.handlePersonNewPage))
	mux.HandleFunc("POST /people/new", a.requireUser(a.handlePersonCreate))
	mux.HandleFunc("GET /people/{id}", a.requireUser(a.handlePerson))
	mux.HandleFunc("POST /people/{id}/edit", a.requireUser(a.handlePersonUpdate))
	mux.HandleFunc("POST /people/{id}/link", a.requireUser(a.handlePersonLink))
	mux.HandleFunc("POST /people/{id}/approve", a.requireUser(a.handlePersonApprove))
	mux.HandleFunc("POST /people/{id}/reject", a.requireUser(a.handlePersonReject))
	mux.HandleFunc("POST /people/{id}/offboard", a.requireUser(a.handlePersonOffboard))
	mux.HandleFunc("POST /people/{id}/complete", a.requireUser(a.handlePersonComplete))
	mux.HandleFunc("POST /people/{id}/badge-return", a.requireUser(a.handleBadgeReturn))
	mux.HandleFunc("POST /people/{id}/purge", a.requireUser(a.handlePersonPurge))
	mux.HandleFunc("POST /people/{id}/delete", a.requireUser(a.handlePersonDelete))
	mux.HandleFunc("POST /people/{id}/ack", a.requireUser(a.handleOfflineAck))
	mux.HandleFunc("POST /people/{id}/accounts", a.requireUser(a.handleAccountCreate))
	mux.HandleFunc("POST /accounts/{id}/revoke", a.requireUser(a.handleAccountRevoke))
	mux.HandleFunc("POST /people/{id}/assign", a.requireUser(a.handleAssetAssignFromPerson))

	mux.HandleFunc("GET /assets", a.requireUser(a.handleAssets))
	mux.HandleFunc("GET /assets/new", a.requireAdmin(a.handleAssetNewPage))
	mux.HandleFunc("POST /assets/new", a.requireUser(a.handleAssetCreate))
	mux.HandleFunc("POST /assets/import", a.requireUser(a.handleAssetImport))
	mux.HandleFunc("GET /assets/{id}", a.requireUser(a.handleAsset))
	mux.HandleFunc("POST /assets/{id}/edit", a.requireUser(a.handleAssetUpdate))
	mux.HandleFunc("POST /assets/{id}/assign", a.requireUser(a.handleAssetAssign))
	mux.HandleFunc("POST /assets/{id}/return", a.requireUser(a.handleAssetReturn))
	mux.HandleFunc("POST /assets/{id}/check", a.requireUser(a.handleAssetCheck))
	mux.HandleFunc("POST /assets/{id}/status", a.requireUser(a.handleAssetStatus))

	mux.HandleFunc("GET /docs", a.requireUser(a.handleDocs))
	mux.HandleFunc("GET /docs/new", a.requireAdmin(a.handleDocNewPage))
	mux.HandleFunc("POST /docs/new", a.requireUser(a.handleDocCreate))
	mux.HandleFunc("GET /docs/{id}", a.requireUser(a.handleDoc))
	mux.HandleFunc("POST /docs/{id}", a.requireUser(a.handleDocUpdate))
	mux.HandleFunc("POST /docs/{id}/bulk", a.requireUser(a.handleDocBulkAck))

	mux.HandleFunc("GET /exports", a.requireUser(a.handleExports))
	mux.HandleFunc("GET /exports/{key}", a.requireUser(a.handleExport))
	mux.HandleFunc("GET /exports/{key}/template", a.requireUser(a.handleTemplateDownload))
	mux.HandleFunc("POST /exports/{key}/template", a.requireUser(a.handleTemplateUpload))
	mux.HandleFunc("POST /exports/{key}/template/reset", a.requireUser(a.handleTemplateReset))
	mux.HandleFunc("GET /acks/{id}/hwpx", a.requireUser(a.handleAckDoc))
	mux.HandleFunc("GET /people/{id}/exit.hwpx", a.requireUser(a.handleExitDoc))

	mux.HandleFunc("GET /settings", a.requireAdmin(a.handleSettings))
	mux.HandleFunc("POST /settings", a.requireUser(a.handleSettingsSave))
	mux.HandleFunc("POST /users", a.requireUser(a.handleUserCreate))
	mux.HandleFunc("POST /users/{id}/reset", a.requireUser(a.handleUserReset))
	mux.HandleFunc("POST /users/{id}/delete", a.requireUser(a.handleUserDelete))
	mux.HandleFunc("GET /audit", a.requireAdmin(a.handleAudit))
	mux.HandleFunc("GET /backup", a.requireAdmin(a.handleBackup))

	return securityHeaders(mux)
}
