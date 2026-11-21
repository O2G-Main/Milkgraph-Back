package api

import (
	"encoding/json"
	"testing"
	"time"
)

func evDowntime(resource string, mulai time.Time, durasiJam float64) DowntimeEvent {
	return DowntimeEvent{
		ResourceID: resource,
		Start:      formatWaktu(mulai),
		End:        formatWaktu(mulai.Add(time.Duration(durasiJam * float64(time.Hour)))),
		Type:       "planned_maintenance",
		Source:     "user_input",
	}
}

func TestPrediksiDowntimeRiwayat(t *testing.T) {
	base := waktuDemo
	events := []DowntimeEvent{
		evDowntime("FL-01", base, 4),
		evDowntime("FL-01", base.AddDate(0, 0, 50), 4),
		evDowntime("FL-01", base.AddDate(0, 0, 95), 4),
		evDowntime("FL-02", base.AddDate(0, 0, 10), 4), // resource lain, diabaikan
	}
	p := prediksiDowntime("FL-01", events, nil, base)
	if p.Basis != "history" {
		t.Fatalf("basis = %q, mau history", p.Basis)
	}
	if p.EventsCount != 3 {
		t.Errorf("events_count = %d, mau 3", p.EventsCount)
	}
	if len(p.IntervalsDays) != 2 || p.IntervalsDays[0] != 50 || p.IntervalsDays[1] != 45 {
		t.Fatalf("intervals = %v, mau [50 45]", p.IntervalsDays)
	}
	if p.AvgIntervalDays == nil || *p.AvgIntervalDays != 47.5 {
		t.Fatalf("avg_interval_days = %v, mau 47.5", p.AvgIntervalDays)
	}
	mauPrediksi := formatWaktu(base.AddDate(0, 0, 142).Add(12 * time.Hour))
	if p.PredictedNext == nil || *p.PredictedNext != mauPrediksi {
		t.Errorf("predicted_next = %v, mau %s", p.PredictedNext, mauPrediksi)
	}
	if p.DaysRemaining == nil || *p.DaysRemaining != 142.5 {
		t.Errorf("days_remaining = %v, mau 142.5", p.DaysRemaining)
	}
	if p.Alert != "AMAN" {
		t.Errorf("alert = %q, mau AMAN", p.Alert)
	}
	if p.LastEventAt == nil || *p.LastEventAt != formatWaktu(base.AddDate(0, 0, 95)) {
		t.Errorf("last_event_at = %v, mau hari ke-95", p.LastEventAt)
	}
}

func TestPrediksiDowntimeAlertMendekatiDanLewat(t *testing.T) {
	// Interval sama [50 45], rata-rata 47.5 hari, ambang MENDEKATI 9.5 hari.
	mendekati := []DowntimeEvent{
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -135), 4),
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -85), 4),
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -40), 4),
	}
	p := prediksiDowntime("FL-01", mendekati, nil, waktuDemo)
	if p.DaysRemaining == nil || *p.DaysRemaining != 7.5 {
		t.Fatalf("days_remaining = %v, mau 7.5", p.DaysRemaining)
	}
	if p.Alert != "MENDEKATI" {
		t.Errorf("alert = %q, mau MENDEKATI", p.Alert)
	}

	lewat := []DowntimeEvent{
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -155), 4),
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -105), 4),
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -60), 4),
	}
	p = prediksiDowntime("FL-01", lewat, nil, waktuDemo)
	if p.DaysRemaining == nil || *p.DaysRemaining != -12.5 {
		t.Fatalf("days_remaining = %v, mau -12.5", p.DaysRemaining)
	}
	if p.Alert != "LEWAT" {
		t.Errorf("alert = %q, mau LEWAT", p.Alert)
	}
}

