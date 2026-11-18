package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNarrateRootCauseMengirimTokenDanMemparsingRespons(t *testing.T) {
	var gotToken, gotPath string
	var gotBody NarrateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Service-Token")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"batch_id": "FG-1013-07",
			"penyebab_utama": "SUMBER_RT04",
			"narasi": "Narasi uji.",
			"rekomendasi": ["Periksa RT-04"],
			"sumber": "jev",
			"label_data": "simulasi",
			"jev": {"pilihan": "SUMBER_RT04", "confidence": 0.9},
			"selisih_dengan_be": false
		}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "token-uji")
	if !c.Enabled() {
		t.Fatal("client dengan token harus Enabled")
	}
	resp, err := c.NarrateRootCause(context.Background(), NarrateRequest{
		BatchID:   "FG-1013-07",
		Komoditas: "susu",
		Konteks:   Context{QCStatus: "out-of-spec", KakiPerjalanan: []Leg{}, PelanggaranBatas: []Violation{}},
		Kandidat:  []Candidate{{Kode: "SUMBER_RT04", Skor: 0.78, Bukti: []string{"bukti"}}},
		LabelData: "simulasi",
	})
	if err != nil {
		t.Fatalf("NarrateRootCause error: %v", err)
	}
	if gotToken != "token-uji" {
		t.Errorf("header X-Service-Token = %q, mau %q", gotToken, "token-uji")
	}
	if gotPath != "/ai/root-cause/narrate" {
		t.Errorf("path = %q, mau /ai/root-cause/narrate", gotPath)
	}
	if gotBody.BatchID != "FG-1013-07" || len(gotBody.Kandidat) != 1 || gotBody.Kandidat[0].Kode != "SUMBER_RT04" {
		t.Errorf("body terkirim tidak sesuai: %+v", gotBody)
	}
	if resp.PenyebabUtama != "SUMBER_RT04" || resp.Sumber != "jev" || resp.SelisihDenganBE {
		t.Errorf("respons terparsing tidak sesuai: %+v", resp)
	}
	var jev struct {
		Pilihan string `json:"pilihan"`
	}
	if err := json.Unmarshal(resp.Jev, &jev); err != nil || jev.Pilihan != "SUMBER_RT04" {
		t.Errorf("field jev mentah tidak utuh: %s", string(resp.Jev))
	}
}

func TestClientTanpaTokenNonaktif(t *testing.T) {
	c := New("", "")
	if c.Enabled() {
		t.Fatal("client tanpa token harus nonaktif")
	}
	if _, err := c.NarrateRootCause(context.Background(), NarrateRequest{}); err == nil {
		t.Fatal("panggilan tanpa token harus error, bukan diam-diam berhasil")
	}
	if c.baseURL != DefaultURL {
		t.Errorf("baseURL default = %q, mau %q", c.baseURL, DefaultURL)
	}
}

func TestNarrateRootCauseStatusBukan200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := New(srv.URL, "token-uji")
	if _, err := c.NarrateRootCause(context.Background(), NarrateRequest{}); err == nil {
		t.Fatal("status 503 dari layanan AI harus menjadi error")
	}
}
