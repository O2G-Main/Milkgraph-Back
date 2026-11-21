package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"milkgraph/internal/store"
)

// Downtime dan prediksi maintenance (Tahap 3, P3): riwayat kejadian downtime
// per alat disimpan dinamis di state store (entity "downtime-event", id
// "current", satu dokumen berisi daftar, pola yang sama dengan holds dan
// transfer). Seed tidak menyediakan riwayat downtime yang kredibel: cipLog
// di ops.json hanya memuat waktu CIP terakhir/berikutnya per aset (satu
// baris per aset, tanpa rangkaian tanggal), plans di scenario.json adalah
// segmen rencana shift, dan pastLog adalah narasi. Karena itu daftar dimulai
// kosong; riwayat tumbuh dari POST dan kelak dari IoT, bukan dari seed
// karangan.
//
// Prediksi bersifat deterministik dari interval antar waktu mulai kejadian:
// rata-rata interval dari riwayat (minimal 2 interval) atau dari parameter
// interval_days yang diberikan pemanggil. Rekomendasi waktu maintenance
// mencari celah kosong jadwal tersimpan untuk alat itu yang berakhir sebelum
// prediksi downtime berikutnya.

var tipeDowntimeValid = map[string]bool{
	"planned_maintenance": true,
	"breakdown":           true,
	"cleaning":            true,
}

var sumberDowntimeValid = map[string]bool{
	"user_input": true,
	"iot":        true,
	"predicted":  true,
}

type DowntimeEvent struct {
	EventID    string `json:"event_id"`
	ResourceID string `json:"resource_id"`
	Start      string `json:"start"`
	End        string `json:"end"`
	Type       string `json:"type"`
	Source     string `json:"source"`
}

func urutDowntime(list []DowntimeEvent) {
	sort.SliceStable(list, func(i, j int) bool {
		ti, ei := parseWaktu(list[i].Start)
		tj, ej := parseWaktu(list[j].Start)
		if ei == nil && ej == nil && !ti.Equal(tj) {
			return ti.Before(tj)
		}
		if list[i].Start != list[j].Start {
			return list[i].Start < list[j].Start
		}
		return list[i].EventID < list[j].EventID
	})
}

// downtimeEvents membaca seluruh riwayat downtime dari state store; tanpa
// DB (mode baca demo) daftarnya kosong karena tidak ada seed untuk domain
// ini.
func (s *Server) downtimeEvents(ctx context.Context) ([]DowntimeEvent, error) {
	list := []DowntimeEvent{}
	if e := s.state(ctx, "downtime-event", "current", &list); e != nil {
		return nil, e
	}
	urutDowntime(list)
	return list, nil
}

// PrediksiDowntime adalah hasil perhitungan prediksi downtime satu resource.
// Field pointer bernilai null di JSON bila tidak bisa dihitung (data belum
// cukup), supaya FE bisa membedakan "nol" dari "tidak diketahui".
type PrediksiDowntime struct {
	ResourceID      string    `json:"resource_id"`
	EventsCount     int       `json:"events_count"`
	IntervalsDays   []float64 `json:"intervals_days"`
	AvgIntervalDays *float64  `json:"avg_interval_days"`
	LastEventAt     *string   `json:"last_event_at"`
	PredictedNext   *string   `json:"predicted_next"`
	DaysRemaining   *float64  `json:"days_remaining"`
	Alert           string    `json:"alert"`
	Basis           string    `json:"basis"`
}

