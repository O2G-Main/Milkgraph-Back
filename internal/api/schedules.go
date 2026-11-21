package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"milkgraph/internal/store"
	"milkgraph/seed/master"
)

// Penjadwalan Tahap 3 (P1): Production Order, jadwal berversi, dan pemicu
// regenerasi, semuanya dinamis di state store:
//   - entity "po"               id "current": []ProductionOrder
//   - entity "schedule"         id "current": Schedule terbaru
//   - entity "schedule"         id "history": []RingkasanJadwal
//   - entity "schedule-trigger" id "current": []ScheduleTrigger
//
// Pola baca/tulis mengikuti rt_arrivals.go dan transfers.go: daftar disimpan
// sebagai satu dokumen, perubahan lewat s.change, dan isi awal diturunkan
// dari seed supaya demo tidak berubah sebelum ada input baru.

// seedPOs adalah PO demo dari kasus coaching Bootcamp 3: satu batch Susu
// Cikelat dipecah dua run filling, 250 ml lalu 1000 ml. SKU coaching
// "UHT-CKL-250"/"UHT-CKL-1000" tidak ada di master data repo; padanan
// nyatanya adalah finished SKU cokelat UHT varian LOCAL:
// UHT-CKL-250 -> UHT-CH-250-LOCAL (FL-08/FL-09/FL-10),
// UHT-CKL-1000 -> UHT-CH-1000-LOCAL (FL-05/FL-06).
func (s *Server) seedPOs() []ProductionOrder {
	return []ProductionOrder{{
		POID:     "PO-2026-1101",
		Priority: 1,
		Lines: []POLine{
			{SKU: "UHT-CH-250-LOCAL", QtyLiter: 12000},
			{SKU: "UHT-CH-1000-LOCAL", QtyLiter: 8000},
		},
		DueDatetime: "2026-11-24T08:00:00+07:00",
		LabelData:   "simulasi",
	}}
}

// productionOrders membaca PO tersimpan, atau seed demo bila store kosong
// (tanpa menulis; penanaman seed terjadi di GET /pos).
func (s *Server) productionOrders(ctx context.Context) ([]ProductionOrder, error) {
	list := []ProductionOrder{}
	if e := s.state(ctx, "po", "current", &list); e != nil {
		return nil, e
	}
	if len(list) == 0 {
		list = s.seedPOs()
	}
	return list, nil
}

func basisPOs(s *Server, raw json.RawMessage) ([]ProductionOrder, error) {
	list := s.seedPOs()
	if len(raw) > 0 {
		if e := json.Unmarshal(raw, &list); e != nil {
			return nil, e
		}
	}
	return list, nil
}

// katalogMaster mengembalikan katalog master, memuat ulang bila Server
// dibangun tanpa Handler (mis. sebagian tes unit).
func (s *Server) katalogMaster() (*master.Catalog, error) {
	if s.master != nil {
		return s.master, nil
	}
	return master.Load(master.Files)
}

// infoSKU membangun turunan master untuk seluruh finished SKU: ukuran
// kemasan (ml) dan nama produk tampilan untuk matriks changeover SOP.
// Nama tampilan mengikuti kosakata limits.json: UHT-CH -> "UHT Cokelat",
// UHT-FC -> "UHT Full Cream", UHT-ST -> "UHT Stroberi".
func infoSKU(katalog *master.Catalog) map[string]InfoSKU {
	ukuranKode := map[string]int{}
	for _, row := range katalog.Tables["pack_size_codes"] {
		if n, e := strconv.Atoi(row["exact_size_ml"]); e == nil {
			ukuranKode[row["pack_size_code"]] = n
		}
	}
	namaProduk := map[string]string{}
	for _, row := range katalog.Tables["product_catalog"] {
		namaProduk[row["sku_id"]] = row["product_name"]
	}
	basisNama := map[string]string{"UHT-CH": "UHT Cokelat", "UHT-FC": "UHT Full Cream", "UHT-ST": "UHT Stroberi"}
	out := map[string]InfoSKU{}
	for _, row := range katalog.Tables["finished_sku_catalog"] {
		ml := ukuranKode[row["pack_size_code"]]
		basis, ok := basisNama[row["liquid_sku_id"]]
		if !ok {
			basis = namaProduk[row["liquid_sku_id"]]
		}
		label := strconv.Itoa(ml) + " ml"
		if ml == 1000 {
			label = "1 L"
		}
		out[row["finished_sku_id"]] = InfoSKU{SizeML: ml, NamaTampil: basis + " " + label}
	}
	return out
}

