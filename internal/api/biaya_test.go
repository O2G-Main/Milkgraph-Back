package api

import (
	"encoding/json"
	"strings"
	"testing"

	"milkgraph/internal/ai"
)

func TestEstimasiToken(t *testing.T) {
	kasus := []struct{ n, mau int }{
		{0, 0}, {1, 1}, {4, 1}, {5, 2}, {100, 25}, {101, 26},
	}
	for _, k := range kasus {
		if got := estimasiToken(k.n); got != k.mau {
			t.Errorf("estimasiToken(%d) = %d, mau %d", k.n, got, k.mau)
		}
	}
}

func TestHitungBiayaLLM(t *testing.T) {
	entries := []AIUsageEntry{
		{Endpoint: "narrate", EstInputTokens: 10_000_000, EstOutputTokens: 500},
		{Endpoint: "chat", EstInputTokens: 15_000_000, EstOutputTokens: 700},
		{Endpoint: "explain", EstInputTokens: 0, EstOutputTokens: 0},
	}
	r := hitungBiayaLLM(entries)
	if r.Calls != 3 || r.EstInputTokens != 25_000_000 || r.EstOutputTokens != 1200 {
		t.Fatalf("agregat salah: %+v", r)
	}
	// 25.000.000 token input x USD 42 / 1 miliar = USD 1,05.
	if r.EstCostUSD != 1.05 {
		t.Errorf("est_cost_usd = %v, mau 1.05", r.EstCostUSD)
	}
	if r.Label != "estimasi" || !strings.Contains(r.Tarif, "42") {
		t.Errorf("label/tarif harus menyebut estimasi dan tarif 42: %+v", r)
	}

	kosong := hitungBiayaLLM(nil)
	if kosong.Calls != 0 || kosong.EstCostUSD != 0 {
		t.Errorf("log kosong harus nol: %+v", kosong)
	}
}

func TestTotalItemPerMataUang(t *testing.T) {
	items := []CostItem{
		{Kategori: "deployment", Jumlah: 100.25, MataUang: "IDR"},
		{Kategori: "charging_post", Jumlah: 200, MataUang: "IDR"},
		{Kategori: "llm", Jumlah: 5, MataUang: "USD"},
	}
	total := totalItemPerMataUang(items)
	if total["IDR"] != 300.25 || total["USD"] != 5 {
		t.Errorf("total = %v, mau IDR 300.25 dan USD 5", total)
	}
	kosong := totalItemPerMataUang(nil)
	if kosong["IDR"] != 0 || kosong["USD"] != 0 {
		t.Errorf("total kosong harus memuat IDR dan USD bernilai 0: %v", kosong)
	}
}

