package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Parameter uji scheduler: dua mesin sama-sama mampu menjalankan dua SKU
// cokelat uji, rate mengikuti contoh coaching (250 ml 4000 L/jam, 1000 ml
// 6000 L/jam), changeover 45 menit dua arah dari matriks uji.
func paramsUjiJadwal() (ParamsJadwal, map[string]InfoSKU) {
	info := map[string]InfoSKU{
		"SKU-250":  {SizeML: 250, NamaTampil: "UHT Cokelat 250 ml"},
		"SKU-1000": {SizeML: 1000, NamaTampil: "UHT Cokelat 1000 ml"},
	}
	p := ParamsJadwal{
		Machines: []MesinJadwal{
			{ID: "FM-A", SKUCapable: []string{"SKU-250", "SKU-1000"}, BackupID: "FM-B", TangkiSumber: "AT-01"},
			{ID: "FM-B", SKUCapable: []string{"SKU-250", "SKU-1000"}, BackupID: "FM-A", TangkiSumber: "AT-02"},
		},
		RateLPerHour: map[int]float64{250: 4000, 1000: 6000},
		Changeover: map[string]int{
			"UHT Cokelat 250 ml>UHT Cokelat 1000 ml": 45,
			"UHT Cokelat 1000 ml>UHT Cokelat 250 ml": 45,
		},
		HoldingLimitH: 10.5,
	}
	return p, info
}

func inputUjiJadwal(pos []ProductionOrder) InputJadwal {
	p, info := paramsUjiJadwal()
	return InputJadwal{
		POs:           pos,
		Params:        p,
		SKUInfo:       info,
		WindowStart:   waktuDemo,
		Version:       1,
		TriggeredBy:   "manual",
		StokTersediaL: 1_000_000,
	}
}

func TestSchedulerChangeoverDiFMYangSama(t *testing.T) {
	in := inputUjiJadwal([]ProductionOrder{{
		POID: "PO-1", Priority: 1,
		Lines:       []POLine{{SKU: "SKU-250", QtyLiter: 12000}, {SKU: "SKU-1000", QtyLiter: 8000}},
		DueDatetime: "2026-11-24T08:00:00+07:00",
	}})
	// Paksa satu mesin supaya kedua run pasti berbagi FM yang sama.
	in.Params.Machines = in.Params.Machines[:1]
	sch := bangunJadwal(in)
	if len(sch.Runs) != 2 {
		t.Fatalf("jumlah run = %d, mau 2: %+v", len(sch.Runs), sch.Runs)
	}
	r1, r2 := sch.Runs[0], sch.Runs[1]
	if r1.SKU != "SKU-250" || r2.SKU != "SKU-1000" {
		t.Fatalf("urutan run salah: %s lalu %s", r1.SKU, r2.SKU)
	}
	if r1.FMID != "FM-A" || r2.FMID != "FM-A" {
		t.Fatalf("kedua run harus di FM-A: %s, %s", r1.FMID, r2.FMID)
	}
	if r1.ChangeoverAfterMin != 45 {
		t.Errorf("changeover_after_min run pertama = %d, mau 45", r1.ChangeoverAfterMin)
	}
	end1, _ := parseWaktu(r1.End)
	start2, _ := parseWaktu(r2.Start)
	if selisih := start2.Sub(end1); selisih != 45*time.Minute {
		t.Errorf("jeda antar run = %v, mau 45 menit (changeover)", selisih)
	}
}

func TestSchedulerHanyaFMCapable(t *testing.T) {
	in := inputUjiJadwal([]ProductionOrder{{
		POID: "PO-1", Priority: 1,
		Lines:       []POLine{{SKU: "SKU-250", QtyLiter: 4000}, {SKU: "SKU-1000", QtyLiter: 6000}},
		DueDatetime: "2026-11-24T08:00:00+07:00",
	}})
	in.Params.Machines[0].SKUCapable = []string{"SKU-1000"} // FM-A hanya 1000
	in.Params.Machines[1].SKUCapable = []string{"SKU-250"}  // FM-B hanya 250
	sch := bangunJadwal(in)
	if len(sch.Runs) != 2 {
		t.Fatalf("jumlah run = %d, mau 2", len(sch.Runs))
	}
	for _, r := range sch.Runs {
		if r.SKU == "SKU-250" && r.FMID != "FM-B" {
			t.Errorf("SKU-250 harus di FM-B, dapat %s", r.FMID)
		}
		if r.SKU == "SKU-1000" && r.FMID != "FM-A" {
			t.Errorf("SKU-1000 harus di FM-A, dapat %s", r.FMID)
		}
	}
}