// paramsJadwal merakit parameter scheduler dari data repo yang nyata:
//   - Mesin + fm_capable dari line_dedication dan
//     line_finished_sku_compatibility (eligible "Y") di master CSV;
//     backup_fm adalah line berikutnya dalam grup dedikasi yang sama
//     (allowed_sku_ids + pack_size_codes identik); tangki sumber dari
//     source_aseptic_tank_id dedikasi.
//   - Tangki dari ops.json asepticTanks (capacity + level nyata). Batas
//     holding per tangki belum terisi di master CSV (nilainya "X"), jadi
//     dipakai batas holding global dari limits (holdingMin, 630 menit).
//   - Rate per ukuran adalah nilai contoh dokumen coaching Bootcamp 3
//     (250 ml: 4000 L/jam, 1000 ml: 6000 L/jam; ukuran kecil lain mengikuti
//     4000, ukuran besar lain 6000) karena speed_packph master masih "X".
//   - Changeover dari matriks sopChangeover di limits.json.
func (s *Server) paramsJadwal(ctx context.Context) (ParamsJadwal, map[string]InfoSKU, error) {
	katalog, e := s.katalogMaster()
	if e != nil {
		return ParamsJadwal{}, nil, e
	}
	info := infoSKU(katalog)

	capable := map[string][]string{}
	for _, row := range katalog.Tables["line_finished_sku_compatibility"] {
		if row["eligible"] == "Y" {
			capable[row["line_id"]] = append(capable[row["line_id"]], row["finished_sku_id"])
		}
	}
	type dedikasi struct {
		id, tangki, grup string
	}
	ded := []dedikasi{}
	for _, row := range katalog.Tables["line_dedication"] {
		ded = append(ded, dedikasi{
			id:     row["line_id"],
			tangki: row["source_aseptic_tank_id"],
			grup:   row["allowed_sku_ids"] + "|" + row["pack_size_codes"],
		})
	}
	sort.Slice(ded, func(i, j int) bool { return ded[i].id < ded[j].id })
	grupLine := map[string][]string{}
	for _, d := range ded {
		grupLine[d.grup] = append(grupLine[d.grup], d.id)
	}
	params := ParamsJadwal{
		RateLPerHour: map[int]float64{200: 4000, 250: 4000, 320: 4000, 950: 6000, 1000: 6000},
		Changeover:   map[string]int{},
	}
	for _, d := range ded {
		backup := ""
		grup := grupLine[d.grup]
		for i, id := range grup {
			if id == d.id && len(grup) > 1 {
				backup = grup[(i+1)%len(grup)]
			}
		}
		sku := append([]string{}, capable[d.id]...)
		sort.Strings(sku)
		params.Machines = append(params.Machines, MesinJadwal{
			ID: d.id, SKUCapable: sku, BackupID: backup, TangkiSumber: d.tangki,
		})
	}
	l, e := s.limits(ctx)
	if e != nil {
		return ParamsJadwal{}, nil, e
	}
	params.HoldingLimitH = float64(l.HoldingMin) / 60
	for _, raw := range arr(s.seeds.Ops["asepticTanks"]) {
		t := obj(raw)
		params.Tanks = append(params.Tanks, TangkiJadwal{
			ID:              str(t["id"]),
			CapacityL:       floatValue(t["capacity"]),
			LevelL:          floatValue(t["level"]),
			MaxHoldingHours: params.HoldingLimitH,
		})
	}
	if raw, ok := s.seeds.Limits["sopChangeover"].(map[string]any); ok {
		for k, v := range raw {
			params.Changeover[k] = num(v)
		}
	}
	return params, info, nil
}

