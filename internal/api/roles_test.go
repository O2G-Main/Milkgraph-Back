package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSeedRegistriRoleMatriks(t *testing.T) {
	reg := seedRoleRegistry()
	if len(reg) != 7 {
		t.Fatalf("registri seed harus 7 role, dapat %d", len(reg))
	}
	ingin := map[string][]string{
		"superadmin": {"constraint.edit", "cost.edit", "demand.send", "downtime.record", "hold.review", "hold.submit", "instruction.ack", "lab.decide", "plan.decide", "role.manage", "rt.manage", "transfer.record"},
		// Basis roles.json (demand.send, plan.decide, constraint.edit,
		// hold.submit) + ekstensi Tahap 3.
		"planner":    {"constraint.edit", "demand.send", "hold.submit", "instruction.ack", "plan.decide", "rt.manage", "transfer.record"},
		"receiving":  {"hold.submit", "instruction.ack", "rt.manage", "transfer.record"},
		"lab":        {"lab.decide"},
		"operator":   {"downtime.record", "instruction.ack", "transfer.record"},
		"qa":         {"constraint.edit", "hold.review", "hold.submit", "lab.decide"},
		"supervisor": {"cost.edit", "hold.review", "hold.submit", "instruction.ack"},
	}
	for _, rec := range reg {
		want, ok := ingin[rec.RoleID]
		if !ok {
			t.Fatalf("role tak terduga di seed: %s", rec.RoleID)
		}
		if !rec.Builtin {
			t.Errorf("role seed %s harus builtin", rec.RoleID)
		}
		if len(rec.Permissions) != len(want) {
			t.Fatalf("permission %s = %v, mau %v", rec.RoleID, rec.Permissions, want)
		}
		for i, p := range want {
			if rec.Permissions[i] != p {
				t.Fatalf("permission %s = %v, mau %v", rec.RoleID, rec.Permissions, want)
			}
		}
	}
}

func TestRolesShapeDanKatalog(t *testing.T) {
	h := (&Server{Mode: "demo"}).Handler()
	w := durableRequest(h, "GET", "/roles", "planner", "")
	if w.Code != 200 {
		t.Fatalf("GET /roles = %d: %s", w.Code, w.Body.String())
	}
	body := decodeMap(t, w)
	roles := obj(body["roles"])
	if len(roles) != 7 {
		t.Fatalf("GET /roles harus memuat 7 role: %s", w.Body.String())
	}
	planner := obj(roles["planner"])
	if planner["label"] != "Planner PPIC" || planner["user"] != "Rina Wulandari" || planner["initials"] != "RW" || planner["builtin"] != true {
		t.Errorf("entri planner kehilangan field lama/baru: %v", planner)
	}
	if len(arr(planner["permissions"])) != 7 {
		t.Errorf("permissions planner di GET /roles: %v", planner["permissions"])
	}
	sa := obj(roles["superadmin"])
	if sa["label"] != "Superadmin" || len(arr(sa["permissions"])) != len(katalogPermission) {
		t.Errorf("entri superadmin tidak sesuai: %v", sa)
	}
	perms := obj(body["permissions"])
	if len(perms) != len(katalogPermission) {
		t.Fatalf("peta permissions harus %d entri, dapat %d", len(katalogPermission), len(perms))
	}
	if holders := arr(obj(perms["role.manage"])["roles"]); len(holders) != 1 || holders[0] != "superadmin" {
		t.Errorf("role.manage hanya boleh dipegang superadmin: %v", holders)
	}

	w = durableRequest(h, "GET", "/permissions", "qa", "")
	if w.Code != 200 {
		t.Fatalf("GET /permissions = %d: %s", w.Code, w.Body.String())
	}
	var katalog []PermissionInfo
	if err := json.Unmarshal(w.Body.Bytes(), &katalog); err != nil || len(katalog) != len(katalogPermission) {
		t.Fatalf("katalog permission tidak sesuai: %s", w.Body.String())
	}
	for _, p := range katalog {
		if p.Name == "" || p.Label == "" {
			t.Errorf("entri katalog kosong: %+v", p)
		}
	}
}

