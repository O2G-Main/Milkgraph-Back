package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"

	"milkgraph/internal/store"
)

// Analisis biaya (Tahap 3, P3): dua sumber biaya dicatat di state store.
// Pertama, log pemakaian service AI (entity "ai-usage"): setiap panggilan
// Backend ke service AI yang berhasil dicatat dengan ESTIMASI token dari
// ukuran payload JSON (panjang byte / 4, dibulatkan ke atas). Ini estimasi
// kasar untuk rincian biaya proposal, bukan angka tagihan TypeSafe; label
// "estimasi" selalu menyertai ringkasannya. Kedua, item biaya manual
// (entity "cost-item"): charging post, deployment, LLM, dan lainnya yang
// diisi tim lewat API.
//
// Tarif LLM yang dipakai: USD 42 per 1 miliar token input, output gratis
// (harga TypeSafe terverifikasi 2026-11-18).

const tarifLLM = "USD 42 per 1 miliar token input, output gratis (harga TypeSafe terverifikasi 2026-11-18)"

var kategoriBiayaValid = map[string]bool{
	"charging_post": true,
	"deployment":    true,
	"llm":           true,
	"lainnya":       true,
}

type AIUsageEntry struct {
	At              string `json:"at"`
	Endpoint        string `json:"endpoint"` // narrate | chat | explain
	EstInputTokens  int    `json:"est_input_tokens"`
	EstOutputTokens int    `json:"est_output_tokens"`
}

type CostItem struct {
	ID       string  `json:"id"`
	Kategori string  `json:"kategori"`
	Nama     string  `json:"nama"`
	Jumlah   float64 `json:"jumlah"`
	MataUang string  `json:"mata_uang"`
	Catatan  string  `json:"catatan"`
}

// estimasiToken mengestimasi jumlah token dari panjang byte payload JSON:
// 1 token dianggap setara 4 byte, dibulatkan ke atas.
func estimasiToken(nBytes int) int {
	return (nBytes + 3) / 4
}

// catatPemakaianAI mencatat satu panggilan service AI yang berhasil ke log
// pemakaian. Best-effort: tanpa DB tidak dicatat, dan kegagalan pencatatan
// hanya ditulis ke log server, tidak pernah menggagalkan respons pengguna.
func (s *Server) catatPemakaianAI(ctx context.Context, endpoint string, reqPayload, respPayload any) {
	if s.DB == nil {
		return
	}
	reqJSON, e1 := json.Marshal(reqPayload)
	respJSON, e2 := json.Marshal(respPayload)
	if e1 != nil || e2 != nil {
		return
	}
	entry := AIUsageEntry{
		At:              formatWaktu(waktuDemo),
		Endpoint:        endpoint,
		EstInputTokens:  estimasiToken(len(reqJSON)),
		EstOutputTokens: estimasiToken(len(respJSON)),
	}
	_, err := s.change(context.WithoutCancel(ctx), "ai-usage", "current", func(tx *store.Tx, raw json.RawMessage) (store.Change, error) {
		list := []AIUsageEntry{}
		if len(raw) > 0 {
			if e := json.Unmarshal(raw, &list); e != nil {
				return store.Change{}, e
			}
		}
		list = append(list, entry)
		return store.Change{Value: list, Event: "ai-usage.recorded"}, nil
	})
	if err != nil {
		slog.Warn("pencatatan pemakaian AI gagal", "endpoint", endpoint, "error", err)
	}
}

func (s *Server) aiUsageEntries(ctx context.Context) ([]AIUsageEntry, error) {
	list := []AIUsageEntry{}
	if e := s.state(ctx, "ai-usage", "current", &list); e != nil {
		return nil, e
	}
	return list, nil
}

func (s *Server) costItems(ctx context.Context) ([]CostItem, error) {
	list := []CostItem{}
	if e := s.state(ctx, "cost-item", "current", &list); e != nil {
		return nil, e
	}
	return list, nil
}

// RingkasanLLM adalah agregat log pemakaian AI beserta estimasi biayanya.
type RingkasanLLM struct {
	Calls           int     `json:"calls"`
	EstInputTokens  int     `json:"est_input_tokens"`
	EstOutputTokens int     `json:"est_output_tokens"`
	EstCostUSD      float64 `json:"est_cost_usd"`
	Tarif           string  `json:"tarif"`
	Label           string  `json:"label"`
}

