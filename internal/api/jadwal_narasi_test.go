package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"milkgraph/internal/ai"
)

func fp(v float64) *float64 { return &v }

func jadwalUjiNarasi() Schedule {
	return Schedule{
		ScheduleID:  "SCH-20261014-v2",
		Version:     2,
		GeneratedAt: "2026-10-14T14:42:00+07:00",
		TriggeredBy: "DOWNTIME",
		Window:      JendelaWaktu{Start: "2026-10-14T14:42:00+07:00", End: "2026-10-17T14:42:00+07:00"},
		Runs: []RunJadwal{
			{RunID: "RUN-001", POID: "PO-1", FMID: "FL-08", Start: "2026-10-14T17:42:00+07:00", End: "2026-10-14T20:42:00+07:00", QtyLiter: 6000},
			{RunID: "RUN-002", POID: "PO-2", FMID: "FL-05", Start: "2026-10-14T14:42:00+07:00", End: "2026-10-14T17:42:00+07:00", QtyLiter: 8000},
		},
		Fulfilment: []FulfilmentPO{
			{POID: "PO-2", Status: "AT_RISK", FulfilledPct: 1, PlannedCompletion: "2026-10-14T17:42:00+07:00", RootCauses: []string{"RT_LATE"}},
			{POID: "PO-1", Status: "LATE", FulfilledPct: 0.5, PlannedCompletion: "2026-10-15T09:00:00+07:00", RootCauses: []string{"DOWNTIME"}},
			{POID: "PO-3", Status: "FULFILLED", FulfilledPct: 1, PlannedCompletion: "2026-10-14T16:00:00+07:00", RootCauses: []string{}},
		},
		Warnings: []WarningJadwal{
			{Type: "HOLDING_TIME", ResourceID: "AT-03", LimitH: fp(10.5), ProjectedH: fp(20)},
			{Type: "TANK_STOCK_SHORT", ResourceID: "ASEPTIC-TANK", NeededL: fp(20000), AvailableL: fp(5000)},
		},
	}
}

