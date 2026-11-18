package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"milkgraph/internal/ai"
)

func serverUjiAI(t *testing.T, url, token string) *Server {
	t.Helper()
	sc, err := loadJSON("scenario.json")
	if err != nil {
		t.Fatalf("muat scenario.json: %v", err)
	}
	return &Server{seeds: &SeedData{Scenario: sc}, AI: ai.New(url, token)}
}

func TestPayloadNarasiFG(t *testing.T) {
	s := serverUjiAI(t, "", "")
	p, ok := s.payloadNarasiFG()
	if !ok {
		t.Fatal("payloadNarasiFG harus berhasil untuk skenario bawaan")
	}
	if p.BatchID != "FG-1013-07" || p.LabelData != "simulasi" {
		t.Errorf("batch/label salah: %+v", p)
	}
	if p.Konteks.QCStatus != "out-of-spec" {
		t.Errorf("qc_status = %q, mau out-of-spec", p.Konteks.QCStatus)
	}
	if len(p.Konteks.KakiPerjalanan) != 7 {
		t.Errorf("kaki perjalanan = %d, mau 7", len(p.Konteks.KakiPerjalanan))
	}
	if got := p.Konteks.KakiPerjalanan[0].DurasiMenit; got != 70 {
		t.Errorf("durasi leg pertama = %v, mau 70", got)
	}
	if len(p.Kandidat) == 0 || p.Kandidat[0].Kode != "SUMBER_RT04" {
		t.Errorf("kandidat teratas harus SUMBER_RT04: %+v", p.Kandidat)
	}
}

func stubAI(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "token-uji" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"batch_id":"FG-1013-07","penyebab_utama":"SUMBER_RT04","narasi":"Narasi stub.","rekomendasi":["Periksa RT-04"],"sumber":"jev","label_data":"simulasi","jev":{"pilihan":"SUMBER_RT04"},"selisih_dengan_be":false}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLampirkanNarasiAI(t *testing.T) {
	stub := stubAI(t)
	s := serverUjiAI(t, stub.URL, "token-uji")

	jawaban := map[string]any{"id": "backward"}
	s.lampirkanNarasiAI(context.Background(), jawaban)
	narasi, ok := jawaban["aiNarasi"].(map[string]any)
	if !ok {
		t.Fatalf("jawaban backward harus membawa aiNarasi: %v", jawaban)
	}
	if narasi["sumber"] != "jev" || narasi["penyebab_utama"] != "SUMBER_RT04" {
		t.Errorf("isi aiNarasi tidak sesuai: %v", narasi)
	}

	lain := map[string]any{"id": "unknown"}
	s.lampirkanNarasiAI(context.Background(), lain)
	if _, ada := lain["aiNarasi"]; ada {
		t.Error("jawaban non-backward tidak boleh membawa aiNarasi")
	}
}

func TestLampirkanNarasiAISaatAIDown(t *testing.T) {
	s := serverUjiAI(t, "http://127.0.0.1:1", "token-uji")
	jawaban := map[string]any{"id": "backward"}
	s.lampirkanNarasiAI(context.Background(), jawaban)
	if _, ada := jawaban["aiNarasi"]; ada {
		t.Error("AI down tidak boleh mengubah jawaban lokal")
	}
}

func TestDurasiJendela(t *testing.T) {
	kasus := []struct {
		jendela string
		mau     float64
		ok      bool
	}{
		{"13 Okt 04.30 - 05.40", 70, true},
		{"13 Okt 23.50 - 00.10", 20, true},
		{"tanpa jam", 0, false},
	}
	for _, k := range kasus {
		got, ok := durasiJendela(k.jendela)
		if ok != k.ok || (ok && got != k.mau) {
			t.Errorf("durasiJendela(%q) = %v, %v; mau %v, %v", k.jendela, got, ok, k.mau, k.ok)
		}
	}
}