// scheduleTriggers membaca pemicu tersimpan (tanpa seed; kosong = belum ada).
func (s *Server) scheduleTriggers(ctx context.Context) ([]ScheduleTrigger, error) {
	list := []ScheduleTrigger{}
	if e := s.state(ctx, "schedule-trigger", "current", &list); e != nil {
		return nil, e
	}
	return list, nil
}

// jendelaDowntimeAktif menurunkan interval downtime dari pemicu DOWNTIME
// yang membawa jendela; jendela tidak valid diabaikan.
func jendelaDowntimeAktif(triggers []ScheduleTrigger) []JendelaDowntime {
	out := []JendelaDowntime{}
	for _, tr := range triggers {
		if tr.ReasonCode != "DOWNTIME" || tr.Downtime == nil {
			continue
		}
		mulai, e1 := parseWaktu(tr.Downtime.Start)
		selesai, e2 := parseWaktu(tr.Downtime.End)
		if e1 != nil || e2 != nil || !selesai.After(mulai) {
			continue
		}
		out = append(out, JendelaDowntime{ResourceID: tr.Downtime.ResourceID, Start: mulai, End: selesai})
	}
	return out
}

// generateSchedule menyusun versi jadwal baru dari PO + parameter + pemicu
// aktif saat ini, lalu menyimpannya sebagai jadwal terbaru dan menambah
// ringkasannya ke riwayat versi dalam satu transaksi state store.
func (s *Server) generateSchedule(ctx context.Context, triggeredBy string) (Schedule, error) {
	if e := s.dbRequired(); e != nil {
		return Schedule{}, e
	}
	pos, e := s.productionOrders(ctx)
	if e != nil {
		return Schedule{}, e
	}
	triggers, e := s.scheduleTriggers(ctx)
	if e != nil {
		return Schedule{}, e
	}
	params, info, e := s.paramsJadwal(ctx)
	if e != nil {
		return Schedule{}, e
	}
	// Stok tersedia: level tangki aseptik saat ini + volume kedatangan yang
	// tidak dibatalkan dan target tibanya tidak melewati akhir jendela.
	stok := 0.0
	for _, t := range params.Tanks {
		stok += t.LevelL
	}
	arrivals, e := s.rtArrivals(ctx)
	if e != nil {
		return Schedule{}, e
	}
	windowEnd := waktuDemo.Add(3 * 24 * time.Hour)
	var susuSiap *time.Time
	for _, a := range arrivals {
		if a.Status == "Dibatalkan" {
			continue
		}
		waktuTiba := a.EstimatedArrival
		if a.ActualArrival != nil && *a.ActualArrival != "" {
			waktuTiba = *a.ActualArrival
		}
		tiba, err := parseWaktu(waktuTiba)
		if err != nil {
			continue
		}
		if susuSiap == nil || tiba.Before(*susuSiap) {
			t := tiba
			susuSiap = &t
		}
		if target, err := parseWaktu(a.TargetArrival); err == nil && !target.After(windowEnd) {
			stok += a.VolumeL
		}
	}
	var riwayat []RingkasanJadwal
	if e := s.state(ctx, "schedule", "history", &riwayat); e != nil {
		return Schedule{}, e
	}
	var sch Schedule
	_, e = s.change(ctx, "schedule", "current", func(tx *store.Tx, raw json.RawMessage) (store.Change, error) {
		versi := 1
		if len(raw) > 0 {
			var lama Schedule
			if e := json.Unmarshal(raw, &lama); e == nil && lama.Version >= 1 {
				versi = lama.Version + 1
			}
		}
		sch = bangunJadwal(InputJadwal{
			POs:           pos,
			Params:        params,
			SKUInfo:       info,
			Downtime:      jendelaDowntimeAktif(triggers),
			Triggers:      triggers,
			WindowStart:   waktuDemo,
			Version:       versi,
			TriggeredBy:   triggeredBy,
			StokTersediaL: stok,
			SusuSiapPada:  susuSiap,
		})
		ringkasan := RingkasanJadwal{
			ScheduleID: sch.ScheduleID, Version: sch.Version,
			GeneratedAt: sch.GeneratedAt, TriggeredBy: sch.TriggeredBy,
			RunsTotal: len(sch.Runs), Warnings: len(sch.Warnings),
		}
		for _, f := range sch.Fulfilment {
			switch f.Status {
			case "LATE":
				ringkasan.POLate++
			case "AT_RISK":
				ringkasan.POAtRisk++
			}
		}
		riwayat = append(riwayat, ringkasan)
		if e := tx.Put(ctx, "schedule", "history", riwayat); e != nil {
			return store.Change{}, e
		}
		return store.Change{Value: sch, Event: "schedule.generated"}, nil
	})
	if e != nil {
		return Schedule{}, e
	}
	return sch, nil
}