// prediksiDowntime menghitung prediksi downtime berikutnya untuk satu
// resource dari daftar kejadian (semua resource; penyaringan di dalam).
//
// Aturan (deterministik):
//   - interval = selisih hari antara waktu mulai dua kejadian berurutan.
//   - avg_interval_days = rata-rata interval bila ada minimal 2 interval
//     (basis "history"); bila tidak, memakai intervalKonfigurasi bila
//     diberikan (basis "configured"); selain itu basis "insufficient_data"
//     dan prediksi tidak dihitung.
//   - predicted_next = waktu mulai kejadian terakhir + avg_interval_days.
//   - days_remaining = predicted_next - now dalam hari (1 desimal).
//   - alert: "LEWAT" bila days_remaining < 0, "MENDEKATI" bila
//     days_remaining <= 20% dari avg_interval_days, selain itu "AMAN".
//     Pada basis insufficient_data alert diisi "INSUFFICIENT_DATA".
func prediksiDowntime(resourceID string, events []DowntimeEvent, intervalKonfigurasi *float64, now time.Time) PrediksiDowntime {
	out := PrediksiDowntime{ResourceID: resourceID, IntervalsDays: []float64{}, Alert: "INSUFFICIENT_DATA", Basis: "insufficient_data"}
	mulai := []time.Time{}
	for _, e := range events {
		if e.ResourceID != resourceID {
			continue
		}
		t, err := parseWaktu(e.Start)
		if err != nil {
			continue
		}
		mulai = append(mulai, t)
	}
	sort.Slice(mulai, func(i, j int) bool { return mulai[i].Before(mulai[j]) })
	out.EventsCount = len(mulai)
	if len(mulai) == 0 {
		return out
	}
	terakhir := formatWaktu(mulai[len(mulai)-1])
	out.LastEventAt = &terakhir
	for i := 1; i < len(mulai); i++ {
		d := math.Round(mulai[i].Sub(mulai[i-1]).Hours()/24*1000) / 1000
		out.IntervalsDays = append(out.IntervalsDays, d)
	}
	var avg float64
	switch {
	case len(out.IntervalsDays) >= 2:
		total := 0.0
		for _, d := range out.IntervalsDays {
			total += d
		}
		avg = math.Round(total/float64(len(out.IntervalsDays))*1000) / 1000
		out.Basis = "history"
	case intervalKonfigurasi != nil && *intervalKonfigurasi > 0:
		avg = *intervalKonfigurasi
		out.Basis = "configured"
	default:
		return out
	}
	out.AvgIntervalDays = &avg
	pred := mulai[len(mulai)-1].Add(time.Duration(avg * float64(24*time.Hour)))
	predStr := formatWaktu(pred)
	out.PredictedNext = &predStr
	sisa := math.Round(pred.Sub(now).Hours()/24*10) / 10
	out.DaysRemaining = &sisa
	switch {
	case sisa < 0:
		out.Alert = "LEWAT"
	case sisa <= 0.2*avg:
		out.Alert = "MENDEKATI"
	default:
		out.Alert = "AMAN"
	}
	return out
}

// RekomendasiMaintenance adalah jendela waktu yang disarankan untuk
// maintenance satu resource beserta alasan pemilihannya.
type RekomendasiMaintenance struct {
	ResourceID       string  `json:"resource_id"`
	RecommendedStart string  `json:"recommended_start"`
	RecommendedEnd   string  `json:"recommended_end"`
	DurationHours    float64 `json:"duration_hours"`
	Reason           string  `json:"reason"`
}