func TestPrediksiDowntimeKurangData(t *testing.T) {
	p := prediksiDowntime("FL-01", nil, nil, waktuDemo)
	if p.Basis != "insufficient_data" || p.EventsCount != 0 || p.AvgIntervalDays != nil || p.PredictedNext != nil {
		t.Fatalf("tanpa kejadian harus insufficient_data: %+v", p)
	}
	if p.Alert != "INSUFFICIENT_DATA" {
		t.Errorf("alert = %q, mau INSUFFICIENT_DATA", p.Alert)
	}

	satu := []DowntimeEvent{evDowntime("FL-01", waktuDemo.AddDate(0, 0, -10), 4)}
	p = prediksiDowntime("FL-01", satu, nil, waktuDemo)
	if p.Basis != "insufficient_data" || p.EventsCount != 1 || len(p.IntervalsDays) != 0 {
		t.Fatalf("satu kejadian harus insufficient_data: %+v", p)
	}
	if p.LastEventAt == nil {
		t.Error("last_event_at harus terisi walau data belum cukup")
	}

	// Dua kejadian hanya memberi 1 interval: belum cukup untuk rata-rata.
	dua := []DowntimeEvent{
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -60), 4),
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -10), 4),
	}
	p = prediksiDowntime("FL-01", dua, nil, waktuDemo)
	if p.Basis != "insufficient_data" || len(p.IntervalsDays) != 1 || p.IntervalsDays[0] != 50 {
		t.Fatalf("dua kejadian harus insufficient_data dengan 1 interval: %+v", p)
	}
}

func TestPrediksiDowntimeIntervalKonfigurasi(t *testing.T) {
	cfg := 30.0
	satu := []DowntimeEvent{evDowntime("FL-01", waktuDemo.AddDate(0, 0, -10), 4)}
	p := prediksiDowntime("FL-01", satu, &cfg, waktuDemo)
	if p.Basis != "configured" {
		t.Fatalf("basis = %q, mau configured", p.Basis)
	}
	if p.AvgIntervalDays == nil || *p.AvgIntervalDays != 30 {
		t.Fatalf("avg_interval_days = %v, mau 30", p.AvgIntervalDays)
	}
	mau := formatWaktu(waktuDemo.AddDate(0, 0, 20))
	if p.PredictedNext == nil || *p.PredictedNext != mau {
		t.Errorf("predicted_next = %v, mau %s", p.PredictedNext, mau)
	}
	if p.Alert != "AMAN" { // sisa 20 hari > 20% x 30 = 6 hari
		t.Errorf("alert = %q, mau AMAN", p.Alert)
	}

	// Satu interval riwayat tetap kalah oleh konfigurasi (belum 2 interval).
	dua := []DowntimeEvent{
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -60), 4),
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -10), 4),
	}
	p = prediksiDowntime("FL-01", dua, &cfg, waktuDemo)
	if p.Basis != "configured" || p.AvgIntervalDays == nil || *p.AvgIntervalDays != 30 {
		t.Fatalf("satu interval + konfigurasi harus basis configured avg 30: %+v", p)
	}
}

func jadwalUjiRekomendasi() Schedule {
	return Schedule{
		ScheduleID: "SCH-20261014-v1",
		Version:    1,
		Window:     JendelaWaktu{Start: formatWaktu(waktuDemo), End: formatWaktu(waktuDemo.AddDate(0, 0, 3))},
		Runs: []RunJadwal{
			{RunID: "RUN-001", FMID: "FL-01", Start: formatWaktu(waktuDemo), End: formatWaktu(waktuDemo.Add(4 * time.Hour))},
			{RunID: "RUN-002", FMID: "FL-01", Start: formatWaktu(waktuDemo.Add(24 * time.Hour)), End: formatWaktu(waktuDemo.Add(28 * time.Hour))},
			{RunID: "RUN-003", FMID: "FL-02", Start: formatWaktu(waktuDemo), End: formatWaktu(waktuDemo.Add(70 * time.Hour))},
		},
	}
}

func TestRekomendasiMaintenanceCelahJadwal(t *testing.T) {
	sch := jadwalUjiRekomendasi()
	pred := prediksiDowntime("FL-01", nil, nil, waktuDemo) // tanpa prediksi
	rec, ok := rekomendasiMaintenance("FL-01", &sch, pred, 4*time.Hour)
	if !ok {
		t.Fatal("harus ada rekomendasi dari celah jadwal")
	}
	// Celah pertama FL-01: sesudah RUN-001 (demo+4j) s/d RUN-002 (demo+24j).
	if rec.RecommendedStart != formatWaktu(waktuDemo.Add(4*time.Hour)) || rec.RecommendedEnd != formatWaktu(waktuDemo.Add(8*time.Hour)) {
		t.Errorf("jendela = %s s/d %s, mau demo+4j s/d demo+8j", rec.RecommendedStart, rec.RecommendedEnd)
	}
	if rec.DurationHours != 4 {
		t.Errorf("duration_hours = %v, mau 4", rec.DurationHours)
	}
}