func TestOtorisasiEndpointTahap3(t *testing.T) {
	h := (&Server{Mode: "demo"}).Handler()
	kasus := []struct {
		nama, method, path, role, body string
		want                           int
	}{
		// receiving memegang rt.manage: lolos otorisasi, berhenti di 503
		// karena tidak ada DB. Tanpa grant akan berhenti di 403.
		{"receiving buat rt-arrival", "POST", "/supply/rt-arrivals", "receiving", `{"farm_name":"Farm X","rt_id":"RT-01","volume_l":1000,"depart_at":"2026-10-14T10:00:00+07:00","target_arrival":"2026-10-14T13:00:00+07:00","estimated_arrival":"2026-10-14T13:00:00+07:00"}`, 503},
		{"receiving generate jadwal", "POST", "/schedules/generate", "receiving", `{}`, 403},
		{"planner generate jadwal", "POST", "/schedules/generate", "planner", `{}`, 503},
		{"planner buat po", "POST", "/pos", "planner", `{"lines":[{"sku":"UHT-CH-250-LOCAL","qty_liter":100}],"due_datetime":"2026-10-15T08:00:00+07:00"}`, 503},
		{"receiving buat po", "POST", "/pos", "receiving", `{"lines":[{"sku":"UHT-CH-250-LOCAL","qty_liter":100}],"due_datetime":"2026-10-15T08:00:00+07:00"}`, 403},
		{"operator catat downtime", "POST", "/downtime-events", "operator", `{"resource_id":"FL-01","start":"2026-10-14T08:00:00+07:00","end":"2026-10-14T09:00:00+07:00","type":"breakdown"}`, 503},
		{"operator buat item biaya", "POST", "/costs/items", "operator", `{"kategori":"deployment","nama":"VPS","jumlah":1000}`, 403},
		{"supervisor buat item biaya", "POST", "/costs/items", "supervisor", `{"kategori":"deployment","nama":"VPS","jumlah":1000}`, 503},
		{"operator catat transfer", "POST", "/trace/transfers", "operator", `{"batch_id":"FG-1013-07","from_ref":"SP-1","to_ref":"ST-4","volume_out_l":100,"volume_in_l":100}`, 503},
		{"qa catat transfer", "POST", "/trace/transfers", "qa", `{"batch_id":"FG-1013-07","from_ref":"SP-1","to_ref":"ST-4","volume_out_l":100,"volume_in_l":100}`, 403},
		{"planner kelola role", "POST", "/roles", "planner", `{"role_id":"auditor","nama":"Auditor","permissions":[]}`, 403},
		{"superadmin kelola role", "POST", "/roles", "superadmin", `{"role_id":"auditor","nama":"Auditor","permissions":[]}`, 503},
	}
	for _, c := range kasus {
		w := durableRequest(h, c.method, c.path, c.role, c.body)
		if w.Code != c.want {
			t.Errorf("%s: %s %s sebagai %s = %d, mau %d: %s", c.nama, c.method, c.path, c.role, w.Code, c.want, w.Body.String())
		}
	}
}

func TestSesiRoleBaru(t *testing.T) {
	h := (&Server{Mode: "demo"}).Handler()
	for role, perm := range map[string]string{"superadmin": "role.manage", "lab": "lab.decide", "operator": "downtime.record"} {
		w := durableRequest(h, "GET", "/session", role, "")
		if w.Code != 200 {
			t.Fatalf("GET /session sebagai %s = %d: %s", role, w.Code, w.Body.String())
		}
		sess := decodeMap(t, w)
		if sess["role"] != role {
			t.Errorf("sesi role = %v, mau %s", sess["role"], role)
		}
		found := false
		for _, p := range arr(sess["permissions"]) {
			if p == perm {
				found = true
			}
		}
		if !found {
			t.Errorf("sesi %s harus memuat %s: %v", role, perm, sess["permissions"])
		}
	}
	// Role lama tidak berubah persona maupun grant basisnya.
	w := durableRequest(h, "GET", "/session", "qa", "")
	sess := decodeMap(t, w)
	if obj(sess["user"])["name"] != "Sri Lestari" {
		t.Errorf("persona qa berubah: %v", sess["user"])
	}
}

