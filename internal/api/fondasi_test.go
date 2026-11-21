package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSelisihTransfer(t *testing.T) {
	kasus := []struct {
		out, in, mau float64
	}{
		{12200, 12000, 200},
		{10000, 9900, 100},
		{500, 500, 0},
		{100.5, 99.25, 1.25},
		{9900, 10000, -100},
	}
	for _, k := range kasus {
		if got := selisihTransfer(k.out, k.in); got != k.mau {
			t.Errorf("selisihTransfer(%v, %v) = %v, mau %v", k.out, k.in, got, k.mau)
		}
	}
}

func TestHitungLateMenit(t *testing.T) {
	kasus := []struct {
		estimated, target string
		mau               int
	}{
		{"2026-10-14T14:00:00+07:00", "2026-10-14T13:25:00+07:00", 35},
		{"2026-10-14T13:00:00+07:00", "2026-10-14T13:10:00+07:00", -10},
		{"2026-10-14T13:10:00+07:00", "2026-10-14T13:10:00+07:00", 0},
		{"bukan waktu", "2026-10-14T13:10:00+07:00", 0},
	}
	for _, k := range kasus {
		if got := hitungLateMenit(k.estimated, k.target); got != k.mau {
			t.Errorf("hitungLateMenit(%q, %q) = %d, mau %d", k.estimated, k.target, got, k.mau)
		}
	}
}

func TestSeedRTArrivals(t *testing.T) {
	s := serverUjiAI(t, "", "")
	list := s.seedRTArrivals()
	if len(list) != 8 {
		t.Fatalf("seed kedatangan = %d, mau 8 tanker skenario", len(list))
	}
	byRT := map[string]RTArrival{}
	for _, a := range list {
		if a.Status != "Sudah Tiba" {
			t.Errorf("%s: status seed = %q, mau Sudah Tiba", a.RTID, a.Status)
		}
		if a.ActualArrival == nil {
			t.Errorf("%s: actual_arrival seed tidak boleh kosong", a.RTID)
		}
		byRT[a.RTID] = a
	}
	rt26, ok := byRT["RT-26"]
	if !ok {
		t.Fatal("seed harus memuat RT-26")
	}
	if rt26.LateMinutes != 35 {
		t.Errorf("RT-26 late_minutes = %d, mau 35", rt26.LateMinutes)
	}
	if rt26.VolumeL != 11400 || rt26.FarmName != "KUD Ngantang" {
		t.Errorf("RT-26 tidak mengikuti data tanker skenario: %+v", rt26)
	}
	if got := byRT["RT-24"].LateMinutes; got != 20 {
		t.Errorf("RT-24 late_minutes = %d, mau 20", got)
	}
	if got := byRT["RT-23"].LateMinutes; got != 0 {
		t.Errorf("RT-23 late_minutes = %d, mau 0", got)
	}
}

func stubChatAI(t *testing.T, tangkap *aiChatRequestTangkap) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Service-Token") != "token-uji" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if tangkap != nil {
			_ = json.NewDecoder(r.Body).Decode(tangkap)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jawaban":"Jawaban chat stub.","sumber":"jev","label_data":"simulasi"}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

type aiChatRequestTangkap struct {
	Pertanyaan   string   `json:"pertanyaan"`
	KonteksFakta []string `json:"konteks_fakta"`
	LabelData    string   `json:"label_data"`
}

