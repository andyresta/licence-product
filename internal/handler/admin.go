package handler

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"

	"github.com/andyresta/licence-product/internal/model"
	"github.com/andyresta/licence-product/internal/service/admin"
	"github.com/andyresta/licence-product/web"
)

type AdminHandler struct {
	admin   *admin.Service
	session *sessionSigner
	captcha *captchaSigner
}

func NewAdminHandler(adminSvc *admin.Service, sessionSecret string) *AdminHandler {
	return &AdminHandler{admin: adminSvc, session: newSessionSigner(sessionSecret), captcha: newCaptchaSigner(sessionSecret)}
}

// RequireAdmin exposes the session middleware so main.go can wrap admin-only routes.
func (h *AdminHandler) RequireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return h.session.requireAdmin(next)
}

type pageData struct {
	Title     string
	LoggedIn  bool
	Alert     string
	AlertKind string
	Query     string

	Licenses any // dashboard's license search results (model.LicenseCustomer list)
	License  any // license_detail.html's single license (model.LicenseCustomer)

	Customers        any // customers.html's directory list (model.Customer list)
	Customer         any // customer_profile.html's single customer (model.Customer)
	CustomerLicenses any // customer_profile.html's licenses owned by that customer

	Products any

	Activations            any
	Purchases              any
	Branches               any
	SubscriptionExtensions any
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
		alert = "Username, password, atau kode captcha salah."
	}
	h.renderPage(w, "login.html", pageData{Title: "Login", Alert: alert, AlertKind: "danger"})
}

// CaptchaImage serves the distorted PNG the login page's <img> tag points at —
// generating and cookie-signing a fresh code on every request. Public (no
// RequireAdmin) since it must be reachable before login.
func (h *AdminHandler) CaptchaImage(w http.ResponseWriter, r *http.Request) {
	h.captcha.ServeImage(w, r)
}

func (h *AdminHandler) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/login?err=1", http.StatusSeeOther)
		return
	}
	captchaOK := h.captcha.verifyRequest(r)
	h.captcha.clearCookie(w) // one attempt per solved code — next try needs a fresh image load
	if !captchaOK {
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

// Dashboard is the license directory — every (customer, product) license, its
// activation/branch usage, and its lifetime/subscription status. Product management
// lives on its own page (ProductsPage); the customer directory lives on its own
// (CustomerDirectory).
func (h *AdminHandler) Dashboard(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	licenses, err := h.admin.ListLicenses(r.Context(), query)
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	products, err := h.admin.ListProducts(r.Context())
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "Dashboard", LoggedIn: true, Query: query, Licenses: licenses, Products: products}
	applyFlash(r, &data)
	h.renderPage(w, "dashboard.html", data)
}

// ProductsPage is the standalone product-management page (full CRUD) that used to be a
// card at the bottom of the dashboard.
func (h *AdminHandler) ProductsPage(w http.ResponseWriter, r *http.Request) {
	products, err := h.admin.ListProducts(r.Context())
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "Produk", LoggedIn: true, Products: products}
	applyFlash(r, &data)
	h.renderPage(w, "products.html", data)
}

// CustomerDirectory lists every Customer on file (independent of which products they've
// licensed) with a search box and an "add customer" form.
func (h *AdminHandler) CustomerDirectory(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	customers, err := h.admin.ListCustomerDirectory(r.Context(), query)
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: "Customer", LoggedIn: true, Query: query, Customers: customers}
	applyFlash(r, &data)
	h.renderPage(w, "customers.html", data)
}

// CustomerProfilePage shows one Customer's contact info plus every License they hold
// across all products — the "everything this person owns" view the old per-license
// detail page couldn't provide.
func (h *AdminHandler) CustomerProfilePage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	customer, appErr := h.admin.GetCustomerProfile(r.Context(), id)
	if appErr != nil {
		http.Error(w, appErr.Message, appErr.HTTPStatus())
		return
	}
	licenses, err := h.admin.ListLicensesByCustomer(r.Context(), id)
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	products, err := h.admin.ListProducts(r.Context())
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	data := pageData{Title: customer.Email, LoggedIn: true, Customer: customer, CustomerLicenses: licenses, Products: products}
	applyFlash(r, &data)
	h.renderPage(w, "customer_profile.html", data)
}