func TestBangunExplainRequest(t *testing.T) {
	sch := jadwalUjiNarasi()
	prev := &RingkasanJadwal{ScheduleID: "SCH-20261014-v1", Version: 1, TriggeredBy: "manual", RunsTotal: 2, POLate: 0, POAtRisk: 1, Warnings: 1}
	triggers := []ScheduleTrigger{
		{ID: "TRG-001", ReasonCode: "RT_LATE", RefID: "PO-2", CreatedBy: "Uji"},
		{ID: "TRG-002", ReasonCode: "DOWNTIME", RefID: "FL-08", Detail: "FL-08 berhenti", CreatedBy: "Uji",
			Downtime: &DowntimeTrigger{ResourceID: "FL-08", Start: "2026-10-14T14:42:00+07:00", End: "2026-10-14T17:42:00+07:00"}},
	}
	req := bangunExplainRequest(sch, prev, triggers, "simulasi")

	if req.BatchID != "SCH-20261014-v2" || req.LabelData != "simulasi" {
		t.Errorf("batch_id/label salah: %+v", req)
	}
	if len(req.UrutanPrioritas) != 7 {
		t.Fatalf("urutan_prioritas = %d tindakan, mau 7 (utama+perubahan+3 PO+2 peringatan): %+v", len(req.UrutanPrioritas), req.UrutanPrioritas)
	}
	for i, a := range req.UrutanPrioritas {
		if a.Rank != i+1 {
			t.Errorf("rank tindakan %d = %d, harus berurutan dari 1", i, a.Rank)
		}
	}
	utama := req.UrutanPrioritas[0]
	if utama.AlasanKode != "DOWNTIME" {
		t.Errorf("alasan_kode tindakan utama = %q, mau DOWNTIME (triggered_by)", utama.AlasanKode)
	}
	if !strings.Contains(utama.Tindakan, "TRG-002") || !strings.Contains(utama.Tindakan, "FL-08 berhenti") {
		t.Errorf("tindakan utama harus memuat pemicu tercatat yang cocok: %q", utama.Tindakan)
	}
	gabung := ""
	for _, a := range req.UrutanPrioritas {
		gabung += a.Tindakan + "\n"
	}
	for _, mau := range []string{"Perubahan dari versi 1 ke versi 2", "RT_LATE", "AT-03", "20000"} {
		if !strings.Contains(gabung, mau) {
			t.Errorf("fakta %q tidak ditemukan di urutan_prioritas:\n%s", mau, gabung)
		}
	}
	// Urutan PO: LATE dulu, lalu AT_RISK, lalu FULFILLED.
	iLate, iRisk, iFull := -1, -1, -1
	kodePerPO := map[string]string{}
	kodePeringatan := map[string]bool{}
	for i, a := range req.UrutanPrioritas {
		switch {
		case strings.HasPrefix(a.Tindakan, "PO PO-1 "):
			iLate, kodePerPO["PO-1"] = i, a.AlasanKode
		case strings.HasPrefix(a.Tindakan, "PO PO-2 "):
			iRisk, kodePerPO["PO-2"] = i, a.AlasanKode
		case strings.HasPrefix(a.Tindakan, "PO PO-3 "):
			iFull = i
		}
		if a.AlasanKode == "HOLDING_TIME" || a.AlasanKode == "TANK_STOCK_SHORT" {
			kodePeringatan[a.AlasanKode] = true
		}
	}
	if !(0 <= iLate && iLate < iRisk && iRisk < iFull) {
		t.Errorf("urutan PO harus LATE < AT_RISK < FULFILLED: %d, %d, %d", iLate, iRisk, iFull)
	}
	if kodePerPO["PO-1"] != "DOWNTIME" || kodePerPO["PO-2"] != "RT_LATE" {
		t.Errorf("alasan_kode PO harus akar masalah pertamanya: %v", kodePerPO)
	}
	if !kodePeringatan["HOLDING_TIME"] || !kodePeringatan["TANK_STOCK_SHORT"] {
		t.Errorf("peringatan harus jadi tindakan dengan alasan_kode tipenya: %v", kodePeringatan)
	}
	if req.PelanggaranSebelum != 2 || req.PelanggaranSesudah != 4 {
		t.Errorf("pelanggaran = %d -> %d, mau 2 -> 4", req.PelanggaranSebelum, req.PelanggaranSesudah)
	}
	if req.JumlahConstraint != 5 {
		t.Errorf("jumlah_constraint = %d, mau 5 (2 run + 3 PO)", req.JumlahConstraint)
	}
}

func TestBangunExplainRequestTanpaVersiSebelumnya(t *testing.T) {
	sch := jadwalUjiNarasi()
	req := bangunExplainRequest(sch, nil, nil, "simulasi")
	if req.PelanggaranSebelum != req.PelanggaranSesudah || req.PelanggaranSesudah != 4 {
		t.Errorf("tanpa versi sebelumnya, sebelum harus sama dengan sesudah (4): %d -> %d", req.PelanggaranSebelum, req.PelanggaranSesudah)
	}
	for _, a := range req.UrutanPrioritas {
		if strings.Contains(a.Tindakan, "Perubahan dari versi") {
			t.Errorf("tanpa versi sebelumnya tidak boleh ada ringkasan perubahan: %q", a.Tindakan)
		}
		if strings.Contains(a.Tindakan, "pemicu tercatat") {
			t.Errorf("tanpa pemicu tercatat tidak boleh mengklaim ada: %q", a.Tindakan)
		}
	}
	if req.UrutanPrioritas[0].AlasanKode != "DOWNTIME" {
		t.Errorf("alasan_kode utama tetap dari triggered_by: %q", req.UrutanPrioritas[0].AlasanKode)
	}
}

func stubExplainAI(t *testing.T, tangkap *ai.ExplainRequest) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "token-uji" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/ai/repair-priority/explain" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if tangkap != nil {
			_ = json.NewDecoder(r.Body).Decode(tangkap)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"batch_id":"SCH-20261014-v2","penjelasan":"Penjelasan perubahan jadwal stub.","sumber":"jev","label_data":"simulasi","jev":{"keyakinan_skor":75,"risiko_jika_ditunda":0.83,"model":"jev-1.14.0"}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestNarasiJadwalTanpaKonfigurasi503(t *testing.T) {
	s := serverUjiAI(t, "", "")
	_, err := s.narasiJadwal(context.Background(), nil)
	var ae *apiError
	if !errors.As(err, &ae) || ae.status != 503 {
		t.Fatalf("narasiJadwal tanpa token harus 503, dapat %v", err)
	}
}

