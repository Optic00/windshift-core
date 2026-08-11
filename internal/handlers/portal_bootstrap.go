package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"windshift/internal/auth"
	"windshift/internal/middleware"
	"windshift/internal/models"
	"windshift/internal/repository"
	"windshift/internal/services"
)

type PublicPortalBootstrapResponse struct {
	Portal       map[string]any             `json:"portal"`
	RequestTypes []models.RequestType       `json:"request_types"`
	AssetReports []models.PublicAssetReport `json:"asset_reports"`
}

type PortalUserBootstrapResponse struct {
	Authenticated bool                            `json:"authenticated"`
	IsInternal    bool                            `json:"is_internal"`
	User          map[string]any                  `json:"user,omitempty"`
	Customer      map[string]any                  `json:"customer,omitempty"`
	MyRequests    []services.PortalRequestSummary `json:"my_requests"`
	MyApprovals   []*models.ApprovalRequest       `json:"my_approvals"`
}

// GetBootstrap composes the anonymous/optional-session portal shell. Request
// types and asset reports use one shared visibility context and remain
// best-effort, matching the old frontend loaders' failure behavior.
func (h *PortalHandler) GetBootstrap(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	portalResult, err := h.findChannelByPortalSlug(ctx, r.PathValue("slug"))
	if err != nil {
		respondNotFound(w, r, "portal")
		return
	}
	if !h.verifyPortalSessionBinding(w, r, portalResult.channel.ID) {
		return
	}
	portal, err := h.loadPortalData(ctx, portalResult.channel, portalResult.config)
	if errors.Is(err, repository.ErrNotFound) {
		respondNotFound(w, r, "workspace")
		return
	}
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	response := PublicPortalBootstrapResponse{
		Portal:       portal,
		RequestTypes: []models.RequestType{},
		AssetReports: []models.PublicAssetReport{},
	}
	vc := h.getPortalVisibilityContext(ctx, r, portalResult.channel.ID)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		requestTypes, err := h.loadPortalRequestTypes(ctx, portalResult.channel.ID, vc)
		if err != nil {
			slog.Warn("portal bootstrap: request types unavailable", "channel_id", portalResult.channel.ID, "error", err)
			return
		}
		response.RequestTypes = requestTypes
	}()
	go func() {
		defer wait.Done()
		assetReports, err := h.loadPortalAssetReports(portalResult, vc)
		if err != nil {
			slog.Warn("portal bootstrap: asset reports unavailable", "channel_id", portalResult.channel.ID, "error", err)
			return
		}
		response.AssetReports = assetReports
	}()
	wait.Wait()
	respondJSONOK(w, response)
}

// GetUserBootstrap composes the optional identity and the two badge datasets
// loaded on every signed-in portal entry. Anonymous probes return a stable
// unauthenticated snapshot rather than a noisy 401 resource error.
func (h *PortalHandler) GetUserBootstrap(w http.ResponseWriter, r *http.Request) {
	ctx, cancel, channel, _, ok := h.resolvePortalBySlug(w, r)
	if !ok {
		return
	}
	defer cancel()
	internalUserID, portalCustomerID := h.getAuthFromContext(r)
	if internalUserID == nil && portalCustomerID == nil {
		respondJSONOK(w, PortalUserBootstrapResponse{
			Authenticated: false,
			MyRequests:    []services.PortalRequestSummary{},
			MyApprovals:   []*models.ApprovalRequest{},
		})
		return
	}

	response, err := h.portalAuthSnapshot(ctx, r)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}

	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		requests, err := h.loadMyPortalRequests(ctx, r, channel.ID)
		if err != nil {
			slog.Warn("portal user bootstrap: requests unavailable", "channel_id", channel.ID, "error", err)
			return
		}
		response.MyRequests = requests
	}()
	go func() {
		defer wait.Done()
		if h.approvalService == nil {
			return
		}
		actor, err := h.portalApprovalActorFromRequest(r)
		if err != nil {
			slog.Warn("portal user bootstrap: approval actor unavailable", "channel_id", channel.ID, "error", err)
			return
		}
		approvals, err := h.getApprovalsForPortalActor(ctx, actor, "pending", channel.ID)
		if err != nil {
			slog.Warn("portal user bootstrap: approvals unavailable", "channel_id", channel.ID, "error", err)
			return
		}
		response.MyApprovals = approvals
	}()
	wait.Wait()
	respondJSONOK(w, response)
}

func (h *PortalHandler) portalAuthSnapshot(ctx context.Context, r *http.Request) (PortalUserBootstrapResponse, error) {
	response := PortalUserBootstrapResponse{
		Authenticated: true,
		MyRequests:    []services.PortalRequestSummary{},
		MyApprovals:   []*models.ApprovalRequest{},
	}
	if session, ok := r.Context().Value(middleware.ContextKeySession).(*auth.Session); ok && session != nil && session.User != nil {
		response.IsInternal = true
		response.User = map[string]any{
			"id":         session.User.ID,
			"email":      session.User.Email,
			"name":       session.User.FirstName + " " + session.User.LastName,
			"first_name": session.User.FirstName,
			"last_name":  session.User.LastName,
		}
		return response, nil
	}

	portalSession, ok := r.Context().Value(middleware.ContextKeyPortalSession).(*auth.PortalSession)
	if !ok || portalSession == nil || portalSession.Customer == nil {
		return PortalUserBootstrapResponse{}, fmt.Errorf("authenticated portal session missing customer")
	}
	info, err := h.portalAuthRepo.GetCustomerSessionInfo(ctx, portalSession.Customer.ID)
	if err != nil {
		slog.Warn("portal user bootstrap: passkey state unavailable", "portal_customer_id", portalSession.Customer.ID, "error", err)
		info = &repository.PortalCustomerSessionInfo{}
	}
	response.Customer = map[string]any{
		"id":                          portalSession.Customer.ID,
		"email":                       portalSession.Customer.Email,
		"name":                        portalSession.Customer.Name,
		"passkey_count":               info.PasskeyCount,
		"dismissed_passkey_prompt_at": info.DismissedPasskeyPromptAt,
	}
	return response, nil
}

func (h *PortalHandler) loadMyPortalRequests(ctx context.Context, r *http.Request, channelID int) ([]services.PortalRequestSummary, error) {
	internalUserID, portalCustomerID := h.getAuthFromContext(r)
	var (
		requests []services.PortalRequestSummary
		err      error
	)
	switch {
	case internalUserID != nil:
		requests, err = h.portalService.GetRequestsByCreatorID(ctx, *internalUserID, channelID)
	case portalCustomerID != nil:
		requests, err = h.portalService.GetRequestsByPortalCustomerID(ctx, *portalCustomerID, channelID)
	default:
		return nil, errPortalApprovalActorUnauthorized
	}
	if requests == nil {
		requests = []services.PortalRequestSummary{}
	}
	return requests, err
}