// hitungBiayaLLM mengagregasi log pemakaian: biaya hanya dari token input
// (output gratis menurut tarif), dibulatkan ke 4 desimal USD.
func hitungBiayaLLM(entries []AIUsageEntry) RingkasanLLM {
	out := RingkasanLLM{Tarif: tarifLLM, Label: "estimasi"}
	for _, e := range entries {
		out.Calls++
		out.EstInputTokens += e.EstInputTokens
		out.EstOutputTokens += e.EstOutputTokens
	}
	out.EstCostUSD = math.Round(float64(out.EstInputTokens)*42/1e9*10000) / 10000
	return out
}

// totalItemPerMataUang menjumlahkan item biaya per mata uang; IDR dan USD
// selalu hadir sebagai kunci walau totalnya nol.
func totalItemPerMataUang(items []CostItem) map[string]float64 {
	out := map[string]float64{"IDR": 0, "USD": 0}
	for _, it := range items {
		out[it.MataUang] += it.Jumlah
	}
	for k, v := range out {
		out[k] = math.Round(v*100) / 100
	}
	return out
}

// RingkasanBiaya adalah bentuk respons GET /api/v1/costs.
type RingkasanBiaya struct {
	LLM                  RingkasanLLM       `json:"llm"`
	ItemsTotalByCurrency map[string]float64 `json:"items_total_by_currency"`
	Items                []CostItem         `json:"items"`
}

func (s *Server) registerBiaya(register func(string, string, endpoint)) {
	register("GET /api/v1/costs/items", "", func(r *http.Request) (any, error) {
		return s.costItems(r.Context())
	})
	register("POST /api/v1/costs/items", "cost.edit", func(r *http.Request) (any, error) {
		if err := s.dbRequired(); err != nil {
			return nil, err
		}
		var b struct {
			Kategori string   `json:"kategori"`
			Nama     string   `json:"nama"`
			Jumlah   *float64 `json:"jumlah"`
			MataUang string   `json:"mata_uang"`
			Catatan  string   `json:"catatan"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		if !kategoriBiayaValid[b.Kategori] {
			return nil, invalid("Kategori biaya tidak valid.", "kategori")
		}
		if strings.TrimSpace(b.Nama) == "" {
			return nil, invalid("Nama item wajib diisi.", "nama")
		}
		if b.Jumlah == nil || *b.Jumlah <= 0 {
			return nil, invalid("Jumlah harus lebih dari 0.", "jumlah")
		}
		mataUang := b.MataUang
		if mataUang == "" {
			mataUang = "IDR"
		}
		if mataUang != "IDR" && mataUang != "USD" {
			return nil, invalid("Mata uang harus IDR atau USD.", "mata_uang")
		}
		var created CostItem
		result, e := s.change(r.Context(), "cost-item", "current", func(tx *store.Tx, raw json.RawMessage) (store.Change, error) {
			list := []CostItem{}
			if len(raw) > 0 {
				if e := json.Unmarshal(raw, &list); e != nil {
					return store.Change{}, e
				}
			}
			created = CostItem{
				ID:       fmt.Sprintf("CST-%03d", len(list)+1),
				Kategori: b.Kategori, Nama: b.Nama, Jumlah: *b.Jumlah,
				MataUang: mataUang, Catatan: b.Catatan,
			}
			list = append(list, created)
			return store.Change{Value: list, Event: "cost-item.created"}, nil
		})
		if e != nil {
			return nil, e
		}
		out := result.(map[string]any)
		out["result"] = created
		return out, nil
	})
	register("GET /api/v1/costs", "", func(r *http.Request) (any, error) {
		entries, e := s.aiUsageEntries(r.Context())
		if e != nil {
			return nil, e
		}
		items, e := s.costItems(r.Context())
		if e != nil {
			return nil, e
		}
		return RingkasanBiaya{
			LLM:                  hitungBiayaLLM(entries),
			ItemsTotalByCurrency: totalItemPerMataUang(items),
			Items:                items,
		}, nil
	})
}
