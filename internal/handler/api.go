// Package handler wires internal/service onto HTTP — the public activation API and
// the admin panel.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/andyresta/licence-product/internal/apperror"
	"github.com/andyresta/licence-product/internal/service/branch"
	"github.com/andyresta/licence-product/internal/service/license"
)

type APIHandler struct {
	license *license.Service
	branch  *branch.Service
}

func NewAPIHandler(license *license.Service, branch *branch.Service) *APIHandler {
	return &APIHandler{license: license, branch: branch}
}

type envelope struct {
	Success bool   `json:"success"`
	Data    any    `json:"data,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body) //nolint:errcheck // response already committed; nothing to do if the client disconnected
}

func writeAppError(w http.ResponseWriter, err *apperror.Error) {
	writeJSON(w, err.HTTPStatus(), envelope{Success: false, Code: string(err.Code), Message: err.Message})
}

type activateRequest struct {
	Email              string  `json:"email"`
	ProductCode        string  `json:"product_code"`
	MachineFingerprint string  `json:"machine_fingerprint"`
	MachineLabel       *string `json:"machine_label"`
}

// Activate handles POST /api/v1/activate. See README.md for the full request/response
// contract every consuming product builds against.
func (h *APIHandler) Activate(w http.ResponseWriter, r *http.Request) {
	var req activateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAppError(w, apperror.New(apperror.Validation, "body permintaan tidak valid"))
		return
	}
	if req.Email == "" || req.ProductCode == "" || req.MachineFingerprint == "" {
		writeAppError(w, apperror.New(apperror.Validation, "email, product_code, dan machine_fingerprint wajib diisi"))
		return
	}

	result, appErr := h.license.Activate(r.Context(), license.ActivateInput{
		Email:              req.Email,
		ProductCode:        req.ProductCode,
		MachineFingerprint: req.MachineFingerprint,
		MachineLabel:       req.MachineLabel,
	})
	if appErr != nil {
		writeAppError(w, appErr)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Success: true, Data: map[string]any{
		"license_lic":   result.LicenseLic,
		"activation_id": result.ActivationID,
		"reused":        result.Reused,
	}})
}

type deactivateRequest struct {
	LicenseLic string `json:"license_lic"`
}

// Deactivate handles POST /api/v1/deactivate. The caller proves ownership of the
// activation being freed by presenting its own currently-signed license.lic content —
// see license.Service.Deactivate's doc comment for why.
func (h *APIHandler) Deactivate(w http.ResponseWriter, r *http.Request) {
	var req deactivateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAppError(w, apperror.New(apperror.Validation, "body permintaan tidak valid"))
		return
	}
	if req.LicenseLic == "" {
		writeAppError(w, apperror.New(apperror.Validation, "license_lic wajib diisi"))
		return
	}
	if appErr := h.license.Deactivate(r.Context(), req.LicenseLic); appErr != nil {
		writeAppError(w, appErr)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Success: true})
}

type branchCheckRequest struct {
	LicenseLic string `json:"license_lic"`
}

// BranchCheck handles POST /api/v1/branches/check — a read-only peek at a license's
// branch quota, identified by its license.lic content. Registers nothing; safe to call
// as often as a product likes (e.g. to show "3/5 cabang terpakai" in its own UI).
func (h *APIHandler) BranchCheck(w http.ResponseWriter, r *http.Request) {
	var req branchCheckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAppError(w, apperror.New(apperror.Validation, "body permintaan tidak valid"))
		return
	}
	if req.LicenseLic == "" {
		writeAppError(w, apperror.New(apperror.Validation, "license_lic wajib diisi"))
		return
	}
	result, appErr := h.branch.Check(r.Context(), req.LicenseLic)
	if appErr != nil {
		writeAppError(w, appErr)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Success: true, Data: map[string]any{
		"exist": result.Exist,
		"kuota": result.Kuota,
	}})
}

type branchReleaseRequest struct {
	LicenseLic string `json:"license_lic"`
	BranchCode string `json:"branch_code"`
}

// BranchRelease handles POST /api/v1/branches/release — the self-service counterpart to
// BranchRegister, for a product to free up a branch/outlet slot on its own (e.g. a store
// closed down) without needing the vendor to force-deactivate it from the admin panel.
func (h *APIHandler) BranchRelease(w http.ResponseWriter, r *http.Request) {
	var req branchReleaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAppError(w, apperror.New(apperror.Validation, "body permintaan tidak valid"))
		return
	}
	if req.LicenseLic == "" || req.BranchCode == "" {
		writeAppError(w, apperror.New(apperror.Validation, "license_lic dan branch_code wajib diisi"))
		return
	}
	result, appErr := h.branch.Release(r.Context(), req.LicenseLic, req.BranchCode)
	if appErr != nil {
		writeAppError(w, appErr)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Success: true, Data: map[string]any{
		"exist": result.Exist,
		"kuota": result.Kuota,
	}})
}

type branchRegisterRequest struct {
	LicenseLic  string  `json:"license_lic"`
	BranchCode  string  `json:"branch_code"`
	BranchLabel *string `json:"branch_label"`
}

// BranchRegister handles POST /api/v1/branches/register. Unlike every other failure in
// this API, a QUOTA_EXCEEDED response here still carries "data" (exist/kuota) — the
// caller needs those numbers to tell the customer why registration was refused, not
// just that it was.
func (h *APIHandler) BranchRegister(w http.ResponseWriter, r *http.Request) {
	var req branchRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAppError(w, apperror.New(apperror.Validation, "body permintaan tidak valid"))
		return
	}
	if req.LicenseLic == "" || req.BranchCode == "" {
		writeAppError(w, apperror.New(apperror.Validation, "license_lic dan branch_code wajib diisi"))
		return
	}
	result, appErr := h.branch.Register(r.Context(), branch.RegisterInput{
		LicenseLic:  req.LicenseLic,
		BranchCode:  req.BranchCode,
		BranchLabel: req.BranchLabel,
	})
	if appErr != nil {
		body := envelope{Success: false, Code: string(appErr.Code), Message: appErr.Message}
		if appErr.Code == apperror.QuotaExceeded {
			body.Data = map[string]any{"exist": result.Exist, "kuota": result.Kuota}
		}
		writeJSON(w, appErr.HTTPStatus(), body)
		return
	}
	writeJSON(w, http.StatusOK, envelope{Success: true, Data: map[string]any{
		"exist": result.Exist,
		"kuota": result.Kuota,
	}})
}