func TestSchedulerDowntimeFailoverKeBackup(t *testing.T) {
	in := inputUjiJadwal([]ProductionOrder{{
		POID: "PO-1", Priority: 1,
		Lines:       []POLine{{SKU: "SKU-250", QtyLiter: 12000}},
		DueDatetime: "2026-11-24T08:00:00+07:00",
	}})
	in.Downtime = []JendelaDowntime{{
		ResourceID: "FM-A",
		Start:      waktuDemo,
		End:        waktuDemo.Add(3 * time.Hour),
	}}
	sch := bangunJadwal(in)
	if len(sch.Runs) != 1 {
		t.Fatalf("jumlah run = %d, mau 1", len(sch.Runs))
	}
	if sch.Runs[0].FMID != "FM-B" {
		t.Errorf("run harus failover ke backup FM-B selama FM-A downtime, dapat %s", sch.Runs[0].FMID)
	}
	if sch.Runs[0].Start != formatWaktu(waktuDemo) {
		t.Errorf("run di backup harus mulai di awal jendela, dapat %s", sch.Runs[0].Start)
	}
}

func TestSchedulerDowntimeMenggeserRun(t *testing.T) {
	in := inputUjiJadwal([]ProductionOrder{{
		POID: "PO-1", Priority: 1,
		Lines:       []POLine{{SKU: "SKU-250", QtyLiter: 12000}},
		DueDatetime: "2026-11-24T08:00:00+07:00",
	}})
	in.Params.Machines[1].SKUCapable = []string{"SKU-1000"} // FM-B tidak mampu 250
	in.Downtime = []JendelaDowntime{{
		ResourceID: "FM-A",
		Start:      waktuDemo,
		End:        waktuDemo.Add(3 * time.Hour),
	}}
	sch := bangunJadwal(in)
	if len(sch.Runs) != 1 {
		t.Fatalf("jumlah run = %d, mau 1", len(sch.Runs))
	}
	mauMulai := formatWaktu(waktuDemo.Add(3 * time.Hour))
	if sch.Runs[0].Start != mauMulai {
		t.Errorf("run harus tergeser ke akhir downtime %s, dapat %s", mauMulai, sch.Runs[0].Start)
	}
	if len(sch.Fulfilment) != 1 || sch.Fulfilment[0].Status != "AT_RISK" {
		t.Fatalf("fulfilment harus AT_RISK karena tergeser downtime: %+v", sch.Fulfilment)
	}
	if len(sch.Fulfilment[0].RootCauses) != 1 || sch.Fulfilment[0].RootCauses[0] != "DOWNTIME" {
		t.Errorf("root_causes harus [DOWNTIME]: %v", sch.Fulfilment[0].RootCauses)
	}
}

func TestSchedulerIDVersi(t *testing.T) {
	in := inputUjiJadwal(nil)
	sch := bangunJadwal(in)
	if sch.ScheduleID != "SCH-20261014-v1" {
		t.Errorf("schedule_id = %q, mau SCH-20261014-v1", sch.ScheduleID)
	}
	in.Version = 2
	sch = bangunJadwal(in)
	if sch.ScheduleID != "SCH-20261014-v2" || sch.Version != 2 {
		t.Errorf("versi 2 salah: %q v%d", sch.ScheduleID, sch.Version)
	}
	if sch.Window.End != formatWaktu(waktuDemo.Add(3*24*time.Hour)) {
		t.Errorf("akhir jendela harus start+3 hari: %s", sch.Window.End)
	}
}

