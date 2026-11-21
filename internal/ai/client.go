// Package ai is the Backend client for the MilkGraph AI service, the
// Jev-backed narration service described in "Kontrak API AI MilkGraph" v2.
//
// The Backend computes facts and decisions deterministically; this service
// only assesses those facts through Jev and returns narration composed from
// them. Every failure here is soft: callers must fall back to their local
// behaviour so the AI service can never take the Backend down.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultURL is the AI service address inside the Dokploy docker network.
const DefaultURL = "http://milkgraph-aimilkgraph-byrdm7:8090"

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New builds a client. An empty baseURL falls back to DefaultURL. An empty
// token disables the client (see Enabled).
func New(baseURL, token string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultURL
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 12 * time.Second},
	}
}

// Enabled reports whether the shared service token is configured. Without it
// the AI service rejects every call, so callers skip the service entirely.
func (c *Client) Enabled() bool { return c != nil && c.token != "" }

// Leg is one transport leg of a batch journey (contract: kaki_perjalanan).
type Leg struct {
	Dari        string   `json:"dari"`
	Ke          string   `json:"ke"`
	DurasiMenit float64  `json:"durasi_menit"`
	SuhuRata    *float64 `json:"suhu_rata,omitempty"`
	SuhuMaks    *float64 `json:"suhu_maks,omitempty"`
}

// Violation is one measured parameter outside its limit (contract:
// pelanggaran_batas).
type Violation struct {
	Parameter              string   `json:"parameter"`
	Batas                  float64  `json:"batas"`
	AktualMaks             float64  `json:"aktual_maks"`
	DurasiDiLuarBatasMenit *float64 `json:"durasi_diluar_batas_menit,omitempty"`
}

// Context carries the batch facts the Backend computed (contract: konteks).
type Context struct {
	QCStatus         string      `json:"qc_status"`
	TotalPack        *int        `json:"total_pack,omitempty"`
	KakiPerjalanan   []Leg       `json:"kaki_perjalanan"`
	PelanggaranBatas []Violation `json:"pelanggaran_batas"`
}

// Candidate is one root-cause candidate from the Backend ranking (contract:
// kandidat_akar_masalah). Order matters: index 0 is the Backend's top rank.
type Candidate struct {
	Kode  string   `json:"kode"`
	Skor  float64  `json:"skor"`
	Bukti []string `json:"bukti"`
}

// NarrateRequest is the POST /ai/root-cause/narrate payload.
type NarrateRequest struct {
	BatchID   string      `json:"batch_id"`
	Komoditas string      `json:"komoditas"`
	Konteks   Context     `json:"konteks"`
	Kandidat  []Candidate `json:"kandidat_akar_masalah"`
	LabelData string      `json:"label_data"`
}

// NarrateResponse mirrors the AI service response. The Jev assessment stays
// raw JSON so new Jev fields never break the Backend.
type NarrateResponse struct {
	BatchID         string          `json:"batch_id"`
	PenyebabUtama   string          `json:"penyebab_utama"`
	Narasi          string          `json:"narasi"`
	Rekomendasi     []string        `json:"rekomendasi"`
	Sumber          string          `json:"sumber"`
	LabelData       string          `json:"label_data"`
	Jev             json.RawMessage `json:"jev"`
	SelisihDenganBE bool            `json:"selisih_dengan_be"`
}

// NarrateRootCause sends batch facts to the AI service and returns its
// narration plus the structured Jev assessment.
func (c *Client) NarrateRootCause(ctx context.Context, req NarrateRequest) (*NarrateResponse, error) {
	var out NarrateResponse
	if err := c.post(ctx, "/ai/root-cause/narrate", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChatRequest is the POST /ai/chat payload: the user's question plus the
// batch facts the Backend selected for it (contract: konteks_fakta). The AI
// service answers extractively from these facts only.
type ChatRequest struct {
	Pertanyaan   string   `json:"pertanyaan"`
	KonteksFakta []string `json:"konteks_fakta"`
	LabelData    string   `json:"label_data"`
}

// ChatResponse mirrors the AI service chat response.
type ChatResponse struct {
	Jawaban   string `json:"jawaban"`
	Sumber    string `json:"sumber"`
	LabelData string `json:"label_data"`
}

// Chat sends a question plus Backend-selected facts to the AI service and
// returns its extractive answer.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	var out ChatResponse
	if err := c.post(ctx, "/ai/chat", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PriorityAction is one ranked follow-up action in the repair-priority
// explanation payload (contract: urutan_prioritas). The Backend computes the
// ranking; the AI service only explains it.
type PriorityAction struct {
	Rank       int    `json:"rank"`
	Tindakan   string `json:"tindakan"`
	AlasanKode string `json:"alasan_kode"`
}

// ExplainRequest is the POST /ai/repair-priority/explain payload.
type ExplainRequest struct {
	BatchID            string           `json:"batch_id"`
	UrutanPrioritas    []PriorityAction `json:"urutan_prioritas"`
	PelanggaranSebelum int              `json:"pelanggaran_sebelum"`
	PelanggaranSesudah int              `json:"pelanggaran_sesudah"`
	JumlahConstraint   int              `json:"jumlah_constraint"`
	LabelData          string           `json:"label_data"`
}

// ExplainResponse mirrors the AI service repair-priority response. The Jev
// assessment stays raw JSON so new Jev fields never break the Backend.
type ExplainResponse struct {
	BatchID    string          `json:"batch_id"`
	Penjelasan string          `json:"penjelasan"`
	Sumber     string          `json:"sumber"`
	LabelData  string          `json:"label_data"`
	Jev        json.RawMessage `json:"jev"`
}

// Explain sends a Backend-computed priority ranking to the AI service and
// returns its human-readable explanation plus the structured Jev assessment.
func (c *Client) Explain(ctx context.Context, req ExplainRequest) (*ExplainResponse, error) {
	var out ExplainResponse
	if err := c.post(ctx, "/ai/repair-priority/explain", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) post(ctx context.Context, path string, payload, out any) error {
	if !c.Enabled() {
		return errors.New("ai: token layanan belum dikonfigurasi")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Service-Token", c.token)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("ai: status %d dari layanan AI", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
