package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestChatMengirimTokenDanMemparsingRespons(t *testing.T) {
	var gotToken, gotPath string
	var gotBody ChatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Service-Token")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jawaban":"Jawaban uji.","sumber":"jev","label_data":"simulasi"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "token-uji")
	resp, err := c.Chat(context.Background(), ChatRequest{
		Pertanyaan:   "Kenapa batch FG-1013-07 ditolak QC?",
		KonteksFakta: []string{"Status QC: out-of-spec"},
		LabelData:    "simulasi",
	})
	if err != nil {
		t.Fatalf("Chat error: %v", err)
	}
	if gotToken != "token-uji" {
		t.Errorf("header X-Service-Token tidak terkirim")
	}
	if gotPath != "/ai/chat" {
		t.Errorf("path = %q, mau /ai/chat", gotPath)
	}
	if gotBody.Pertanyaan == "" || len(gotBody.KonteksFakta) != 1 || gotBody.LabelData != "simulasi" {
		t.Errorf("body terkirim tidak sesuai: %+v", gotBody)
	}
	if resp.Jawaban != "Jawaban uji." || resp.Sumber != "jev" || resp.LabelData != "simulasi" {
		t.Errorf("respons terparsing tidak sesuai: %+v", resp)
	}
}

func TestChatTanpaTokenError(t *testing.T) {
	c := New("", "")
	if _, err := c.Chat(context.Background(), ChatRequest{Pertanyaan: "apa?"}); err == nil {
		t.Fatal("Chat tanpa token harus error, bukan diam-diam berhasil")
	}
}