func TestRekomendasiMaintenanceFallbackPrediksi(t *testing.T) {
	events := []DowntimeEvent{
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -135), 4),
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -85), 4),
		evDowntime("FL-01", waktuDemo.AddDate(0, 0, -40), 4),
	}
	pred := prediksiDowntime("FL-01", events, nil, waktuDemo) // prediksi: demo + 7.5 hari
	rec, ok := rekomendasiMaintenance("FL-01", nil, pred, 4*time.Hour)
	if !ok {
		t.Fatal("tanpa jadwal, prediksi harus memberi rekomendasi fallback")
	}
	mauAkhir := waktuDemo.AddDate(0, 0, 7).Add(12 * time.Hour)
	if rec.RecommendedEnd != formatWaktu(mauAkhir) || rec.RecommendedStart != formatWaktu(mauAkhir.Add(-4*time.Hour)) {
		t.Errorf("jendela fallback = %s s/d %s, mau berakhir di prediksi %s", rec.RecommendedStart, rec.RecommendedEnd, formatWaktu(mauAkhir))
	}
}

func TestRekomendasiMaintenanceCelahTerpotongPrediksi(t *testing.T) {
	sch := jadwalUjiRekomendasi()
	// Prediksi 6 jam dari demo: celah FL-01 pertama (demo+4j, perlu 4 jam ->
	// berakhir demo+8j) melewati prediksi, jadi fallback yang dipakai.
	prediksiStr := formatWaktu(waktuDemo.Add(6 * time.Hour))
	pred := PrediksiDowntime{ResourceID: "FL-01", PredictedNext: &prediksiStr, Basis: "history"}
	rec, ok := rekomendasiMaintenance("FL-01", &sch, pred, 4*time.Hour)
	if !ok {
		t.Fatal("harus jatuh ke fallback prediksi")
	}
	if rec.RecommendedEnd != prediksiStr || rec.RecommendedStart != formatWaktu(waktuDemo.Add(2*time.Hour)) {
		t.Errorf("jendela = %s s/d %s, mau demo+2j s/d %s", rec.RecommendedStart, rec.RecommendedEnd, prediksiStr)
	}
}

func TestRekomendasiMaintenanceTidakAda(t *testing.T) {
	pred := prediksiDowntime("FL-01", nil, nil, waktuDemo)
	if _, ok := rekomendasiMaintenance("FL-01", nil, pred, 4*time.Hour); ok {
		t.Error("tanpa jadwal dan tanpa prediksi tidak boleh ada rekomendasi")
	}
}

