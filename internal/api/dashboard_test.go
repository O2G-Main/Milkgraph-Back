package api

import (
	"testing"
)

func TestTotalSusuDiterima(t *testing.T) {
	arrivals := []RTArrival{
		{FarmID: "FARM-A", FarmName: "Farm A", VolumeL: 10000, Status: "Sudah Tiba"},
		{FarmID: "FARM-A", FarmName: "Farm A", VolumeL: 5000, Status: "Sudah Tiba"},
		{FarmID: "FARM-A", FarmName: "Farm A", VolumeL: 7000, Status: "Dalam Perjalanan"},
		{FarmID: "FARM-B", FarmName: "Farm B", VolumeL: 3000, Status: "Sudah Tiba"},
		{FarmID: "FARM-C", FarmName: "Farm C", VolumeL: 9000, Status: "Dibatalkan"},
	}
	got := totalSusuDiterima(arrivals)
	if got.Total != 18000 {
		t.Errorf("total = %v, mau 18000 (hanya yang Sudah Tiba)", got.Total)
	}
	if len(got.ByFarm) != 2 {
		t.Fatalf("by_farm = %d farm, mau 2: %+v", len(got.ByFarm), got.ByFarm)
	}
	if got.ByFarm[0].FarmID != "FARM-A" || got.ByFarm[0].TotalL != 15000 || got.ByFarm[0].FarmName != "Farm A" {
		t.Errorf("baris FARM-A salah: %+v", got.ByFarm[0])
	}
	if got.ByFarm[1].FarmID != "FARM-B" || got.ByFarm[1].TotalL != 3000 {
		t.Errorf("baris FARM-B salah: %+v", got.ByFarm[1])
	}
}

func TestUtilisasiFM(t *testing.T) {
	sch := Schedule{
		Window: JendelaWaktu{Start: "2026-10-14T14:42:00+07:00", End: "2026-10-17T14:42:00+07:00"}, // 72 jam
		Runs: []RunJadwal{
			{RunID: "RUN-001", FMID: "FL-05", Start: "2026-10-14T14:42:00+07:00", End: "2026-10-14T17:42:00+07:00"}, // 3 jam
			{RunID: "RUN-002", FMID: "FL-05", Start: "2026-10-15T08:00:00+07:00", End: "2026-10-15T12:00:00+07:00"}, // 4 jam
			{RunID: "RUN-003", FMID: "FL-06", Start: "2026-10-14T15:00:00+07:00", End: "2026-10-14T17:00:00+07:00"}, // 2 jam
		},
	}
	triggers := []ScheduleTrigger{
		{ID: "TRG-001", ReasonCode: "DOWNTIME", Downtime: &DowntimeTrigger{ResourceID: "FL-05", Start: "2026-10-14T13:42:00+07:00", End: "2026-10-14T15:42:00+07:00"}}, // irisan 1 jam
		{ID: "TRG-002", ReasonCode: "DOWNTIME", Downtime: &DowntimeTrigger{ResourceID: "FL-05", Start: "2026-10-15T00:00:00+07:00", End: "2026-10-15T02:00:00+07:00"}}, // 2 jam
		{ID: "TRG-003", ReasonCode: "DOWNTIME", Downtime: &DowntimeTrigger{ResourceID: "FL-99", Start: "2026-10-20T00:00:00+07:00", End: "2026-10-20T02:00:00+07:00"}}, // di luar jendela
		{ID: "TRG-004", ReasonCode: "RT_LATE", RefID: "PO-1"},
	}
	got := utilisasiFM(sch, triggers)
	if len(got) != 2 {
		t.Fatalf("utilisasi = %d FM, mau 2 (FL-99 di luar jendela tidak tampil): %+v", len(got), got)
	}
	fl05, fl06 := got[0], got[1]
	if fl05.FMID != "FL-05" || fl05.UsedHours != 7 || fl05.DowntimeHours != 3 || fl05.Pct != 9.7 {
		t.Errorf("FL-05 salah: %+v, mau used 7, downtime 3, pct 9.7", fl05)
	}
	if fl06.FMID != "FL-06" || fl06.UsedHours != 2 || fl06.DowntimeHours != 0 || fl06.Pct != 2.8 {
		t.Errorf("FL-06 salah: %+v, mau used 2, downtime 0, pct 2.8", fl06)
	}
}

