package handler

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"github.com/andyresta/licence-product/internal/service/admin"
	"github.com/andyresta/licence-product/web"
)

type AdminHandler struct {
	admin   *admin.Service
	session *sessionSigner
}

func NewAdminHandler(adminSvc *admin.Service, sessionSecret string) *AdminHandler {
	return &AdminHandler{admin: adminSvc, session: newSessionSigner(sessionSecret)}
}

// RequireAdmin exposes the session middleware so main.go can wrap admin-only routes.
func (h *AdminHandler) RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return h.session.requireAdmin(next)
}

type pageData struct {
	Title       string
	LoggedIn    bool
	Alert       string
	AlertKind   string
	Query       string
	Customers   any
	Products    any
	Customer    any
	Activations any
	Purchases   any
}

// renderPage parses layout.html plus exactly one page template per call, rather than
// one shared *template.Template for every page — html/template's {{define "content"}}
// is shared namespace-wide, so parsing every page file together would leave only the
// last-parsed page's "content" block ever rendering.
func (h *AdminHandler) renderPage(w http.ResponseWriter, page string, data pageData) {
	tmpl := template.Must(template.ParseFS(web.FS, "templates/layout.html", "templates/"+page))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
	}
}

func (h *AdminHandler) LoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.session.adminFromRequest(r); ok {
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
		return
	}
	alert := ""
	if r.URL.Query().Get("err") != "" {
		alert = "Username atau password salah."
	}
	h.renderPage(w, "login.html", pageData{Title: "Login", Alert: alert, AlertKind: "danger"})
}

func (h *AdminHandler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/login?err=1", http.StatusSeeOther)
		return
	}
	adminUserID, appErr := h.admin.Authenticate(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if appErr != nil {
		http.Redirect(w, r, "/admin/login?err=1", http.StatusSeeOther)
		return
	}
	h.session.setCookie(w, adminUserID)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func (h *AdminHandler) Logout(w http.ResponseWriter, r *http.Request) {
	h.session.clearCookie(w)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (h *AdminHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	customers, err := h.admin.ListCustomers(r.Context(), query)
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	products, err := h.admin.ListProducts(r.Context())
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "Dashboard", LoggedIn: true, Query: query, Customers: customers, Products: products}
	applyFlash(r, &data)
	h.renderPage(w, "dashboard.html", data)
}

func (h *AdminHandler) CustomerDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	customer, appErr := h.admin.GetCustomer(r.Context(), id)
	if appErr != nil {
		http.Error(w, appErr.Message, appErr.HTTPStatus())
		return
	}
	activations, err := h.admin.ListActivations(r.Context(), id)
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	purchases, err := h.admin.ListPurchases(r.Context(), id)
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: customer.Email, LoggedIn: true, Customer: customer, Activations: activations, Purchases: purchases}
	applyFlash(r, &data)
	h.renderPage(w, "customer_detail.html", data)
}

func (h *AdminHandler) CreateProduct(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape("form tidak valid"), http.StatusSeeOther)
		return
	}
	_, appErr := h.admin.CreateProduct(r.Context(), r.FormValue("product_code"), r.FormValue("nama"))
	if appErr != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin?ok="+url.QueryEscape("Produk berhasil ditambahkan"), http.StatusSeeOther)
}

func (h *AdminHandler) RecordPurchase(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape("form tidak valid"), http.StatusSeeOther)
		return
	}
	seats, err := strconv.Atoi(r.FormValue("seats"))
	if err != nil || seats <= 0 {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape("jumlah seat tidak valid"), http.StatusSeeOther)
		return
	}
	email := r.FormValue("email")
	productCode := r.FormValue("product_code")
	var catatan *string
	if v := r.FormValue("catatan"); v != "" {
		catatan = &v
	}
	adminUserID := adminUserIDFromContext(r.Context())
	appErr := h.admin.RecordPurchase(r.Context(), email, productCode, seats, catatan, adminUserID)
	redirectBack := "/admin?"
	if appErr != nil {
		http.Redirect(w, r, redirectBack+"err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, redirectBack+"ok="+url.QueryEscape("Pembelian berhasil dicatat"), http.StatusSeeOther)
}

func (h *AdminHandler) ForceDeactivate(w http.ResponseWriter, r *http.Request) {
	activationID := r.PathValue("id")
	adminUserID := adminUserIDFromContext(r.Context())
	appErr := h.admin.ForceDeactivate(r.Context(), activationID, adminUserID)
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/admin"
	}
	sep := "?"
	if appErr != nil {
		http.Redirect(w, r, referer+sep+"err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, referer+sep+"ok="+url.QueryEscape("Aktivasi berhasil dilepas"), http.StatusSeeOther)
}

func applyFlash(r *http.Request, data *pageData) {
	if ok := r.URL.Query().Get("ok"); ok != "" {
		data.Alert = ok
		data.AlertKind = "success"
	} else if errMsg := r.URL.Query().Get("err"); errMsg != "" {
		data.Alert = errMsg
		data.AlertKind = "danger"
	}
}
