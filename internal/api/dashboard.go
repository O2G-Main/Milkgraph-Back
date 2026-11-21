package api

import (
	"context"
	"math"
	"net/http"
	"sort"
	"time"
)

// Dashboard Eksekutif (Tahap 3, P2): empat agregat untuk layar pimpinan,
// semuanya dihitung deterministik dari state store pada saat dibaca, tanpa
// menulis apa pun:
//   - milk_received_l: total susu yang sudah tiba per farm, dari daftar
//     kedatangan RT (s.rtArrivals: isi store, atau seed skenario tanker bila
//     store kosong; dashboard tidak menanam seed, GET ini murni baca).
//   - machine_utilization: dari jadwal versi terbaru. used_hours adalah
//     total durasi run per FM yang tumpang tindih dengan jendela jadwal;
//     downtime_hours adalah total jendela pemicu DOWNTIME yang tumpang
//     tindih dengan jendela yang sama; pct = used/jam jendela x 100.
//     FM yang tampil hanya yang punya run atau downtime di jendela itu.
//   - daily_schedule: target harian per tanggal jendela. Target utama adalah
//     qty PO yang jatuh tempo pada tanggal itu (bisa diturunkan dari data
//     PO), dilengkapi qty yang benar-benar terjadwal dari run pada tanggal
//     itu sebagai pembanding.
//   - po_summary: hitungan fulfilment per status dari jadwal terbaru,
//     disertai array fulfilment-nya sendiri.
//
// Bila belum ada jadwal, machine_utilization kosong dan daily_schedule serta
// po_summary bernilai null. Tanpa DB pola bacanya sama seperti endpoint GET
// lain: susu dari seed, jadwal dianggap belum ada.

type SusuPerFarm struct {
	FarmID   string  `json:"farm_id"`
	FarmName string  `json:"farm_name"`
	TotalL   float64 `json:"total_l"`
}

type SusuDiterima struct {
	Total  float64       `json:"total"`
	ByFarm []SusuPerFarm `json:"by_farm"`
}

type UtilisasiFM struct {
	FMID          string  `json:"fm_id"`
	UsedHours     float64 `json:"used_hours"`
	DowntimeHours float64 `json:"downtime_hours"`
	Pct           float64 `json:"pct"`
}

type TargetHarian struct {
	Date           string  `json:"date"`
	DueLiter       float64 `json:"due_liter"`
	ScheduledLiter float64 `json:"scheduled_liter"`
}

type JadwalHarian struct {
	ScheduleID string         `json:"schedule_id"`
	Version    int            `json:"version"`
	Window     JendelaWaktu   `json:"window"`
	RunsCount  int            `json:"runs_count"`
	Targets    []TargetHarian `json:"targets"`
}

type RingkasanPO struct {
	Fulfilled  int            `json:"fulfilled"`
	AtRisk     int            `json:"at_risk"`
	Late       int            `json:"late"`
	OnTrack    int            `json:"on_track"`
	Fulfilment []FulfilmentPO `json:"fulfilment"`
}

type DashboardEksekutif struct {
	MilkReceivedL      SusuDiterima  `json:"milk_received_l"`
	MachineUtilization []UtilisasiFM `json:"machine_utilization"`
	DailySchedule      *JadwalHarian `json:"daily_schedule"`
	POSummary          *RingkasanPO  `json:"po_summary"`
}

func bulat1(x float64) float64 { return math.Round(x*10) / 10 }

// totalSusuDiterima menjumlahkan volume kedatangan berstatus "Sudah Tiba"
// per farm; kedatangan yang belum tiba atau dibatalkan tidak dihitung.
func totalSusuDiterima(arrivals []RTArrival) SusuDiterima {
	out := SusuDiterima{ByFarm: []SusuPerFarm{}}
	perFarm := map[string]*SusuPerFarm{}
	for _, a := range arrivals {
		if a.Status != "Sudah Tiba" {
			continue
		}
		f, ok := perFarm[a.FarmID]
		if !ok {
			f = &SusuPerFarm{FarmID: a.FarmID, FarmName: a.FarmName}
			perFarm[a.FarmID] = f
		}
		f.TotalL += a.VolumeL
		out.Total += a.VolumeL
	}
	for _, f := range perFarm {
		out.ByFarm = append(out.ByFarm, *f)
	}
	sort.Slice(out.ByFarm, func(i, j int) bool { return out.ByFarm[i].FarmID < out.ByFarm[j].FarmID })
	return out
}

// tumpangTindih mengembalikan durasi irisan dua interval (0 bila tidak
// bersinggungan).
func tumpangTindih(aStart, aEnd, bStart, bEnd time.Time) time.Duration {
	s := aStart
	if bStart.After(s) {
		s = bStart
	}
	e := aEnd
	if bEnd.Before(e) {
		e = bEnd
	}
	if e.After(s) {
		return e.Sub(s)
	}
	return 0
}

