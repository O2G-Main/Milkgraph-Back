package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"milkgraph/internal/ai"
)

// Narasi perubahan jadwal (Tahap 3, P2): Backend memetakan fakta jadwal
// berversi ke skema POST /ai/repair-priority/explain milik service AI
// (Kontrak API AI MilkGraph v2). Skema endpoint itu memang berorientasi
// prioritas perbaikan dan tidak punya field konteks_fakta, jadi pemetaannya:
//   - batch_id          = schedule_id versi yang dinarasikan.
//   - urutan_prioritas  = tindakan tindak lanjut terurut tingkat keparahan:
//     tindakan utama menerapkan jadwal versi ini (memuat triggered_by dan
//     pemicu tercatat yang cocok), ringkasan perubahan dari versi sebelumnya
//     (dari hitungan di RingkasanJadwal), satu tindakan per PO (status,
//     persen terpenuhi, penyelesaian direncanakan, akar masalah sebagai
//     alasan_kode), lalu satu tindakan per peringatan jadwal.
//   - pelanggaran_sebelum/sesudah = jumlah masalah versi itu: peringatan +
//     PO berstatus LATE atau AT_RISK. Tanpa versi sebelumnya, sebelum =
//     sesudah (tidak ada pembanding).
//   - jumlah_constraint = jumlah run + jumlah PO yang dievaluasi scheduler
//     pada versi ini.
//   - label_data diteruskan dari PO (selama ini "simulasi").
//
// Kegagalan service AI mengikuti pola narasi lain: 503 bila token belum
// dikonfigurasi, 502 bila tidak terjangkau, 404 bila jadwal (atau versi yang
// diminta) tidak ada. Hanya versi terbaru yang tersimpan penuh di state
// store, jadi versi lama yang diminta eksplisit dibalas 404.

// jumlahMasalah menghitung "pelanggaran" satu versi jadwal: peringatan
// ditambah PO yang terlambat atau berisiko.
func jumlahMasalah(sch Schedule) int {
	n := len(sch.Warnings)
	for _, f := range sch.Fulfilment {
		if f.Status == "LATE" || f.Status == "AT_RISK" {
			n++
		}
	}
	return n
}

// hitungStatusPO menghitung PO terlambat dan berisiko pada satu jadwal.
func hitungStatusPO(sch Schedule) (late, atRisk int) {
	for _, f := range sch.Fulfilment {
		switch f.Status {
		case "LATE":
			late++
		case "AT_RISK":
			atRisk++
		}
	}
	return late, atRisk
}

// pemicuUntukVersi memilih pemicu yang tercatat untuk versi ini. State store
// tidak menyimpan tautan eksplisit pemicu ke versi, jadi pencocokannya
// deterministik berdasarkan urutan pembuatan: pemicu terakhir yang
// reason_code-nya sama dengan triggered_by versi tersebut. Nil bila tidak
// ada (mis. generate manual).
func pemicuUntukVersi(sch Schedule, triggers []ScheduleTrigger) *ScheduleTrigger {
	var cocok *ScheduleTrigger
	for i := range triggers {
		if triggers[i].ReasonCode == sch.TriggeredBy {
			cocok = &triggers[i]
		}
	}
	return cocok
}

// teksPeringatan merangkai satu peringatan jadwal menjadi kalimat tindakan.
func teksPeringatan(w WarningJadwal) string {
	switch w.Type {
	case "TANK_STOCK_SHORT":
		if w.NeededL != nil && w.AvailableL != nil {
			return fmt.Sprintf("Peringatan stok bahan baku: kebutuhan %g L melebihi stok tersedia %g L", *w.NeededL, *w.AvailableL)
		}
	case "HOLDING_TIME":
		if w.LimitH != nil && w.ProjectedH != nil {
			return fmt.Sprintf("Peringatan holding time pada %s: proyeksi menunggu %g jam melewati batas %g jam", w.ResourceID, *w.ProjectedH, *w.LimitH)
		}
	}
	return fmt.Sprintf("Peringatan %s pada %s", w.Type, w.ResourceID)
}

// tingkatStatus mengurutkan status fulfilment dari paling parah.
func tingkatStatus(status string) int {
	switch status {
	case "LATE":
		return 0
	case "AT_RISK":
		return 1
	case "ON_TRACK":
		return 2
	case "FULFILLED":
		return 3
	}
	return 4
}