func (h *AdminHandler) CreateCustomer(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/customers?err="+url.QueryEscape("form tidak valid"), http.StatusSeeOther)
		return
	}
	customer, appErr := h.admin.CreateCustomer(r.Context(), r.FormValue("email"), r.FormValue("nama"), r.FormValue("telp"), r.FormValue("catatan"))
	if appErr != nil {
		http.Redirect(w, r, "/admin/customers?err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/customers/"+customer.CustomerID+"?ok="+url.QueryEscape("Customer berhasil ditambahkan"), http.StatusSeeOther)
}

func (h *AdminHandler) UpdateCustomerProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/customers/"+id+"?err="+url.QueryEscape("form tidak valid"), http.StatusSeeOther)
		return
	}
	appErr := h.admin.UpdateCustomerProfile(r.Context(), id, r.FormValue("nama"), r.FormValue("telp"), r.FormValue("catatan"))
	if appErr != nil {
		http.Redirect(w, r, "/admin/customers/"+id+"?err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/customers/"+id+"?ok="+url.QueryEscape("Profil customer berhasil diperbarui"), http.StatusSeeOther)
}

// DeleteCustomer removes a customer — refused (see admin.Service.DeleteCustomer) while
// they still have any license, so this never silently destroys license history.
func (h *AdminHandler) DeleteCustomer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if appErr := h.admin.DeleteCustomer(r.Context(), id); appErr != nil {
		http.Redirect(w, r, "/admin/customers/"+id+"?err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/customers?ok="+url.QueryEscape("Customer berhasil dihapus"), http.StatusSeeOther)
}

// LicenseDetail shows one License's full detail — activations, branches, purchase
// history, and (for a SUBSCRIPTION license) its extension history. Formerly named
// CustomerDetail; renamed because model.LicenseCustomer is a License, not a Customer.
func (h *AdminHandler) LicenseDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	license, appErr := h.admin.GetLicense(r.Context(), id)
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
	branches, err := h.admin.ListBranches(r.Context(), id)
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	subscriptionExtensions, err := h.admin.ListSubscriptionExtensions(r.Context(), id)
	if err != nil {
		http.Error(w, "gagal memuat data", http.StatusInternalServerError)
		return
	}
	data := pageData{
		Title: license.Email, LoggedIn: true, License: license,
		Activations: activations, Purchases: purchases, Branches: branches,
		SubscriptionExtensions: subscriptionExtensions,
	}
	applyFlash(r, &data)
	h.renderPage(w, "license_detail.html", data)
}