func TestSchedulerFulfilmentLate(t *testing.T) {
	in := inputUjiJadwal([]ProductionOrder{{
		POID: "PO-1", Priority: 1,
		Lines:       []POLine{{SKU: "SKU-250", QtyLiter: 12000}}, // 3 jam, selesai 17.42
		DueDatetime: "2026-10-14T15:42:00+07:00",                 // due 1 jam setelah jendela mulai
	}})
	sch := bangunJadwal(in)
	if len(sch.Fulfilment) != 1 {
		t.Fatalf("fulfilment = %d, mau 1", len(sch.Fulfilment))
	}
	f := sch.Fulfilment[0]
	if f.Status != "LATE" {
		t.Errorf("status = %q, mau LATE (selesai %s melewati due)", f.Status, f.PlannedCompletion)
	}
	if f.FulfilledPct != 1 {
		t.Errorf("fulfilled_pct = %v, mau 1 (terjadwal penuh tapi telat)", f.FulfilledPct)
	}
}

func TestSchedulerFulfilledDanOnTrack(t *testing.T) {
	// Due di dalam jendela dan selesai tepat waktu: FULFILLED.
	in := inputUjiJadwal([]ProductionOrder{{
		POID: "PO-1", Priority: 1,
		Lines:       []POLine{{SKU: "SKU-250", QtyLiter: 4000}},
		DueDatetime: "2026-10-15T08:00:00+07:00",
	}})
	sch := bangunJadwal(in)
	if sch.Fulfilment[0].Status != "FULFILLED" {
		t.Errorf("status = %q, mau FULFILLED: %+v", sch.Fulfilment[0].Status, sch.Fulfilment[0])
	}
	// Due di luar jendela 3 hari: ON_TRACK.
	in = inputUjiJadwal([]ProductionOrder{{
		POID: "PO-1", Priority: 1,
		Lines:       []POLine{{SKU: "SKU-250", QtyLiter: 4000}},
		DueDatetime: "2026-11-24T08:00:00+07:00",
	}})
	sch = bangunJadwal(in)
	if sch.Fulfilment[0].Status != "ON_TRACK" {
		t.Errorf("status = %q, mau ON_TRACK: %+v", sch.Fulfilment[0].Status, sch.Fulfilment[0])
	}
}

func TestSchedulerPeringatanHoldingDanStok(t *testing.T) {
	in := inputUjiJadwal([]ProductionOrder{{
		POID: "PO-1", Priority: 1,
		Lines:       []POLine{{SKU: "SKU-250", QtyLiter: 12000}, {SKU: "SKU-1000", QtyLiter: 8000}},
		DueDatetime: "2026-11-24T08:00:00+07:00",
	}})
	in.StokTersediaL = 5000 // kebutuhan 20000, stok 5000
	siap := waktuDemo.Add(-20 * time.Hour)
	in.SusuSiapPada = &siap
	sch := bangunJadwal(in)
	var stok, holding *WarningJadwal
	for i, w := range sch.Warnings {
		switch w.Type {
		case "TANK_STOCK_SHORT":
			stok = &sch.Warnings[i]
		case "HOLDING_TIME":
			holding = &sch.Warnings[i]
		}
	}
	if stok == nil {
		t.Fatalf("peringatan TANK_STOCK_SHORT tidak muncul: %+v", sch.Warnings)
	}
	if *stok.NeededL != 20000 || *stok.AvailableL != 5000 {
		t.Errorf("stok: needed %v available %v, mau 20000/5000", *stok.NeededL, *stok.AvailableL)
	}
	if holding == nil {
		t.Fatalf("peringatan HOLDING_TIME tidak muncul: %+v", sch.Warnings)
	}
	if *holding.LimitH != 10.5 || *holding.ProjectedH != 20 {
		t.Errorf("holding: limit %v projected %v, mau 10.5/20", *holding.LimitH, *holding.ProjectedH)
	}
}

