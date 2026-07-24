package handlers

import (
	"net/http"

	"windshift/internal/services"
)

// respondPlanningValidationError maps the shared service validation contract
// onto the legacy API response shape. It returns true when err was handled.
func respondPlanningValidationError(w http.ResponseWriter, r *http.Request, err error) bool {
	validationErr, ok := services.AsPlanningValidationError(err)
	if !ok {
		return false
	}
	respondValidationError(w, r, validationErr.Error())
	return true
}