// rekomendasiMaintenance memilih jendela maintenance untuk satu resource.
//
// Urutan pencarian (deterministik):
//  1. Celah kosong pada jadwal tersimpan (jendela jadwal dikurangi run
//     resource itu, termasuk tepi jendela) yang panjangnya >= durasi dan,
//     bila prediksi tersedia, berakhir sebelum predicted_next. Celah paling
//     awal yang memenuhi syarat menang.
//  2. Bila tidak ada celah yang memenuhi (atau tidak ada jadwal) tetapi
//     prediksi tersedia: jendela yang berakhir tepat di predicted_next.
//  3. Selain itu tidak ada rekomendasi (ok = false).
//
// Penyederhanaan yang disadari: celah dihitung hanya dari run resource itu
// sendiri di jadwal terbaru; status FM lain dan changeover tidak dihitung,
// dan jalur 2 tidak memeriksa bentrok dengan run yang sudah terjadwal.
func rekomendasiMaintenance(resourceID string, sch *Schedule, pred PrediksiDowntime, durasi time.Duration) (RekomendasiMaintenance, bool) {
	jam := durasi.Hours()
	var predNext *time.Time
	if pred.PredictedNext != nil {
		if t, err := parseWaktu(*pred.PredictedNext); err == nil {
			predNext = &t
		}
	}
	if sch != nil {
		mulai, errM := parseWaktu(sch.Window.Start)
		selesai, errS := parseWaktu(sch.Window.End)
		if errM == nil && errS == nil {
			type rentang struct{ dari, sampai time.Time }
			runs := []rentang{}
			for _, r := range sch.Runs {
				if r.FMID != resourceID {
					continue
				}
				rs, e1 := parseWaktu(r.Start)
				re, e2 := parseWaktu(r.End)
				if e1 != nil || e2 != nil {
					continue
				}
				runs = append(runs, rentang{rs, re})
			}
			sort.Slice(runs, func(i, j int) bool { return runs[i].dari.Before(runs[j].dari) })
			celah := []rentang{}
			kursor := mulai
			for _, rn := range runs {
				if rn.dari.After(kursor) {
					celah = append(celah, rentang{kursor, rn.dari})
				}
				if rn.sampai.After(kursor) {
					kursor = rn.sampai
				}
			}
			if selesai.After(kursor) {
				celah = append(celah, rentang{kursor, selesai})
			}
			for _, c := range celah {
				if c.sampai.Sub(c.dari) < durasi {
					continue
				}
				akhir := c.dari.Add(durasi)
				if predNext != nil && akhir.After(*predNext) {
					continue
				}
				reason := fmt.Sprintf("Celah kosong %.1f jam pada jadwal %s; belum ada prediksi downtime untuk resource ini.", jam, sch.ScheduleID)
				if predNext != nil {
					reason = fmt.Sprintf("Celah kosong %.1f jam pada jadwal %s yang berakhir sebelum prediksi downtime %s.", jam, sch.ScheduleID, *pred.PredictedNext)
				}
				return RekomendasiMaintenance{
					ResourceID: resourceID, RecommendedStart: formatWaktu(c.dari),
					RecommendedEnd: formatWaktu(akhir), DurationHours: jam, Reason: reason,
				}, true
			}
		}
	}
	if predNext != nil {
		return RekomendasiMaintenance{
			ResourceID: resourceID, RecommendedStart: formatWaktu(predNext.Add(-durasi)),
			RecommendedEnd: formatWaktu(*predNext), DurationHours: jam,
			Reason: fmt.Sprintf("Tidak ada celah jadwal yang cukup; jendela ditempatkan tepat sebelum prediksi downtime %s.", *pred.PredictedNext),
		}, true
	}
	return RekomendasiMaintenance{}, false
}