func TestNarasiJadwalTanpaJadwal404(t *testing.T) {
	stub := stubExplainAI(t, nil)
	s := serverUjiAI(t, stub.URL, "token-uji")
	_, err := s.narasiJadwal(context.Background(), nil)
	var ae *apiError
	if !errors.As(err, &ae) || ae.status != 404 {
		t.Fatalf("narasiJadwal tanpa jadwal tersimpan harus 404, dapat %v", err)
	}
}

func TestNarasiJadwalDenganDB(t *testing.T) {
	db := testDB(t)
	if err := db.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	var tangkap ai.ExplainRequest
	stub := stubExplainAI(t, &tangkap)
	h := (&Server{Mode: "demo", DB: db, AI: ai.New(stub.URL, "token-uji")}).Handler()

	w := durableRequest(h, "POST", "/schedules/generate", "planner", `{"triggered_by":"manual"}`)
	if w.Code != 200 {
		t.Fatalf("POST /schedules/generate = %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/schedules/triggers", "planner", `{"reason_code":"DOWNTIME","ref_id":"FL-08","detail":"FL-08 berhenti","downtime":{"resource_id":"FL-08","start":"2026-10-14T14:42:00+07:00","end":"2026-10-14T17:42:00+07:00"}}`)
	if w.Code != 200 {
		t.Fatalf("POST /schedules/triggers = %d: %s", w.Code, w.Body.String())
	}

	// Narasi versi terbaru (bawaan) diteruskan apa adanya dari service AI.
	w = durableRequest(h, "POST", "/schedules/narrate", "planner", `{}`)
	if w.Code != 200 {
		t.Fatalf("POST /schedules/narrate = %d: %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	if m["penjelasan"] != "Penjelasan perubahan jadwal stub." || m["sumber"] != "jev" {
		t.Errorf("respons narasi harus diteruskan dari service AI: %v", m)
	}
	if tangkap.BatchID != "SCH-20261014-v2" {
		t.Errorf("batch_id terkirim = %q, mau SCH-20261014-v2", tangkap.BatchID)
	}
	if len(tangkap.UrutanPrioritas) == 0 || tangkap.UrutanPrioritas[0].AlasanKode != "DOWNTIME" {
		t.Errorf("tindakan utama harus berkode DOWNTIME: %+v", tangkap.UrutanPrioritas)
	}
	if tangkap.LabelData != "simulasi" {
		t.Errorf("label_data terkirim = %q, mau simulasi", tangkap.LabelData)
	}
	gabung := ""
	for _, a := range tangkap.UrutanPrioritas {
		gabung += a.Tindakan + "\n"
	}
	if !strings.Contains(gabung, "Perubahan dari versi 1 ke versi 2") {
		t.Errorf("permintaan harus memuat ringkasan perubahan versi:\n%s", gabung)
	}

	// Versi eksplisit yang cocok dengan terbaru berhasil; versi lain 404.
	w = durableRequest(h, "POST", "/schedules/narrate", "planner", `{"version":2}`)
	if w.Code != 200 {
		t.Fatalf("POST /schedules/narrate versi 2 = %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/schedules/narrate", "planner", `{"version":7}`)
	if w.Code != 404 {
		t.Fatalf("POST /schedules/narrate versi 7 harus 404, dapat %d: %s", w.Code, w.Body.String())
	}

	// Service AI tidak terjangkau: 502, jadwal tidak ikut error.
	hDown := (&Server{Mode: "demo", DB: db, AI: ai.New("http://127.0.0.1:1", "token-uji")}).Handler()
	w = durableRequest(hDown, "POST", "/schedules/narrate", "planner", `{}`)
	if w.Code != 502 {
		t.Fatalf("POST /schedules/narrate saat AI down harus 502, dapat %d: %s", w.Code, w.Body.String())
	}
}
