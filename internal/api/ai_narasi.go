package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"milkgraph/internal/ai"
)

// Integrasi layanan AI (Kontrak API AI MilkGraph v2): Backend mengirim fakta
// yang ia hitung sendiri ke service AI, service membalas narasi plus
// penilaian terstruktur Jev. Kegagalan service AI tidak pernah menggagalkan
// respons Backend; pemanggil selalu jatuh ke perilaku lokal.

// lampirkanNarasiAI memperkaya jawaban trace backward (akar masalah
// FG-1013-07) dengan field "aiNarasi" dari service AI bila aktif.
func (s *Server) lampirkanNarasiAI(ctx context.Context, answer map[string]any) {
	if s.AI == nil || !s.AI.Enabled() || str(answer["id"]) != "backward" {
		return
	}
	payload, ok := s.payloadNarasiFG()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	resp, err := s.AI.NarrateRootCause(ctx, payload)
	if err != nil {
		slog.Warn("narasi AI tidak tersedia, jawaban lokal dipakai", "error", err)
		return
	}
	answer["aiNarasi"] = keMap(resp)
}

// narasiAI melayani POST /api/v1/assistant/narrate: narasi akar masalah satu
// batch dari service AI. Saat ini hanya FG-1013-07 yang memiliki fakta akar
// masalah terkalibrasi di Backend.
func (s *Server) narasiAI(ctx context.Context, batchID string) (any, error) {
	if batchID != "FG-1013-07" {
		return nil, missing()
	}
	if s.AI == nil || !s.AI.Enabled() {
		return nil, &apiError{503, "UNAVAILABLE", "Layanan AI belum dikonfigurasi.", ""}
	}
	payload, ok := s.payloadNarasiFG()
	if !ok {
		return nil, &apiError{503, "UNAVAILABLE", "Fakta batch tidak tersedia.", ""}
	}
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	resp, err := s.AI.NarrateRootCause(ctx, payload)
	if err != nil {
		slog.Warn("panggilan layanan AI gagal", "error", err)
		return nil, &apiError{502, "AI_UNAVAILABLE", "Layanan AI tidak dapat dihubungi.", ""}
	}
	return keMap(resp), nil
}

// payloadNarasiFG menyusun payload kontrak dari fakta skenario Backend untuk
// FG-1013-07: status QC dari qcEvent, kaki perjalanan dari handoffs (durasi
// dihitung dari jendela waktunya), dan peringkat kandidat akar masalah versi
// Backend. RT-04 peringkat pertama karena ia satu-satunya sumber yang
// screening antibiotiknya mendekati batas deteksi (LAB-4471) dan lot ini
// mengandung 12% susu RT-04, sesuai kesimpulan trace backward.
func (s *Server) payloadNarasiFG() (ai.NarrateRequest, bool) {
	if s.seeds == nil || s.seeds.Scenario == nil {
		return ai.NarrateRequest{}, false
	}
	sc := s.seeds.Scenario
	qc := obj(sc["qcEvent"])
	if str(qc["lot"]) != "FG-1013-07" {
		return ai.NarrateRequest{}, false
	}
	status := "dalam spesifikasi"
	if strings.Contains(str(qc["result"]), "Positif") {
		status = "out-of-spec"
	}
	legs := []ai.Leg{}
	for _, raw := range arr(sc["handoffs"]) {
		h := obj(raw)
		dur, ok := durasiJendela(str(h["window"]))
		if !ok {
			continue
		}
		legs = append(legs, ai.Leg{Dari: str(h["from"]), Ke: str(h["to"]), DurasiMenit: dur})
	}
	return ai.NarrateRequest{
		BatchID:   "FG-1013-07",
		Komoditas: "susu",
		Konteks: ai.Context{
			QCStatus:         status,
			KakiPerjalanan:   legs,
			PelanggaranBatas: []ai.Violation{},
		},
		Kandidat: []ai.Candidate{
			{Kode: "SUMBER_RT04", Skor: 0.78, Bukti: []string{
				"Screening antibiotik RT-04 (Farm Ngadirejo) lolos tetapi mendekati batas deteksi (LAB-4471)",
				"Lot FG-1013-07 mengandung 12% susu dari RT-04",
				"Residu antibiotik beta-laktam positif pada FG-1013-07, di atas batas (LAB-8812)",
			}},
			{Kode: "SUMBER_RT_LAIN", Skor: 0.18, Bukti: []string{
				"RT-02, RT-06, dan RT-09 menyumbang 88% volume RMT-2 tanpa temuan lab",
			}},
			{Kode: "KONTAMINASI_PROSES", Skor: 0.12, Bukti: []string{
				"Jalur proses RMT-2 ke TH-1, SP-1, ST-4, MX-2, UHT-2, AT-2 tercatat normal di historian",
			}},
		},
		LabelData: "simulasi",
	}, true
}

var polaJendela = regexp.MustCompile(`(\d{1,2})\.(\d{2})\s*-\s*(\d{1,2})\.(\d{2})`)

// durasiJendela menghitung menit dari jendela waktu handoff, misalnya
// "13 Okt 04.30 - 05.40" menjadi 70.
func durasiJendela(window string) (float64, bool) {
	m := polaJendela.FindStringSubmatch(window)
	if m == nil {
		return 0, false
	}
	jam := func(s string) int { n, _ := strconv.Atoi(s); return n }
	menit := func(s string) int { n, _ := strconv.Atoi(s); return n }
	mulai := jam(m[1])*60 + menit(m[2])
	selesai := jam(m[3])*60 + menit(m[4])
	d := selesai - mulai
	if d < 0 {
		d += 24 * 60
	}
	return float64(d), true
}

func keMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil
	}
	return m
}