func TestRoleCRUDDenganDB(t *testing.T) {
	db := testDB(t)
	if err := db.Reset(t.Context()); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Mode: "demo", DB: db}).Handler()

	// Buat role kustom sebagai superadmin.
	w := durableRequest(h, "POST", "/roles", "superadmin", `{"role_id":"auditor","nama":"Auditor Mutu","permissions":["transfer.record"]}`)
	if w.Code != 200 {
		t.Fatalf("POST /roles = %d: %s", w.Code, w.Body.String())
	}
	created := obj(decodeMap(t, w)["result"])
	if created["role_id"] != "auditor" || created["builtin"] != false {
		t.Errorf("role kustom tidak sesuai: %v", created)
	}

	// Role kustom langsung bisa dipakai sebagai X-Demo-Role.
	w = durableRequest(h, "POST", "/trace/transfers", "auditor", `{"batch_id":"FG-1013-07","from_ref":"SP-1","to_ref":"ST-4","volume_out_l":100,"volume_in_l":98}`)
	if w.Code != 200 {
		t.Fatalf("auditor POST transfer = %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/schedules/generate", "auditor", `{}`)
	if w.Code != 403 {
		t.Fatalf("auditor POST generate harus 403, dapat %d: %s", w.Code, w.Body.String())
	}

	// Edit permission: tambah downtime.record, lalu buktikan berlaku.
	w = durableRequest(h, "PUT", "/roles/auditor", "superadmin", `{"permissions":["downtime.record","transfer.record"]}`)
	if w.Code != 200 {
		t.Fatalf("PUT /roles/auditor = %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "POST", "/downtime-events", "auditor", `{"resource_id":"FL-01","start":"2026-10-14T08:00:00+07:00","end":"2026-10-14T09:00:00+07:00","type":"breakdown"}`)
	if w.Code != 200 {
		t.Fatalf("auditor POST downtime sesudah edit = %d: %s", w.Code, w.Body.String())
	}

	// Pengaman-pengaman.
	w = durableRequest(h, "PUT", "/roles/auditor", "superadmin", `{"permissions":["terbang"]}`)
	if w.Code != 422 {
		t.Fatalf("PUT permission tak dikenal harus 422, dapat %d", w.Code)
	}
	w = durableRequest(h, "POST", "/roles", "superadmin", `{"role_id":"penipu","nama":"Penipu","permissions":["terbang"]}`)
	if w.Code != 422 {
		t.Fatalf("POST permission tak dikenal harus 422, dapat %d", w.Code)
	}
	w = durableRequest(h, "POST", "/roles", "superadmin", `{"role_id":"auditor","nama":"Duplikat","permissions":[]}`)
	if w.Code != 422 {
		t.Fatalf("POST role duplikat harus 422, dapat %d", w.Code)
	}
	w = durableRequest(h, "DELETE", "/roles/planner", "superadmin", "")
	if w.Code != 422 {
		t.Fatalf("DELETE role bawaan harus 422, dapat %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "PUT", "/roles/superadmin", "superadmin", `{"permissions":["plan.decide"]}`)
	if w.Code != 422 {
		t.Fatalf("PUT superadmin tanpa role.manage harus 422, dapat %d", w.Code)
	}
	w = durableRequest(h, "POST", "/roles", "planner", `{"role_id":"ilegal","nama":"Ilegal","permissions":[]}`)
	if w.Code != 403 {
		t.Fatalf("POST /roles sebagai planner harus 403, dapat %d", w.Code)
	}

	// Perubahan tercatat di audit atas nama aktor.
	w = durableRequest(h, "GET", "/audit", "superadmin", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Role baru dibuat") {
		t.Fatalf("audit harus memuat pembuatan role: %d %s", w.Code, w.Body.String())
	}

	// Hapus role kustom; sesudahnya header role itu tidak lagi sah.
	w = durableRequest(h, "DELETE", "/roles/auditor", "superadmin", "")
	if w.Code != 200 {
		t.Fatalf("DELETE /roles/auditor = %d: %s", w.Code, w.Body.String())
	}
	w = durableRequest(h, "GET", "/session", "auditor", "")
	if w.Code != 401 {
		t.Fatalf("auditor sesudah dihapus harus 401, dapat %d", w.Code)
	}
	w = durableRequest(h, "GET", "/roles", "superadmin", "")
	if strings.Contains(w.Body.String(), `"auditor"`) {
		t.Fatalf("auditor masih tampil di GET /roles: %s", w.Body.String())
	}
}