func TestDowntimeButuhDB(t *testing.T) {
	h := (&Server{Mode: "demo"}).Handler()
	w := durableRequest(h, "POST", "/downtime-events", "operator", `{"resource_id":"FL-01","start":"2026-10-14T08:00:00+07:00","end":"2026-10-14T12:00:00+07:00","type":"breakdown"}`)
	if w.Code != 503 {
		t.Fatalf("POST /downtime-events tanpa DB harus 503, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "GET", "/downtime-events", "planner", "")
	if w.Code != 200 {
		t.Fatalf("GET /downtime-events tanpa DB harus 200, dapat %d: %s", w.Code, w.Body.String())
	}
	var list []DowntimeEvent
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 0 {
		t.Fatalf("GET /downtime-events tanpa DB harus daftar kosong: %s", w.Body.String())
	}
	w = durableRequest(h, "GET", "/downtime-events/prediction?resource_id=FL-01", "planner", "")
	if w.Code != 200 {
		t.Fatalf("GET prediction tanpa DB harus 200, dapat %d: %s", w.Code, w.Body.String())
	}
	p := decodeMap(t, w)
	if p["basis"] != "insufficient_data" || p["events_count"] != 0.0 {
		t.Errorf("prediksi tanpa riwayat harus insufficient_data: %v", p)
	}
	w = durableRequest(h, "GET", "/downtime-events/prediction", "planner", "")
	if w.Code != 422 {
		t.Fatalf("GET prediction tanpa resource_id harus 422, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "GET", "/downtime-events/prediction?resource_id=FL-01&interval_days=abc", "planner", "")
	if w.Code != 422 {
		t.Fatalf("GET prediction interval_days rusak harus 422, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "GET", "/maintenance/recommendation?resource_id=FL-01", "planner", "")
	if w.Code != 404 {
		t.Fatalf("GET recommendation tanpa jadwal/prediksi harus 404, dapat %d: %s", w.Code, w.Body.String())
	}
}

func TestDowntimeDenganDB(t *testing.T) {
	db := testDB(t)
	if err := db.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Mode: "demo", DB: db}).Handler()

	// Validasi: tipe tidak dikenal dan end <= start ditolak 422.
	w := durableRequest(h, "POST", "/downtime-events", "operator", `{"resource_id":"FL-01","start":"2026-09-01T08:00:00+07:00","end":"2026-09-01T12:00:00+07:00","type":"ngawur"}`)
	if w.Code != 422 {
		t.Fatalf("POST tipe ngawur harus 422, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/downtime-events", "operator", `{"resource_id":"FL-01","start":"2026-09-01T12:00:00+07:00","end":"2026-09-01T08:00:00+07:00","type":"breakdown"}`)
	if w.Code != 422 {
		t.Fatalf("POST end sebelum start harus 422, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/downtime-events", "operator", `{"resource_id":"FL-01","start":"2026-09-01T08:00:00+07:00","end":"2026-09-01T12:00:00+07:00","type":"breakdown","source":"alien"}`)
	if w.Code != 422 {
		t.Fatalf("POST source ngawur harus 422, dapat %d: %s", w.Code, w.Body.String())
	}

	// Tiga kejadian FL-99 berjarak 50 dan 45 hari dari 14 Okt 2026 14.42:
	// mulai 10 Jun, 30 Jul, 13 Sep (dibulatkan ke jam demo).
	mulai := []string{"2026-06-10T14:42:00+07:00", "2026-07-30T14:42:00+07:00", "2026-09-13T14:42:00+07:00"}
	for i, m := range mulai {
		body := `{"resource_id":"FL-99","start":"` + m + `","end":"` + m[:11] + `18:42:00+07:00","type":"planned_maintenance"}`
		w = durableRequest(h, "POST", "/downtime-events", "operator", body)
		if w.Code != 200 {
			t.Fatalf("POST downtime %d = %d: %s", i, w.Code, w.Body.String())
		}
		created := obj(decodeMap(t, w)["result"])
		if created["event_id"] != "DTE-00"+itoa(i+1) || created["source"] != "user_input" {
			t.Errorf("kejadian %d tidak sesuai: %v", i, created)
		}
	}
	w = durableRequest(h, "GET", "/downtime-events?resource_id=FL-99", "planner", "")
	var list []DowntimeEvent
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 3 {
		t.Fatalf("GET downtime FL-99 harus 3 kejadian: %s", w.Body.String())
	}
	w = durableRequest(h, "GET", "/downtime-events?resource_id=FL-LAIN", "planner", "")
	list = nil
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) != 0 {
		t.Fatalf("GET downtime filter lain harus kosong: %s", w.Body.String())
	}

	// Prediksi dari riwayat: interval [50 45], rata-rata 47.5.
	w = durableRequest(h, "GET", "/downtime-events/prediction?resource_id=FL-99", "planner", "")
	p := decodeMap(t, w)
	if p["basis"] != "history" || p["avg_interval_days"] != 47.5 || p["events_count"] != 3.0 {
		t.Fatalf("prediksi FL-99 tidak sesuai: %v", p)
	}

	// Rekomendasi: FL-99 tidak punya run, jadi seluruh jendela jadwal adalah
	// celah; prediksi berikutnya (31 Okt) sesudah akhir jendela (17 Okt).
	w = durableRequest(h, "POST", "/schedules/generate", "planner", `{"triggered_by":"manual"}`)
	if w.Code != 200 {
		t.Fatalf("POST /schedules/generate = %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "GET", "/maintenance/recommendation?resource_id=FL-99", "planner", "")
	if w.Code != 200 {
		t.Fatalf("GET recommendation FL-99 = %d: %s", w.Code, w.Body.String())
	}
	rec := decodeMap(t, w)
	if rec["recommended_start"] != "2026-10-14T14:42:00+07:00" || rec["recommended_end"] != "2026-10-14T18:42:00+07:00" {
		t.Errorf("rekomendasi FL-99 harus awal jendela + 4 jam: %v", rec)
	}
	if rec["duration_hours"] != 4.0 {
		t.Errorf("duration_hours = %v, mau 4", rec["duration_hours"])
	}
}