func TestRingkasanPODanTargetHarian(t *testing.T) {
	sch := Schedule{
		ScheduleID: "SCH-20261014-v1", Version: 1,
		Window: JendelaWaktu{Start: "2026-10-14T14:42:00+07:00", End: "2026-10-17T14:42:00+07:00"},
		Runs: []RunJadwal{
			{RunID: "RUN-001", POID: "PO-1", Start: "2026-10-14T14:42:00+07:00", End: "2026-10-14T17:42:00+07:00", QtyLiter: 12000},
			{RunID: "RUN-002", POID: "PO-1", Start: "2026-10-15T08:00:00+07:00", End: "2026-10-15T09:30:00+07:00", QtyLiter: 8000},
		},
		Fulfilment: []FulfilmentPO{
			{POID: "PO-1", Status: "FULFILLED"},
			{POID: "PO-2", Status: "ON_TRACK"},
			{POID: "PO-3", Status: "AT_RISK"},
			{POID: "PO-4", Status: "LATE"},
		},
	}
	pos := []ProductionOrder{
		{POID: "PO-1", DueDatetime: "2026-10-15T08:00:00+07:00", Lines: []POLine{{SKU: "SKU-250", QtyLiter: 12000}, {SKU: "SKU-1000", QtyLiter: 8000}}},
		{POID: "PO-2", DueDatetime: "2026-11-24T08:00:00+07:00", Lines: []POLine{{SKU: "SKU-250", QtyLiter: 4000}}},
	}
	r := ringkasanPO(sch)
	if r.Fulfilled != 1 || r.AtRisk != 1 || r.Late != 1 || r.OnTrack != 1 || len(r.Fulfilment) != 4 {
		t.Errorf("ringkasan PO salah: %+v", r)
	}
	h := jadwalHarian(sch, pos)
	if h.ScheduleID != "SCH-20261014-v1" || h.RunsCount != 2 || len(h.Targets) != 3 {
		t.Fatalf("jadwal harian salah: %+v", h)
	}
	if h.Targets[0].Date != "2026-10-14" || h.Targets[0].DueLiter != 0 || h.Targets[0].ScheduledLiter != 12000 {
		t.Errorf("target 14 Okt salah: %+v", h.Targets[0])
	}
	if h.Targets[1].Date != "2026-10-15" || h.Targets[1].DueLiter != 20000 || h.Targets[1].ScheduledLiter != 8000 {
		t.Errorf("target 15 Okt salah: %+v", h.Targets[1])
	}
	if h.Targets[2].Date != "2026-10-16" || h.Targets[2].DueLiter != 0 || h.Targets[2].ScheduledLiter != 0 {
		t.Errorf("target 16 Okt salah: %+v", h.Targets[2])
	}
}

func TestDashboardTanpaJadwal(t *testing.T) {
	srv := &Server{Mode: "demo"}
	h := srv.Handler()
	w := durableRequest(h, "GET", "/dashboard/executive", "planner", "")
	if w.Code != 200 {
		t.Fatalf("GET dashboard tanpa jadwal harus 200, dapat %d: %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	if m["daily_schedule"] != nil || m["po_summary"] != nil {
		t.Errorf("tanpa jadwal, daily_schedule dan po_summary harus null: %v", m)
	}
	if got := arr(m["machine_utilization"]); len(got) != 0 {
		t.Errorf("tanpa jadwal, utilisasi harus kosong: %v", got)
	}
	want := 0.0
	for _, a := range srv.seedRTArrivals() {
		want += a.VolumeL
	}
	susu := obj(m["milk_received_l"])
	if susu["total"] != want {
		t.Errorf("total susu = %v, mau %v dari seed kedatangan", susu["total"], want)
	}
	if len(arr(susu["by_farm"])) == 0 {
		t.Errorf("by_farm tidak boleh kosong untuk seed: %v", susu)
	}
}

func TestDashboardDenganDB(t *testing.T) {
	db := testDB(t)
	if err := db.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Mode: "demo", DB: db}).Handler()

	w := durableRequest(h, "POST", "/schedules/generate", "planner", `{"triggered_by":"manual"}`)
	if w.Code != 200 {
		t.Fatalf("POST /schedules/generate = %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "GET", "/dashboard/executive", "planner", "")
	if w.Code != 200 {
		t.Fatalf("GET dashboard = %d: %s", w.Code, w.Body.String())
	}
	m := decodeMap(t, w)
	harian := obj(m["daily_schedule"])
	if harian["schedule_id"] != "SCH-20261014-v1" || harian["runs_count"] != 2.0 {
		t.Errorf("daily_schedule tidak mengikuti jadwal terbaru: %v", harian)
	}
	ringkasan := obj(m["po_summary"])
	if len(arr(ringkasan["fulfilment"])) != 1 || ringkasan["on_track"] != 1.0 {
		t.Errorf("po_summary harus 1 PO seed ON_TRACK: %v", ringkasan)
	}
	if len(arr(m["machine_utilization"])) == 0 {
		t.Errorf("utilisasi tidak boleh kosong bila ada run: %v", m["machine_utilization"])
	}
	if got := obj(m["milk_received_l"])["total"]; got == 0.0 {
		t.Errorf("total susu tidak boleh 0: %v", m["milk_received_l"])
	}
}