func (s *Server) registerPOs(register func(string, string, endpoint)) {
	register("GET /api/v1/pos", "", func(r *http.Request) (any, error) {
		list, e := s.productionOrders(r.Context())
		if e != nil {
			return nil, e
		}
		if s.DB != nil {
			stored := []ProductionOrder{}
			if e := s.state(r.Context(), "po", "current", &stored); e != nil {
				return nil, e
			}
			if len(stored) == 0 {
				// Baca pertama: tanam seed PO demo ke state store.
				res, e := s.change(r.Context(), "po", "current", func(tx *store.Tx, raw json.RawMessage) (store.Change, error) {
					cur, e := basisPOs(s, raw)
					if e != nil {
						return store.Change{}, e
					}
					return store.Change{Value: cur, Event: "po.seeded"}, nil
				})
				if e != nil {
					return nil, e
				}
				return obj(res)["result"], nil
			}
		}
		return list, nil
	})
	register("POST /api/v1/pos", "plan.decide", func(r *http.Request) (any, error) {
		if err := s.dbRequired(); err != nil {
			return nil, err
		}
		var b struct {
			POID        string   `json:"po_id"`
			Priority    *int     `json:"priority"`
			Lines       []POLine `json:"lines"`
			DueDatetime string   `json:"due_datetime"`
			LabelData   string   `json:"label_data"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		if len(b.Lines) == 0 || len(b.Lines) > 20 {
			return nil, invalid("PO harus berisi 1-20 baris SKU.", "lines")
		}
		katalog, e := s.katalogMaster()
		if e != nil {
			return nil, e
		}
		info := infoSKU(katalog)
		for _, line := range b.Lines {
			if _, ok := info[line.SKU]; !ok {
				return nil, invalid("SKU tidak dikenal di master data: "+line.SKU, "lines")
			}
			if line.QtyLiter <= 0 {
				return nil, invalid("Qty liter harus lebih dari 0.", "lines")
			}
		}
		if e := validasiWaktuKedatangan(b.DueDatetime, "due_datetime"); e != nil {
			return nil, e
		}
		prioritas := 1
		if b.Priority != nil {
			if *b.Priority < 0 {
				return nil, invalid("Prioritas tidak boleh negatif.", "priority")
			}
			prioritas = *b.Priority
		}
		label := b.LabelData
		if label == "" {
			label = "simulasi"
		}
		var created ProductionOrder
		result, e := s.change(r.Context(), "po", "current", func(tx *store.Tx, raw json.RawMessage) (store.Change, error) {
			list, e := basisPOs(s, raw)
			if e != nil {
				return store.Change{}, e
			}
			id := strings.TrimSpace(b.POID)
			if id == "" {
				id = fmt.Sprintf("PO-%s-%03d", waktuDemo.Format("20060102"), len(list)+1)
			}
			for _, po := range list {
				if po.POID == id {
					return store.Change{}, invalid("po_id sudah dipakai: "+id, "po_id")
				}
			}
			created = ProductionOrder{
				POID: id, Priority: prioritas, Lines: b.Lines,
				DueDatetime: b.DueDatetime, LabelData: label,
			}
			list = append(list, created)
			return store.Change{Value: list, Event: "po.created"}, nil
		})
		if e != nil {
			return nil, e
		}
		out := result.(map[string]any)
		out["result"] = created
		return out, nil
	})
}

func (s *Server) registerSchedules(register func(string, string, endpoint)) {
	register("POST /api/v1/schedules/generate", "plan.decide", func(r *http.Request) (any, error) {
		if err := s.dbRequired(); err != nil {
			return nil, err
		}
		var b struct {
			TriggeredBy string `json:"triggered_by"`
		}
		if e := decodeOptional(r, &b); e != nil {
			return nil, e
		}
		oleh := strings.TrimSpace(b.TriggeredBy)
		if oleh == "" {
			oleh = "manual"
		}
		sch, e := s.generateSchedule(r.Context(), oleh)
		if e != nil {
			return nil, e
		}
		return map[string]any{"result": sch, "log": nil}, nil
	})
	register("GET /api/v1/schedules/latest", "", func(r *http.Request) (any, error) {
		var sch Schedule
		if e := s.state(r.Context(), "schedule", "current", &sch); e != nil {
			return nil, e
		}
		if sch.ScheduleID == "" {
			return nil, missing()
		}
		return sch, nil
	})
	register("GET /api/v1/schedules", "", func(r *http.Request) (any, error) {
		riwayat := []RingkasanJadwal{}
		if e := s.state(r.Context(), "schedule", "history", &riwayat); e != nil {
			return nil, e
		}
		return riwayat, nil
	})
	register("POST /api/v1/schedules/triggers", "plan.decide", func(r *http.Request) (any, error) {
		if err := s.dbRequired(); err != nil {
			return nil, err
		}
		var b struct {
			ReasonCode string           `json:"reason_code"`
			RefID      string           `json:"ref_id"`
			Detail     string           `json:"detail"`
			Downtime   *DowntimeTrigger `json:"downtime"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		if !reasonCodeValid[b.ReasonCode] {
			return nil, invalid("reason_code tidak valid.", "reason_code")
		}
		if b.Downtime != nil {
			if strings.TrimSpace(b.Downtime.ResourceID) == "" {
				return nil, invalid("Resource downtime wajib diisi.", "downtime")
			}
			mulai, e1 := parseWaktu(b.Downtime.Start)
			selesai, e2 := parseWaktu(b.Downtime.End)
			if e1 != nil || e2 != nil {
				return nil, invalid("Format waktu downtime tidak valid.", "downtime")
			}
			if !selesai.After(mulai) {
				return nil, invalid("Downtime harus berakhir sesudah mulai.", "downtime")
			}
		}
		a, _ := s.authenticate(r)
		var created ScheduleTrigger
		_, e := s.change(r.Context(), "schedule-trigger", "current", func(tx *store.Tx, raw json.RawMessage) (store.Change, error) {
			list := []ScheduleTrigger{}
			if len(raw) > 0 {
				if e := json.Unmarshal(raw, &list); e != nil {
					return store.Change{}, e
				}
			}
			created = ScheduleTrigger{
				ID:         fmt.Sprintf("TRG-%03d", len(list)+1),
				ReasonCode: b.ReasonCode,
				RefID:      b.RefID,
				Detail:     b.Detail,
				Downtime:   b.Downtime,
				CreatedBy:  a.Name,
				CreatedAt:  formatWaktu(waktuDemo),
			}
			list = append(list, created)
			return store.Change{Value: list, Event: "schedule.triggered"}, nil
		})
		if e != nil {
			return nil, e
		}
		sch, e := s.generateSchedule(r.Context(), b.ReasonCode)
		if e != nil {
			return nil, e
		}
		return map[string]any{"result": sch, "trigger": created, "log": nil}, nil
	})
}
