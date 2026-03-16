package web

import (
	"embed"
	"encoding/json"
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/ioworker0/lore-reply/internal/config"
	"github.com/ioworker0/lore-reply/internal/mail"
)

//go:embed templates/index.html
var templateFS embed.FS

type pageData struct {
	DefaultFromName  string
	DefaultFromEmail string
	AutoLoadURL      string
}

type loadRequest struct {
	URL         string `json:"url"`
	FromName    string `json:"from_name"`
	FromEmail   string `json:"from_email"`
	ForceReload bool   `json:"force_reload"`
}

type errorResponse struct {
	Error  string `json:"error"`
	Output string `json:"output,omitempty"`
}

// New builds the HTTP handler for the lore-reply UI and APIs.
func New(cfg config.Config) (http.Handler, error) {
	tmpl, err := template.ParseFS(templateFS, "templates/index.html")
	if err != nil {
		return nil, err
	}

	server := &server{
		cfg: cfg,
		mail: mail.Service{
			B4Path:    cfg.B4Path,
			GitPath:   cfg.GitPath,
			DraftsDir: cfg.DraftsDir,
		},
		template: tmpl,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", server.handleIndex)
	mux.HandleFunc("POST /api/load", server.handleLoad)
	mux.HandleFunc("POST /api/save", server.handleSave)
	mux.HandleFunc("POST /api/send", server.handleSend)
	return mux, nil
}

type server struct {
	cfg      config.Config
	mail     mail.Service
	template *template.Template
}

func (s *server) handleIndex(writer http.ResponseWriter, request *http.Request) {
	_ = s.template.Execute(writer, pageData{
		DefaultFromName:  s.cfg.FromName,
		DefaultFromEmail: s.cfg.FromEmail,
		AutoLoadURL:      s.cfg.AutoLoadURL,
	})
}

func (s *server) handleLoad(writer http.ResponseWriter, request *http.Request) {
	var payload loadRequest
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		writeJSONError(writer, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	draft, err := s.mail.LoadDraft(request.Context(), mail.LoadOptions{
		URL:         payload.URL,
		FromName:    fallback(payload.FromName, s.cfg.FromName),
		FromEmail:   fallback(payload.FromEmail, s.cfg.FromEmail),
		ForceReload: payload.ForceReload,
	})
	if err != nil {
		var b4Err *mail.B4Error
		if errors.As(err, &b4Err) {
			writeJSONError(writer, http.StatusInternalServerError, b4Err.Error())
			return
		}
		writeJSONError(writer, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(writer, http.StatusOK, draft)
}

func (s *server) handleSave(writer http.ResponseWriter, request *http.Request) {
	var draft mail.Draft
	if err := json.NewDecoder(request.Body).Decode(&draft); err != nil {
		writeJSONError(writer, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	draft.FromName = fallback(draft.FromName, s.cfg.FromName)
	draft.FromEmail = fallback(draft.FromEmail, s.cfg.FromEmail)

	result, err := s.mail.SaveDraft(draft)
	if err != nil {
		writeJSONError(writer, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(writer, http.StatusOK, result)
}

func (s *server) handleSend(writer http.ResponseWriter, request *http.Request) {
	var draft mail.Draft
	if err := json.NewDecoder(request.Body).Decode(&draft); err != nil {
		writeJSONError(writer, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	draft.FromName = fallback(draft.FromName, s.cfg.FromName)
	draft.FromEmail = fallback(draft.FromEmail, s.cfg.FromEmail)

	result, err := s.mail.SendDraft(request.Context(), draft)
	if err != nil {
		var sendErr *mail.SendError
		if errors.As(err, &sendErr) {
			message := "git send-email failed"
			if strings.TrimSpace(result.Output) == "" {
				message = sendErr.Error()
			}
			writeJSONDetailedError(writer, http.StatusInternalServerError, message, result.Output)
			return
		}
		writeJSONError(writer, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(writer, http.StatusOK, result)
}

func fallback(value, defaultValue string) string {
	if strings.TrimSpace(value) == "" {
		return defaultValue
	}
	return value
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func writeJSONError(writer http.ResponseWriter, status int, message string) {
	writeJSONDetailedError(writer, status, message, "")
}

func writeJSONDetailedError(writer http.ResponseWriter, status int, message, output string) {
	writeJSON(writer, status, errorResponse{
		Error:  message,
		Output: output,
	})
}