// DeleteLicense permanently removes a license and its own activations/branches/
// purchases/subscription history — used from the license detail page to let the admin
// clear out a mistaken or test license entirely, rather than just deactivating it.
func (h *AdminHandler) DeleteLicense(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if appErr := h.admin.DeleteLicense(r.Context(), id); appErr != nil {
		http.Redirect(w, r, "/admin/licenses/"+id+"?err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin?ok="+url.QueryEscape("Lisensi berhasil dihapus"), http.StatusSeeOther)
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
	// Every entry point (dashboard's quick form, a customer profile's "add license"
	// form) posts here and expects to land back where it came from.
	redirectBack := "/admin?"
	if v := r.FormValue("redirect_to"); v != "" {
		redirectBack = v + "?"
	}
	seats, err := strconv.Atoi(r.FormValue("seats"))
	if err != nil || seats <= 0 {
		http.Redirect(w, r, redirectBack+"err="+url.QueryEscape("jumlah seat tidak valid"), http.StatusSeeOther)
		return
	}
	licenseType := r.FormValue("license_type")
	if licenseType == "" {
		licenseType = model.LicenseTypeLifetime
	}
	subscriptionMonths, _ := strconv.Atoi(r.FormValue("subscription_months"))
	var catatan *string
	if v := r.FormValue("catatan"); v != "" {
		catatan = &v
	}
	adminUserID := adminUserIDFromContext(r.Context())
	appErr := h.admin.RecordPurchase(r.Context(), admin.RecordPurchaseInput{
		Email:              r.FormValue("email"),
		ProductCode:        r.FormValue("product_code"),
		Seats:              seats,
		Catatan:            catatan,
		RecordedBy:         adminUserID,
		LicenseType:        licenseType,
		SubscriptionMonths: subscriptionMonths,
	})
	if appErr != nil {
		http.Redirect(w, r, redirectBack+"err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, redirectBack+"ok="+url.QueryEscape("Lisensi berhasil dicatat"), http.StatusSeeOther)
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

func (h *AdminHandler) UpdateProduct(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape("form tidak valid"), http.StatusSeeOther)
		return
	}
	appErr := h.admin.UpdateProduct(r.Context(), id, r.FormValue("nama"), r.FormValue("keterangan"))
	if appErr != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin?ok="+url.QueryEscape("Produk berhasil diperbarui"), http.StatusSeeOther)
}

// ToggleProductStatus reads the desired new state from the submitting form's own hidden
// "aktif" field (each product row renders its own Aktifkan/Nonaktifkan form with the
// opposite of its current state baked in) rather than flipping status_aktif blindly —
// that keeps this idempotent under a double-submit instead of toggling twice.
func (h *AdminHandler) ToggleProductStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape("form tidak valid"), http.StatusSeeOther)
		return
	}
	aktif := r.FormValue("aktif") == "true"
	if appErr := h.admin.SetProductStatus(r.Context(), id, aktif); appErr != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	msg := "Produk berhasil dinonaktifkan"
	if aktif {
		msg = "Produk berhasil diaktifkan"
	}
	http.Redirect(w, r, "/admin?ok="+url.QueryEscape(msg), http.StatusSeeOther)
}

func (h *AdminHandler) DeleteProduct(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if appErr := h.admin.DeleteProduct(r.Context(), id); appErr != nil {
		http.Redirect(w, r, "/admin?err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin?ok="+url.QueryEscape("Produk berhasil dihapus"), http.StatusSeeOther)
}

func (h *AdminHandler) SetBranchQuota(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/licenses/"+id+"?err="+url.QueryEscape("form tidak valid"), http.StatusSeeOther)
		return
	}
	maxBranches, err := strconv.Atoi(r.FormValue("max_branches"))
	if err != nil || maxBranches < 0 {
		http.Redirect(w, r, "/admin/licenses/"+id+"?err="+url.QueryEscape("kuota branch tidak valid"), http.StatusSeeOther)
		return
	}
	if appErr := h.admin.SetMaxBranches(r.Context(), id, maxBranches); appErr != nil {
		http.Redirect(w, r, "/admin/licenses/"+id+"?err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/licenses/"+id+"?ok="+url.QueryEscape("Kuota branch berhasil diperbarui"), http.StatusSeeOther)
}

func (h *AdminHandler) ForceDeactivateBranch(w http.ResponseWriter, r *http.Request) {
	branchID := r.PathValue("id")
	adminUserID := adminUserIDFromContext(r.Context())
	appErr := h.admin.ForceDeactivateBranch(r.Context(), branchID, adminUserID)
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/admin"
	}
	if appErr != nil {
		http.Redirect(w, r, referer+"?err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, referer+"?ok="+url.QueryEscape("Branch berhasil dilepas"), http.StatusSeeOther)
}

func (h *AdminHandler) ExtendSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/admin/licenses/"+id+"?err="+url.QueryEscape("form tidak valid"), http.StatusSeeOther)
		return
	}
	months, err := strconv.Atoi(r.FormValue("months"))
	if err != nil || months <= 0 {
		http.Redirect(w, r, "/admin/licenses/"+id+"?err="+url.QueryEscape("jumlah bulan tidak valid"), http.StatusSeeOther)
		return
	}
	var catatan *string
	if v := r.FormValue("catatan"); v != "" {
		catatan = &v
	}
	adminUserID := adminUserIDFromContext(r.Context())
	if appErr := h.admin.ExtendSubscription(r.Context(), id, months, catatan, adminUserID); appErr != nil {
		http.Redirect(w, r, "/admin/licenses/"+id+"?err="+url.QueryEscape(appErr.Message), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/licenses/"+id+"?ok="+url.QueryEscape("Langganan berhasil diperpanjang"), http.StatusSeeOther)
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
