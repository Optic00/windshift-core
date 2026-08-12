package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"windshift/internal/services"
)

// PushHandler exposes the Web Push subscription lifecycle. Every endpoint is
// scoped to the authenticated user — a client can only register, list, or
// delete its own subscriptions.
type PushHandler struct {
	service *services.PushService
}

// NewPushHandler constructs a PushHandler.
func NewPushHandler(service *services.PushService) *PushHandler {
	return &PushHandler{service: service}
}

// subscribeRequest mirrors the browser PushSubscription JSON shape.
type subscribeRequest struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		Auth   string `json:"auth"`
		P256dh string `json:"p256dh"`
	} `json:"keys"`
}

// GetVAPIDKey returns the public VAPID key the browser needs to subscribe, plus
// whether push is configured at all so the client can hide the affordance.
func (h *PushHandler) GetVAPIDKey(w http.ResponseWriter, r *http.Request) {
	if _, ok := RequireAuth(w, r); !ok {
		return
	}
	respondJSONOK(w, map[string]any{
		"enabled":    h.service.Enabled(),
		"public_key": h.service.PublicKey(),
	})
}

// Subscribe registers (or refreshes) a push subscription for the current user.
func (h *PushHandler) Subscribe(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}

	var req subscribeRequest
	if err := newJSONDecoder(w, r).Decode(&req); err != nil {
		respondBadRequest(w, r, "invalid subscription payload")
		return
	}
	if req.Endpoint == "" || req.Keys.Auth == "" || req.Keys.P256dh == "" {
		respondBadRequest(w, r, "endpoint and keys are required")
		return
	}

	// User agent comes from the request header, not the client body, so a label
	// can't be spoofed and we don't have to trust client-supplied device names.
	if err := h.service.Subscribe(user.ID, req.Endpoint, req.Keys.Auth, req.Keys.P256dh, r.UserAgent()); err != nil {
		if errors.Is(err, services.ErrInvalidEndpoint) {
			respondBadRequest(w, r, "invalid subscription endpoint")
			return
		}
		respondInternalError(w, r, err)
		return
	}
	respondJSONCreated(w, map[string]any{"status": "subscribed"})
}

// List returns the current user's active subscriptions (no keys).
func (h *PushHandler) List(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	subs, err := h.service.List(user.ID)
	if err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, subs)
}

// Delete removes one of the current user's subscriptions by id.
func (h *PushHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user, ok := RequireAuth(w, r)
	if !ok {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		respondBadRequest(w, r, "invalid subscription id")
		return
	}
	if err := h.service.Delete(user.ID, id); err != nil {
		respondInternalError(w, r, err)
		return
	}
	respondJSONOK(w, map[string]any{"status": "deleted"})
}