func (s *Server) registerDowntime(register func(string, string, endpoint)) {
	register("GET /api/v1/downtime-events", "", func(r *http.Request) (any, error) {
		list, e := s.downtimeEvents(r.Context())
		if e != nil {
			return nil, e
		}
		filter := r.URL.Query().Get("resource_id")
		out := []DowntimeEvent{}
		for _, ev := range list {
			if filter == "" || ev.ResourceID == filter {
				out = append(out, ev)
			}
		}
		return out, nil
	})
	register("POST /api/v1/downtime-events", "downtime.record", func(r *http.Request) (any, error) {
		if err := s.dbRequired(); err != nil {
			return nil, err
		}
		var b struct {
			ResourceID string `json:"resource_id"`
			Start      string `json:"start"`
			End        string `json:"end"`
			Type       string `json:"type"`
			Source     string `json:"source"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		if strings.TrimSpace(b.ResourceID) == "" {
			return nil, invalid("Resource wajib diisi.", "resource_id")
		}
		if e := validasiWaktuKedatangan(b.Start, "start"); e != nil {
			return nil, e
		}
		if e := validasiWaktuKedatangan(b.End, "end"); e != nil {
			return nil, e
		}
		mulai, _ := parseWaktu(b.Start)
		selesai, _ := parseWaktu(b.End)
		if !selesai.After(mulai) {
			return nil, invalid("Waktu selesai harus sesudah waktu mulai.", "end")
		}
		if !tipeDowntimeValid[b.Type] {
			return nil, invalid("Tipe downtime tidak valid.", "type")
		}
		source := b.Source
		if source == "" {
			source = "user_input"
		}
		if !sumberDowntimeValid[source] {
			return nil, invalid("Sumber downtime tidak valid.", "source")
		}
		var created DowntimeEvent
		result, e := s.change(r.Context(), "downtime-event", "current", func(tx *store.Tx, raw json.RawMessage) (store.Change, error) {
			list := []DowntimeEvent{}
			if len(raw) > 0 {
				if e := json.Unmarshal(raw, &list); e != nil {
					return store.Change{}, e
				}
			}
			created = DowntimeEvent{
				EventID:    fmt.Sprintf("DTE-%03d", len(list)+1),
				ResourceID: b.ResourceID, Start: b.Start, End: b.End,
				Type: b.Type, Source: source,
			}
			list = append(list, created)
			urutDowntime(list)
			return store.Change{Value: list, Event: "downtime-event.created"}, nil
		})
		if e != nil {
			return nil, e
		}
		out := result.(map[string]any)
		out["result"] = created
		return out, nil
	})
	register("GET /api/v1/downtime-events/prediction", "", func(r *http.Request) (any, error) {
		resourceID := strings.TrimSpace(r.URL.Query().Get("resource_id"))
		if resourceID == "" {
			return nil, invalid("resource_id wajib diisi.", "resource_id")
		}
		var intervalCfg *float64
		if mentah := strings.TrimSpace(r.URL.Query().Get("interval_days")); mentah != "" {
			v, err := strconv.ParseFloat(mentah, 64)
			if err != nil || v <= 0 {
				return nil, invalid("interval_days harus angka lebih dari 0.", "interval_days")
			}
			intervalCfg = &v
		}
		events, e := s.downtimeEvents(r.Context())
		if e != nil {
			return nil, e
		}
		return prediksiDowntime(resourceID, events, intervalCfg, waktuDemo), nil
	})
	register("GET /api/v1/maintenance/recommendation", "", func(r *http.Request) (any, error) {
		resourceID := strings.TrimSpace(r.URL.Query().Get("resource_id"))
		if resourceID == "" {
			return nil, invalid("resource_id wajib diisi.", "resource_id")
		}
		durasiJam := 4.0
		if mentah := strings.TrimSpace(r.URL.Query().Get("duration_hours")); mentah != "" {
			v, err := strconv.ParseFloat(mentah, 64)
			if err != nil || v <= 0 {
				return nil, invalid("duration_hours harus angka lebih dari 0.", "duration_hours")
			}
			durasiJam = v
		}
		events, e := s.downtimeEvents(r.Context())
		if e != nil {
			return nil, e
		}
		pred := prediksiDowntime(resourceID, events, nil, waktuDemo)
		var sch Schedule
		if e := s.state(r.Context(), "schedule", "current", &sch); e != nil {
			return nil, e
		}
		var schPtr *Schedule
		if sch.ScheduleID != "" {
			schPtr = &sch
		}
		rec, ok := rekomendasiMaintenance(resourceID, schPtr, pred, time.Duration(durasiJam*float64(time.Hour)))
		if !ok {
			return nil, &apiError{404, "NOT_FOUND", "Belum ada jadwal tersimpan atau prediksi downtime untuk resource ini.", ""}
		}
		return rec, nil
	})
}
