package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExplainMengirimTokenDanMemparsingRespons(t *testing.T) {
	var gotToken, gotPath string
	var gotBody ExplainRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotToken = r.Header.Get("X-Service-Token")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"batch_id":"SCH-20261014-v2","penjelasan":"Penjelasan uji.","sumber":"jev","label_data":"simulasi","jev":{"keyakinan_skor":75,"risiko_jika_ditunda":0.83,"model":"jev-1.14.0"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "token-uji")
	resp, err := c.Explain(context.Background(), ExplainRequest{
		BatchID: "SCH-20261014-v2",
		UrutanPrioritas: []PriorityAction{
			{Rank: 1, Tindakan: "Terapkan jadwal versi 2", AlasanKode: "DOWNTIME"},
			{Rank: 2, Tindakan: "PO PO-1 berstatus LATE", AlasanKode: "DOWNTIME"},
		},
		PelanggaranSebelum: 2,
		PelanggaranSesudah: 4,
		JumlahConstraint:   5,
		LabelData:          "simulasi",
	})
	if err != nil {
		t.Fatalf("Explain error: %v", err)
	}
	if gotToken != "token-uji" {
		t.Errorf("header X-Service-Token tidak terkirim")
	}
	if gotPath != "/ai/repair-priority/explain" {
		t.Errorf("path = %q, mau /ai/repair-priority/explain", gotPath)
	}
	if gotBody.BatchID != "SCH-20261014-v2" || len(gotBody.UrutanPrioritas) != 2 ||
		gotBody.UrutanPrioritas[0].AlasanKode != "DOWNTIME" ||
		gotBody.PelanggaranSebelum != 2 || gotBody.PelanggaranSesudah != 4 ||
		gotBody.JumlahConstraint != 5 || gotBody.LabelData != "simulasi" {
		t.Errorf("body terkirim tidak sesuai: %+v", gotBody)
	}
	if resp.Penjelasan != "Penjelasan uji." || resp.Sumber != "jev" || resp.LabelData != "simulasi" {
		t.Errorf("respons terparsing tidak sesuai: %+v", resp)
	}
	if !strings.Contains(string(resp.Jev), `"keyakinan_skor":75`) {
		t.Errorf("jev mentah harus diteruskan apa adanya: %s", resp.Jev)
	}
}

func TestExplainTanpaTokenError(t *testing.T) {
	c := New("", "")
	if _, err := c.Explain(context.Background(), ExplainRequest{BatchID: "SCH-1"}); err == nil {
		t.Fatal("Explain tanpa token harus error, bukan diam-diam berhasil")
	}
}