func TestChatAI(t *testing.T) {
	var tangkap aiChatRequestTangkap
	stub := stubChatAI(t, &tangkap)
	s := serverUjiAI(t, stub.URL, "token-uji")

	out, err := s.chatAI(context.Background(), "Kenapa batch FG-1013-07 ditolak QC?")
	if err != nil {
		t.Fatalf("chatAI error: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("chatAI harus mengembalikan map, dapat %T", out)
	}
	if m["jawaban"] != "Jawaban chat stub." || m["sumber"] != "jev" {
		t.Errorf("isi jawaban chat tidak sesuai: %v", m)
	}
	if tangkap.Pertanyaan != "Kenapa batch FG-1013-07 ditolak QC?" {
		t.Errorf("pertanyaan terkirim = %q", tangkap.Pertanyaan)
	}
	if tangkap.LabelData != "simulasi" {
		t.Errorf("label_data terkirim = %q, mau simulasi", tangkap.LabelData)
	}
	if len(tangkap.KonteksFakta) == 0 || !strings.Contains(tangkap.KonteksFakta[0], "out-of-spec") {
		t.Errorf("konteks_fakta harus diawali status QC out-of-spec: %v", tangkap.KonteksFakta)
	}
}

func TestChatAITanpaKonfigurasi503(t *testing.T) {
	s := serverUjiAI(t, "", "")
	_, err := s.chatAI(context.Background(), "apa?")
	var ae *apiError
	if !errors.As(err, &ae) || ae.status != 503 {
		t.Fatalf("chatAI tanpa token harus 503, dapat %v", err)
	}
}

func TestChatAISaatAIDown502(t *testing.T) {
	s := serverUjiAI(t, "http://127.0.0.1:1", "token-uji")
	_, err := s.chatAI(context.Background(), "apa?")
	var ae *apiError
	if !errors.As(err, &ae) || ae.status != 502 {
		t.Fatalf("chatAI saat AI down harus 502, dapat %v", err)
	}
}

func TestFondasiDinamisButuhDB(t *testing.T) {
	h := (&Server{Mode: "demo"}).Handler()
	w := durableRequest(h, "POST", "/trace/transfers", "planner", `{"batch_id":"FG-1013-07","from_ref":"SP-1","to_ref":"ST-4","volume_out_l":12200,"volume_in_l":12000}`)
	if w.Code != 503 {
		t.Fatalf("POST transfer tanpa DB harus 503, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/supply/rt-arrivals", "planner", `{"farm_name":"Farm Tutur","rt_id":"RT-99","volume_l":5000,"depart_at":"2026-10-14T13:00:00+07:00","target_arrival":"2026-10-14T15:00:00+07:00","estimated_arrival":"2026-10-14T15:10:00+07:00"}`)
	if w.Code != 503 {
		t.Fatalf("POST rt-arrivals tanpa DB harus 503, dapat %d: %s", w.Code, w.Body.String())
	}
}

func TestFondasiDinamisDenganDB(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	if err := db.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Mode: "demo", DB: db}).Handler()

	// Transfer: selisih dihitung server.
	w := durableRequest(h, "POST", "/trace/transfers", "planner", `{"batch_id":"FG-1013-07","from_ref":"SP-1","to_ref":"ST-4","volume_out_l":12200,"volume_in_l":12000}`)
	if w.Code != 200 {
		t.Fatalf("POST transfer = %d: %s", w.Code, w.Body.String())
	}
	rec := obj(decodeMap(t, w)["result"])
	if rec["selisih_l"] != 200.0 || rec["source"] != "Manual" || rec["id"] != "TRF-001" {
		t.Errorf("record transfer tidak sesuai: %v", rec)
	}
	w = durableRequest(h, "POST", "/trace/transfers", "planner", `{"batch_id":"LOT-LAIN","from_ref":"TH-1","to_ref":"MX-2","volume_out_l":5000,"volume_in_l":4950,"source":"IoT"}`)
	if w.Code != 200 {
		t.Fatalf("POST transfer kedua = %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "GET", "/trace/transfers?batch_id=FG-1013-07", "planner", "")
	var filtered []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &filtered); err != nil || len(filtered) != 1 {
		t.Fatalf("GET transfer terfilter harus 1 baris: %s", w.Body.String())
	}
	w = durableRequest(h, "GET", "/trace/transfers", "planner", "")
	var semua []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &semua); err != nil || len(semua) != 2 {
		t.Fatalf("GET transfer harus 2 baris: %s", w.Body.String())
	}
	if semua[0]["batch_id"] != "LOT-LAIN" {
		t.Errorf("transfer terbaru harus di depan: %v", semua[0]["batch_id"])
	}

	// Kedatangan: seed 8 tanker tampil, buat baru, lalu update estimasi.
	w = durableRequest(h, "GET", "/supply/rt-arrivals", "planner", "")
	var arrivals []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &arrivals); err != nil || len(arrivals) != 8 {
		t.Fatalf("GET rt-arrivals awal harus berisi 8 seed: %s", w.Body.String())
	}
	w = durableRequest(h, "POST", "/supply/rt-arrivals", "planner", `{"farm_name":"Farm Tutur","rt_id":"RT-99","volume_l":5000,"depart_at":"2026-10-14T13:00:00+07:00","target_arrival":"2026-10-14T15:00:00+07:00","estimated_arrival":"2026-10-14T15:10:00+07:00"}`)
	if w.Code != 200 {
		t.Fatalf("POST rt-arrivals = %d: %s", w.Code, w.Body.String())
	}
	created := obj(decodeMap(t, w)["result"])
	if created["id"] != "ARR-RT-99" || created["late_minutes"] != 10.0 || created["status"] != "Dijadwalkan" {
		t.Errorf("kedatangan baru tidak sesuai: %v", created)
	}
	w = durableRequest(h, "PUT", "/supply/rt-arrivals/ARR-RT-99", "planner", `{"estimated_arrival":"2026-10-14T15:40:00+07:00","status":"Dalam Perjalanan"}`)
	if w.Code != 200 {
		t.Fatalf("PUT rt-arrivals = %d: %s", w.Code, w.Body.String())
	}
	updated := obj(decodeMap(t, w)["result"])
	if updated["late_minutes"] != 40.0 || updated["status"] != "Dalam Perjalanan" {
		t.Errorf("kedatangan terupdate tidak sesuai: %v", updated)
	}
	w = durableRequest(h, "GET", "/supply/rt-arrivals", "planner", "")
	arrivals = nil
	if err := json.Unmarshal(w.Body.Bytes(), &arrivals); err != nil || len(arrivals) != 9 {
		t.Fatalf("GET rt-arrivals akhir harus 9 baris: %s", w.Body.String())
	}
}
