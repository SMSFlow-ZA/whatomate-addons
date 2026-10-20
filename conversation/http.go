package conversation

import (
	"database/sql"
	"github.com/SMSFlow-ZA/whatomate-addons/api"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"net/http"
	"strings"
)

type httpHandler struct{ s Service }

func NewHTTPHandler(db *sql.DB, cfg HTTPConfig) http.Handler {
	return &httpHandler{Service{DB: db, Config: cfg}}
}
func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a, e := api.Require(r, "conversation:simulate")
	if e != nil || !h.s.Config.EnabledSyntheticDevelopment {
		api.Error(w, 403, "denied")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/extensions/conversation/")
	if h.s.DB == nil {
		api.Error(w, 503, "unavailable")
		return
	}
	switch {
	case r.Method == "POST" && path == "simulate":
		var body struct {
			Sender    string `json:"sender"`
			Text      string `json:"text"`
			MessageID string `json:"provider_message_id"`
		}
		if api.Decode(r, &body) != nil {
			api.Error(w, 400, "invalid")
			return
		}
		turn, e := h.s.Receive(r.Context(), Inbound{Tenant: a.OrganizationID, Account: "synthetic", Sender: body.Sender, Text: body.Text, ProviderMessageID: body.MessageID})
		if e != nil {
			status := 503
			if e == ErrDenied {
				status = 403
			}
			if e == ErrConflict {
				status = 409
			}
			api.Error(w, status, "conversation_unavailable")
			return
		}
		api.Write(w, 200, map[string]any{"phase": turn.Phase, "reply": turn.Reply, "citations": turn.Citations, "cases": turn.Cases, "selected": turn.Selected})
	case r.Method == "GET" && path == "logs":
		sender := r.URL.Query().Get("sender")
		if _, _, e = h.s.employee(r.Context(), Inbound{Tenant: a.OrganizationID, Sender: sender}); e != nil {
			api.Error(w, 403, "denied")
			return
		}
		messages, e := delivery.List(r.Context(), h.s.DB, a.OrganizationID, sender)
		if e != nil {
			api.Error(w, 503, "unavailable")
			return
		}
		result := []map[string]any{}
		for _, m := range messages {
			if m.Account != "synthetic" {
				continue
			}
			if h.s.AuthorizeDelivery(r.Context(), m) != nil {
				continue
			}
			result = append(result, map[string]any{"id": m.ID, "text": m.Text, "state": m.State, "case_id": m.CaseID})
		}
		api.Write(w, 200, result)
	default:
		api.Error(w, 404, "not_found")
	}
}