// utilisasiFM menghitung jam pakai dan jam downtime per FM dari jadwal
// terbaru dan pemicu DOWNTIME yang tercatat.
func utilisasiFM(sch Schedule, triggers []ScheduleTrigger) []UtilisasiFM {
	out := []UtilisasiFM{}
	mulai, e1 := parseWaktu(sch.Window.Start)
	selesai, e2 := parseWaktu(sch.Window.End)
	if e1 != nil || e2 != nil || !selesai.After(mulai) {
		return out
	}
	jamJendela := selesai.Sub(mulai).Hours()
	type agg struct {
		used, down time.Duration
		adaRun     bool
	}
	perFM := map[string]*agg{}
	untuk := func(id string) *agg {
		a, ok := perFM[id]
		if !ok {
			a = &agg{}
			perFM[id] = a
		}
		return a
	}
	for _, r := range sch.Runs {
		rs, e1 := parseWaktu(r.Start)
		re, e2 := parseWaktu(r.End)
		if e1 != nil || e2 != nil {
			continue
		}
		a := untuk(r.FMID)
		a.adaRun = true
		a.used += tumpangTindih(rs, re, mulai, selesai)
	}
	for _, w := range jendelaDowntimeAktif(triggers) {
		d := tumpangTindih(w.Start, w.End, mulai, selesai)
		if d <= 0 {
			continue
		}
		untuk(w.ResourceID).down += d
	}
	for id, a := range perFM {
		if !a.adaRun && a.down <= 0 {
			continue
		}
		used := a.used.Hours()
		out = append(out, UtilisasiFM{
			FMID:          id,
			UsedHours:     bulat1(used),
			DowntimeHours: bulat1(a.down.Hours()),
			Pct:           bulat1(used / jamJendela * 100),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FMID < out[j].FMID })
	return out
}

// jadwalHarian merangkum jadwal terbaru per tanggal jendela (3 hari).
func jadwalHarian(sch Schedule, pos []ProductionOrder) *JadwalHarian {
	out := &JadwalHarian{
		ScheduleID: sch.ScheduleID,
		Version:    sch.Version,
		Window:     sch.Window,
		RunsCount:  len(sch.Runs),
		Targets:    []TargetHarian{},
	}
	mulai, err := parseWaktu(sch.Window.Start)
	if err != nil {
		return out
	}
	duePerTanggal := map[string]float64{}
	for _, po := range pos {
		due, e := parseWaktu(po.DueDatetime)
		if e != nil {
			continue
		}
		for _, line := range po.Lines {
			duePerTanggal[due.Format("2006-01-02")] += line.QtyLiter
		}
	}
	terjadwalPerTanggal := map[string]float64{}
	for _, r := range sch.Runs {
		m, e := parseWaktu(r.Start)
		if e != nil {
			continue
		}
		terjadwalPerTanggal[m.Format("2006-01-02")] += r.QtyLiter
	}
	for i := 0; i < 3; i++ {
		tanggal := mulai.AddDate(0, 0, i).Format("2006-01-02")
		out.Targets = append(out.Targets, TargetHarian{
			Date:           tanggal,
			DueLiter:       duePerTanggal[tanggal],
			ScheduledLiter: terjadwalPerTanggal[tanggal],
		})
	}
	return out
}

// ringkasanPO menghitung fulfilment jadwal terbaru per status.
func ringkasanPO(sch Schedule) *RingkasanPO {
	out := &RingkasanPO{Fulfilment: sch.Fulfilment}
	if out.Fulfilment == nil {
		out.Fulfilment = []FulfilmentPO{}
	}
	for _, f := range sch.Fulfilment {
		switch f.Status {
		case "FULFILLED":
			out.Fulfilled++
		case "AT_RISK":
			out.AtRisk++
		case "LATE":
			out.Late++
		case "ON_TRACK":
			out.OnTrack++
		}
	}
	return out
}

// dashboardEksekutif merakit keempat agregat dari state saat ini.
func (s *Server) dashboardEksekutif(ctx context.Context) (any, error) {
	arrivals, e := s.rtArrivals(ctx)
	if e != nil {
		return nil, e
	}
	out := DashboardEksekutif{
		MilkReceivedL:      totalSusuDiterima(arrivals),
		MachineUtilization: []UtilisasiFM{},
	}
	var sch Schedule
	if e := s.state(ctx, "schedule", "current", &sch); e != nil {
		return nil, e
	}
	if sch.ScheduleID == "" {
		return out, nil
	}
	triggers, e := s.scheduleTriggers(ctx)
	if e != nil {
		return nil, e
	}
	pos, e := s.productionOrders(ctx)
	if e != nil {
		return nil, e
	}
	out.MachineUtilization = utilisasiFM(sch, triggers)
	out.DailySchedule = jadwalHarian(sch, pos)
	out.POSummary = ringkasanPO(sch)
	return out, nil
}

func (s *Server) registerDashboard(register func(string, string, endpoint)) {
	register("GET /api/v1/dashboard/executive", "", func(r *http.Request) (any, error) {
		return s.dashboardEksekutif(r.Context())
	})
}