// bangunExplainRequest memetakan fakta satu versi jadwal (dan ringkasan
// versi sebelumnya bila ada) ke payload /ai/repair-priority/explain.
func bangunExplainRequest(sch Schedule, prev *RingkasanJadwal, triggers []ScheduleTrigger, label string) ai.ExplainRequest {
	tindakan := []ai.PriorityAction{}
	tambah := func(teks, kode string) {
		tindakan = append(tindakan, ai.PriorityAction{Rank: len(tindakan) + 1, Tindakan: teks, AlasanKode: kode})
	}

	kodePemicu := sch.TriggeredBy
	if !reasonCodeValid[kodePemicu] {
		kodePemicu = "PERUBAHAN_JADWAL"
	}
	utama := fmt.Sprintf("Terapkan jadwal %s versi %d yang digenerate pada %s dengan pemicu %s",
		sch.ScheduleID, sch.Version, sch.GeneratedAt, sch.TriggeredBy)
	if tr := pemicuUntukVersi(sch, triggers); tr != nil {
		utama += fmt.Sprintf("; pemicu tercatat %s oleh %s", tr.ID, tr.CreatedBy)
		if tr.RefID != "" {
			utama += ", ref " + tr.RefID
		}
		if tr.Detail != "" {
			utama += ": " + tr.Detail
		}
		if tr.Downtime != nil {
			utama += fmt.Sprintf(" (downtime %s, %s s/d %s)", tr.Downtime.ResourceID, tr.Downtime.Start, tr.Downtime.End)
		}
	}
	tambah(utama, kodePemicu)

	if prev != nil {
		late, risk := hitungStatusPO(sch)
		tambah(fmt.Sprintf("Perubahan dari versi %d ke versi %d: total run %d menjadi %d, PO terlambat %d menjadi %d, PO berisiko %d menjadi %d, peringatan %d menjadi %d",
			prev.Version, sch.Version, prev.RunsTotal, len(sch.Runs), prev.POLate, late, prev.POAtRisk, risk, prev.Warnings, len(sch.Warnings)),
			"PERUBAHAN_JADWAL")
	}

	urut := append([]FulfilmentPO{}, sch.Fulfilment...)
	sort.SliceStable(urut, func(i, j int) bool {
		si, sj := tingkatStatus(urut[i].Status), tingkatStatus(urut[j].Status)
		if si != sj {
			return si < sj
		}
		return urut[i].POID < urut[j].POID
	})
	for _, f := range urut {
		teks := fmt.Sprintf("PO %s berstatus %s, terpenuhi %g%%", f.POID, f.Status, f.FulfilledPct*100)
		if f.PlannedCompletion != "" {
			teks += ", penyelesaian direncanakan " + f.PlannedCompletion
		} else {
			teks += ", belum terjadwal"
		}
		if len(f.RootCauses) > 0 {
			teks += "; akar masalah: " + strings.Join(f.RootCauses, ", ")
		}
		kode := "SESUAI_JADWAL"
		if len(f.RootCauses) > 0 {
			kode = f.RootCauses[0]
		} else if f.Status == "LATE" {
			kode = "TERLAMBAT"
		}
		tambah(teks, kode)
	}

	for _, w := range sch.Warnings {
		tambah(teksPeringatan(w), w.Type)
	}

	sebelum := jumlahMasalah(sch)
	if prev != nil {
		sebelum = prev.POLate + prev.POAtRisk + prev.Warnings
	}
	return ai.ExplainRequest{
		BatchID:            sch.ScheduleID,
		UrutanPrioritas:    tindakan,
		PelanggaranSebelum: sebelum,
		PelanggaranSesudah: jumlahMasalah(sch),
		JumlahConstraint:   len(sch.Runs) + len(sch.Fulfilment),
		LabelData:          label,
	}
}

// narasiJadwal melayani POST /api/v1/schedules/narrate: penjelasan perubahan
// jadwal dari service AI untuk satu versi (bawaan: versi terbaru,
// dibandingkan dengan versi sebelumnya dari riwayat bila ada).
func (s *Server) narasiJadwal(ctx context.Context, versi *int) (any, error) {
	if s.AI == nil || !s.AI.Enabled() {
		return nil, &apiError{503, "UNAVAILABLE", "Layanan AI belum dikonfigurasi.", ""}
	}
	var sch Schedule
	if e := s.state(ctx, "schedule", "current", &sch); e != nil {
		return nil, e
	}
	if sch.ScheduleID == "" || (versi != nil && *versi != sch.Version) {
		return nil, missing()
	}
	var riwayat []RingkasanJadwal
	if e := s.state(ctx, "schedule", "history", &riwayat); e != nil {
		return nil, e
	}
	var prev *RingkasanJadwal
	for i := range riwayat {
		if riwayat[i].Version == sch.Version-1 {
			c := riwayat[i]
			prev = &c
		}
	}
	triggers, e := s.scheduleTriggers(ctx)
	if e != nil {
		return nil, e
	}
	pos, e := s.productionOrders(ctx)
	if e != nil {
		return nil, e
	}
	label := "simulasi"
	for _, po := range pos {
		if po.LabelData != "" {
			label = po.LabelData
			break
		}
	}
	req := bangunExplainRequest(sch, prev, triggers, label)
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	resp, err := s.AI.Explain(ctx, req)
	if err != nil {
		slog.Warn("panggilan layanan AI gagal", "error", err)
		return nil, &apiError{502, "AI_UNAVAILABLE", "Layanan AI tidak dapat dihubungi.", ""}
	}
	s.catatPemakaianAI(ctx, "explain", req, resp)
	return keMap(resp), nil
}

func (s *Server) registerJadwalNarasi(register func(string, string, endpoint)) {
	register("POST /api/v1/schedules/narrate", "", func(r *http.Request) (any, error) {
		var b struct {
			Version *int `json:"version"`
		}
		if e := decodeOptional(r, &b); e != nil {
			return nil, e
		}
		return s.narasiJadwal(r.Context(), b.Version)
	})
}
