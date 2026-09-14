package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPChatSenderAvailableModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.6-luna","owned_by":"codex"},{"id":"kimi-k2.8","owned_by":"kimi"}]}`))
	}))
	defer srv.Close()

	sender := newHTTPChatSender(srv.URL, "sk-test")
	available, err := sender.AvailableModels(context.Background())
	if err != nil {
		t.Fatalf("AvailableModels: %v", err)
	}
	if !available["gpt-5.6-luna"] || !available["kimi-k2.8"] {
		t.Fatalf("available = %v, want both models present", available)
	}
}

func TestHTTPChatSenderListModelsDetailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5.6-luna","owned_by":"codex"},{"id":"","owned_by":"ignored"}]}`))
	}))
	defer srv.Close()

	sender := newHTTPChatSender(srv.URL, "sk-test")
	models, err := sender.ListModelsDetailed(context.Background())
	if err != nil {
		t.Fatalf("ListModelsDetailed: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.6-luna" || models[0].OwnedBy != "codex" {
		t.Fatalf("models = %+v, want exactly one gpt-5.6-luna/codex entry (blank ids skipped)", models)
	}
}

func TestHTTPChatSenderAvailableModelsUnreachable(t *testing.T) {
	sender := newHTTPChatSender("http://127.0.0.1:1", "sk-test") // nothing listens here
	if _, err := sender.AvailableModels(context.Background()); err == nil {
		t.Fatalf("expected an error for an unreachable base URL")
	}
}