func TestBiayaButuhDB(t *testing.T) {
	h := (&Server{Mode: "demo"}).Handler()
	w := durableRequest(h, "POST", "/costs/items", "supervisor", `{"kategori":"deployment","nama":"VPS","jumlah":1000}`)
	if w.Code != 503 {
		t.Fatalf("POST /costs/items tanpa DB harus 503, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "GET", "/costs/items", "planner", "")
	if w.Code != 200 {
		t.Fatalf("GET /costs/items tanpa DB harus 200, dapat %d: %s", w.Code, w.Body.String())
	}
	var items []CostItem
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 0 {
		t.Fatalf("GET /costs/items tanpa DB harus daftar kosong: %s", w.Body.String())
	}
	w = durableRequest(h, "GET", "/costs", "planner", "")
	if w.Code != 200 {
		t.Fatalf("GET /costs tanpa DB harus 200, dapat %d: %s", w.Code, w.Body.String())
	}
	r := decodeMap(t, w)
	llm := obj(r["llm"])
	if llm["calls"] != 0.0 || llm["est_cost_usd"] != 0.0 || llm["label"] != "estimasi" {
		t.Errorf("ringkasan LLM kosong tidak sesuai: %v", llm)
	}
	total := obj(r["items_total_by_currency"])
	if total["IDR"] != 0.0 || total["USD"] != 0.0 {
		t.Errorf("total per mata uang harus memuat IDR dan USD: %v", total)
	}
}

func TestBiayaDenganDB(t *testing.T) {
	db := testDB(t)
	if err := db.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	stub := stubAI(t)
	h := (&Server{Mode: "demo", DB: db, AI: ai.New(stub.URL, "token-uji")}).Handler()

	// Validasi item biaya.
	w := durableRequest(h, "POST", "/costs/items", "supervisor", `{"kategori":"ngawur","nama":"X","jumlah":100}`)
	if w.Code != 422 {
		t.Fatalf("POST kategori ngawur harus 422, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/costs/items", "supervisor", `{"kategori":"deployment","nama":"X","jumlah":0}`)
	if w.Code != 422 {
		t.Fatalf("POST jumlah 0 harus 422, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/costs/items", "supervisor", `{"kategori":"deployment","nama":"X","jumlah":100,"mata_uang":"EUR"}`)
	if w.Code != 422 {
		t.Fatalf("POST mata uang EUR harus 422, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/costs/items", "supervisor", `{"kategori":"deployment","nama":"VPS JagoanHosting","jumlah":1500000,"catatan":"per bulan"}`)
	if w.Code != 200 {
		t.Fatalf("POST /costs/items = %d: %s", w.Code, w.Body.String())
	}
	created := obj(decodeMap(t, w)["result"])
	if created["id"] != "CST-001" || created["mata_uang"] != "IDR" {
		t.Errorf("item pertama harus CST-001 ber mata uang bawaan IDR: %v", created)
	}
	w = durableRequest(h, "POST", "/costs/items", "supervisor", `{"kategori":"llm","nama":"Saldo TypeSafe","jumlah":5,"mata_uang":"USD"}`)
	if w.Code != 200 {
		t.Fatalf("POST /costs/items USD = %d: %s", w.Code, w.Body.String())
	}

	// Tiga jalur AI yang berhasil harus tercatat di log pemakaian.
	w = durableRequest(h, "POST", "/assistant/narrate", "planner", `{"batch_id":"FG-1013-07"}`)
	if w.Code != 200 {
		t.Fatalf("POST /assistant/narrate = %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/assistant/chat", "planner", `{"question":"Kenapa batch ini out-of-spec?"}`)
	if w.Code != 200 {
		t.Fatalf("POST /assistant/chat = %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/schedules/generate", "planner", `{}`)
	if w.Code != 200 {
		t.Fatalf("POST /schedules/generate = %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/schedules/narrate", "planner", `{}`)
	if w.Code != 200 {
		t.Fatalf("POST /schedules/narrate = %d: %s", w.Code, w.Body.String())
	}

	w = durableRequest(h, "GET", "/costs", "planner", "")
	r := decodeMap(t, w)
	llm := obj(r["llm"])
	if llm["calls"] != 3.0 {
		t.Fatalf("calls harus 3 (narrate, chat, explain), dapat %v: %s", llm["calls"], w.Body.String())
	}
	if llm["est_input_tokens"].(float64) <= 0 || llm["est_output_tokens"].(float64) <= 0 {
		t.Errorf("estimasi token harus positif: %v", llm)
	}
	total := obj(r["items_total_by_currency"])
	if total["IDR"] != 1500000.0 || total["USD"] != 5.0 {
		t.Errorf("total item tidak sesuai: %v", total)
	}
	if len(arr(r["items"])) != 2 {
		t.Errorf("items harus 2: %v", r["items"])
	}

	// Log pemakaian mentah: endpoint tercatat sesuai jalur.
	entries, e := (&Server{DB: db}).aiUsageEntries(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	perEndpoint := map[string]int{}
	for _, en := range entries {
		perEndpoint[en.Endpoint]++
		if en.At != formatWaktu(waktuDemo) {
			t.Errorf("stempel log harus jam demo: %v", en)
		}
	}
	if perEndpoint["narrate"] != 1 || perEndpoint["chat"] != 1 || perEndpoint["explain"] != 1 {
		t.Errorf("log per endpoint salah: %v", perEndpoint)
	}
}