func TestParamsJadwalDariMasterNyata(t *testing.T) {
	seeds, err := LoadAllSeeds()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{seeds: seeds}
	params, info, err := s.paramsJadwal(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	mesin := map[string]MesinJadwal{}
	for _, m := range params.Machines {
		mesin[m.ID] = m
	}
	if !mengandung(mesin["FL-05"].SKUCapable, "UHT-CH-1000-LOCAL") {
		t.Errorf("FL-05 harus capable UHT-CH-1000-LOCAL: %v", mesin["FL-05"].SKUCapable)
	}
	if !mengandung(mesin["FL-08"].SKUCapable, "UHT-CH-250-LOCAL") {
		t.Errorf("FL-08 harus capable UHT-CH-250-LOCAL: %v", mesin["FL-08"].SKUCapable)
	}
	if mesin["FL-05"].BackupID != "FL-06" || mesin["FL-05"].TangkiSumber != "AT-03" {
		t.Errorf("FL-05 backup/tangki salah: %+v", mesin["FL-05"])
	}
	if info["UHT-CH-250-LOCAL"].SizeML != 250 || info["UHT-CH-1000-LOCAL"].SizeML != 1000 {
		t.Errorf("info SKU salah: %v / %v", info["UHT-CH-250-LOCAL"], info["UHT-CH-1000-LOCAL"])
	}
	if params.Changeover["UHT Cokelat 200 ml>UHT Full Cream 1 L"] != 45 {
		t.Errorf("matriks changeover SOP harus terbaca dari limits.json: %v", params.Changeover)
	}
	stok := 0.0
	for _, tk := range params.Tanks {
		stok += tk.LevelL
	}
	sch := bangunJadwal(InputJadwal{
		POs: s.seedPOs(), Params: params, SKUInfo: info,
		WindowStart: waktuDemo, Version: 1, TriggeredBy: "manual", StokTersediaL: stok,
	})
	if len(sch.Runs) != 2 {
		t.Fatalf("jadwal PO seed harus 2 run: %+v", sch.Runs)
	}
	for _, r := range sch.Runs {
		if r.SKU == "UHT-CH-250-LOCAL" && !mengandung([]string{"FL-08", "FL-09", "FL-10"}, r.FMID) {
			t.Errorf("run 250 ml harus di line small pack, dapat %s", r.FMID)
		}
		if r.SKU == "UHT-CH-1000-LOCAL" && !mengandung([]string{"FL-05", "FL-06"}, r.FMID) {
			t.Errorf("run 1000 ml harus di line large pack, dapat %s", r.FMID)
		}
	}
	if len(sch.Fulfilment) != 1 || sch.Fulfilment[0].Status != "ON_TRACK" || sch.Fulfilment[0].FulfilledPct != 1 {
		t.Errorf("fulfilment PO seed harus ON_TRACK 100%%: %+v", sch.Fulfilment)
	}
	if len(sch.Warnings) != 0 {
		t.Errorf("jadwal seed tidak boleh memuat peringatan: %+v", sch.Warnings)
	}
}

func TestJadwalButuhDB(t *testing.T) {
	h := (&Server{Mode: "demo"}).Handler()
	w := durableRequest(h, "POST", "/pos", "planner", `{"lines":[{"sku":"UHT-CH-250-LOCAL","qty_liter":1000}],"due_datetime":"2026-10-15T08:00:00+07:00"}`)
	if w.Code != 503 {
		t.Fatalf("POST /pos tanpa DB harus 503, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/schedules/generate", "planner", `{}`)
	if w.Code != 503 {
		t.Fatalf("POST /schedules/generate tanpa DB harus 503, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/schedules/triggers", "planner", `{"reason_code":"PO_CHANGED"}`)
	if w.Code != 503 {
		t.Fatalf("POST /schedules/triggers tanpa DB harus 503, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "GET", "/schedules/latest", "planner", "")
	if w.Code != 404 {
		t.Fatalf("GET /schedules/latest tanpa jadwal harus 404, dapat %d: %s", w.Code, w.Body.String())
	}
	// GET /pos tanpa DB tetap melayani seed demo.
	w = durableRequest(h, "GET", "/pos", "planner", "")
	if w.Code != 200 {
		t.Fatalf("GET /pos tanpa DB harus 200, dapat %d: %s", w.Code, w.Body.String())
	}
	var pos []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &pos); err != nil || len(pos) != 1 {
		t.Fatalf("GET /pos awal harus berisi 1 PO seed: %s", w.Body.String())
	}
	if pos[0]["po_id"] != "PO-2026-1101" || pos[0]["label_data"] != "simulasi" {
		t.Errorf("PO seed tidak sesuai: %v", pos[0])
	}
}

func TestJadwalDenganDB(t *testing.T) {
	db := testDB(t)
	if err := db.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Mode: "demo", DB: db}).Handler()

	// Seed PO demo tampil lebih dulu.
	w := durableRequest(h, "GET", "/pos", "planner", "")
	var pos []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &pos); err != nil || len(pos) != 1 {
		t.Fatalf("GET /pos awal harus 1 PO seed: %s", w.Body.String())
	}
	lines := arr(pos[0]["lines"])
	if len(lines) != 2 || str(obj(lines[0])["sku"]) != "UHT-CH-250-LOCAL" || str(obj(lines[1])["sku"]) != "UHT-CH-1000-LOCAL" {
		t.Fatalf("baris PO seed harus SKU cokelat master: %v", pos[0]["lines"])
	}

	// Buat PO baru; SKU tak dikenal ditolak.
	w = durableRequest(h, "POST", "/pos", "planner", `{"po_id":"PO-UJI-1","priority":2,"lines":[{"sku":"UHT-CH-250-LOCAL","qty_liter":4000}],"due_datetime":"2026-10-15T08:00:00+07:00"}`)
	if w.Code != 200 {
		t.Fatalf("POST /pos = %d: %s", w.Code, w.Body.String())
	}
	if got := obj(decodeMap(t, w)["result"]); got["po_id"] != "PO-UJI-1" {
		t.Errorf("PO baru tidak sesuai: %v", got)
	}
	w = durableRequest(h, "POST", "/pos", "planner", `{"lines":[{"sku":"SKU-NGIBUL","qty_liter":100}],"due_datetime":"2026-10-15T08:00:00+07:00"}`)
	if w.Code != 422 {
		t.Fatalf("POST /pos SKU asing harus 422, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/pos", "planner", `{"po_id":"PO-UJI-1","lines":[{"sku":"UHT-CH-250-LOCAL","qty_liter":100}],"due_datetime":"2026-10-15T08:00:00+07:00"}`)
	if w.Code != 422 {
		t.Fatalf("POST /pos po_id duplikat harus 422, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "GET", "/pos", "planner", "")
	pos = nil
	if err := json.Unmarshal(w.Body.Bytes(), &pos); err != nil || len(pos) != 2 {
		t.Fatalf("GET /pos akhir harus 2 PO: %s", w.Body.String())
	}

	// Generate versi 1.
	w = durableRequest(h, "POST", "/schedules/generate", "planner", `{"triggered_by":"manual"}`)
	if w.Code != 200 {
		t.Fatalf("POST /schedules/generate = %d: %s", w.Code, w.Body.String())
	}
	sch := obj(decodeMap(t, w)["result"])
	if sch["schedule_id"] != "SCH-20261014-v1" || sch["version"] != 1.0 {
		t.Fatalf("jadwal versi 1 tidak sesuai: %v", sch["schedule_id"])
	}
	if len(arr(sch["runs"])) < 3 {
		t.Errorf("jadwal harus memuat minimal 3 run (2 seed + 1 uji): %v", sch["runs"])
	}
	for _, f := range arr(sch["fulfilment"]) {
		fm := obj(f)
		if fm["po_id"] == "PO-2026-1101" && fm["fulfilled_pct"] != 1.0 {
			t.Errorf("fulfilment PO seed harus 100%%: %v", fm)
		}
	}

	// Trigger downtime memicu regenerasi ke versi 2.
	w = durableRequest(h, "POST", "/schedules/triggers", "planner", `{"reason_code":"DOWNTIME","ref_id":"FL-08","detail":"FL-08 berhenti","downtime":{"resource_id":"FL-08","start":"2026-10-14T14:42:00+07:00","end":"2026-10-14T17:42:00+07:00"}}`)
	if w.Code != 200 {
		t.Fatalf("POST /schedules/triggers = %d: %s", w.Code, w.Body.String())
	}
	sch2 := obj(decodeMap(t, w)["result"])
	if sch2["version"] != 2.0 || sch2["triggered_by"] != "DOWNTIME" {
		t.Errorf("jadwal hasil trigger harus versi 2 oleh DOWNTIME: %v", sch2)
	}
	w = durableRequest(h, "GET", "/schedules/latest", "planner", "")
	latest := decodeMap(t, w)
	if latest["version"] != 2.0 {
		t.Errorf("latest harus versi 2: %v", latest["version"])
	}
	w = durableRequest(h, "GET", "/schedules", "planner", "")
	var riwayat []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &riwayat); err != nil || len(riwayat) != 2 {
		t.Fatalf("riwayat jadwal harus 2 versi: %s", w.Body.String())
	}
}
