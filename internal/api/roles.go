package api

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"milkgraph/internal/domain"
	"milkgraph/internal/store"
)

// Registri role Tahap 3.
//
// Dokumen fitur Tahap 3 (Pak Aji / Mas Nico) mendefinisikan 7 role dengan
// matriks menu/tindakan dan pengelolaan role dinamis oleh Superadmin lewat
// Pengaturan -> Role & Hak Akses. Keputusan "matriks statis vs CRUD dinamis"
// masih terbuka, jadi implementasi ini memilih jalan tengah yang aman:
// registri disimpan dinamis di state store (entity "role", id "current"),
// tetapi isinya di-seed dari matriks terdokumentasi di bawah. Bila tim
// akhirnya memilih statis, seed inilah matriks statisnya; bila memilih
// dinamis, Superadmin dapat mengubahnya lewat API tanpa deploy ulang.
//
// Lapisan auth (authenticate/authorize di middleware.go) selalu membaca
// registri ini: bila state store punya data role, itu yang dipakai; bila
// tidak (termasuk tanpa DB sama sekali), fallback ke seedRoleRegistry()
// sehingga perilakunya identik.

// RoleRecord adalah satu baris registri role.
type RoleRecord struct {
	RoleID      string   `json:"role_id"`
	Nama        string   `json:"nama"`
	Builtin     bool     `json:"builtin"`
	Permissions []string `json:"permissions"`
}

// PermissionInfo adalah satu entri katalog permission untuk editor role FE.
type PermissionInfo struct {
	Name  string `json:"name"`
	Label string `json:"label"`
}

// katalogPermission adalah daftar lengkap permission yang dikenal sistem.
// Tujuh pertama berasal dari seed/roles.json (label dipertahankan persis);
// lima terakhir adalah permission tulis fitur Tahap 3.
var katalogPermission = []PermissionInfo{
	{Name: "demand.send", Label: "Mengirim kebutuhan susu ke farm"},
	{Name: "plan.decide", Label: "Menyetujui atau menolak rekomendasi rencana"},
	{Name: "constraint.edit", Label: "Mengubah batas constraint"},
	{Name: "instruction.ack", Label: "Mengonfirmasi instruksi dari rencana yang disetujui"},
	{Name: "hold.submit", Label: "Mengajukan daftar hold dari hasil trace"},
	{Name: "hold.review", Label: "Menyetujui, menolak, atau melepas hold"},
	{Name: "lab.decide", Label: "Merilis atau menolak hasil lab penerimaan RT"},
	{Name: "rt.manage", Label: "Mengelola jadwal kedatangan road tanker"},
	{Name: "transfer.record", Label: "Mencatat transfer dan volume susu antar titik"},
	{Name: "downtime.record", Label: "Mencatat kejadian downtime alat"},
	{Name: "cost.edit", Label: "Mengelola item biaya operasional"},
	{Name: "role.manage", Label: "Mengelola role dan hak akses pengguna"},
}

func permissionDikenal(name string) bool {
	for _, p := range katalogPermission {
		if p.Name == name {
			return true
		}
	}
	return false
}

// ekstensiRoleTahap3 adalah tambahan grant di luar basis roles.json,
// diturunkan dari rincian per role di dokumen fitur Tahap 3:
//
//   - planner (Planner/PPIC): mengatur target Penjadwalan dan menyetujui
//     perubahan jadwal (plan.decide sudah basis), menjalankan instruksi
//     tindak lanjut (instruction.ack), melihat/mengatur data pendukung
//     perencanaan termasuk kedatangan RT (rt.manage) dan catatan transfer
//     (transfer.record).
//   - receiving (Penerimaan): mengelola Jadwal Kedatangan RT (rt.manage),
//     mencatat Transfer & Volume dari RT ke tangki penerimaan
//     (transfer.record), dan mengajukan hold dari antrean (hold.submit
//     sudah separuh basis lewat trace; ditegaskan di sini).
//   - lab (Lab): memasukkan dan memutuskan hasil pemeriksaan (lab.decide).
//   - operator (Operator Produksi): mencatat Transfer & Volume pada alat
//     yang ditugaskan (transfer.record), memperbarui Kondisi Alat dan
//     mencatat downtime (downtime.record), mengonfirmasi instruksi pada
//     lininya (instruction.ack).
//   - qa (QA/QC): keputusan mutu dan hold (basis) plus mengatur batas
//     holding time dan ketentuan mutu di Aturan Produksi (constraint.edit).
//   - supervisor (Supervisor/Manager): memantau Executive Dashboard,
//     meninjau hold (hold.review), dan mengajukan simulasi Rencana;
//     biaya operasional berada di area manajerial (cost.edit).
var ekstensiRoleTahap3 = map[string][]string{
	"planner":    {"instruction.ack", "rt.manage", "transfer.record"},
	"receiving":  {"hold.submit", "rt.manage", "transfer.record"},
	"lab":        {"lab.decide"},
	"operator":   {"downtime.record", "instruction.ack", "transfer.record"},
	"qa":         {"constraint.edit"},
	"supervisor": {"cost.edit", "hold.review"},
}

// urutanRoleSeed adalah urutan tampil registri bawaan.
var urutanRoleSeed = []string{"superadmin", "planner", "receiving", "lab", "operator", "qa", "supervisor"}

// namaRoleSeed adalah nama tampil role baru; empat role lama mengambil
// label dari domain.RolesMap agar konsisten dengan roles.json.
var namaRoleSeed = map[string]string{
	"superadmin": "Superadmin",
	"lab":        "Lab",
	"operator":   "Operator Produksi",
}

// seedRoleRegistry membangun matriks bawaan: basis grant diambil dari
// domain.PermsMap (cermin persis seed/roles.json, jadi assignment lama
// selalu menang sebagai basis), lalu ditambah ekstensiRoleTahap3, dan
// superadmin menerima seluruh katalog termasuk role.manage.
func seedRoleRegistry() []RoleRecord {
	grant := map[string]map[string]bool{}
	tambah := func(role, perm string) {
		if grant[role] == nil {
			grant[role] = map[string]bool{}
		}
		grant[role][perm] = true
	}
	for perm, def := range domain.PermsMap {
		for _, r := range def.Roles {
			tambah(string(r), string(perm))
		}
	}
	for role, perms := range ekstensiRoleTahap3 {
		for _, p := range perms {
			tambah(role, p)
		}
	}
	for _, p := range katalogPermission {
		tambah("superadmin", p.Name)
	}
	out := []RoleRecord{}
	for _, id := range urutanRoleSeed {
		nama := namaRoleSeed[id]
		if def, ok := domain.RolesMap[domain.RoleId(id)]; ok {
			nama = def.Label
		}
		perms := []string{}
		for p := range grant[id] {
			perms = append(perms, p)
		}
		sort.Strings(perms)
		out = append(out, RoleRecord{RoleID: id, Nama: nama, Builtin: true, Permissions: perms})
	}
	return out
}

// roleRegistry membaca registri aktif: isi state store bila ada, jika
// tidak fallback ke seed. Tanpa DB fungsi ini murni mengembalikan seed.
func (s *Server) roleRegistry(ctx context.Context) ([]RoleRecord, error) {
	var stored []RoleRecord
	if err := s.state(ctx, "role", "current", &stored); err != nil {
		return nil, err
	}
	if len(stored) > 0 {
		return stored, nil
	}
	return seedRoleRegistry(), nil
}

func cariRole(reg []RoleRecord, id string) (RoleRecord, bool) {
	for _, r := range reg {
		if r.RoleID == id {
			return r, true
		}
	}
	return RoleRecord{}, false
}

// roleCan menjawab apakah role memegang permission menurut registri aktif.
func (s *Server) roleCan(ctx context.Context, roleID, perm string) (bool, error) {
	if !permissionDikenal(perm) {
		return false, nil
	}
	reg, err := s.roleRegistry(ctx)
	if err != nil {
		return false, err
	}
	rec, ok := cariRole(reg, roleID)
	if !ok {
		return false, nil
	}
	for _, p := range rec.Permissions {
		if p == perm {
			return true, nil
		}
	}
	return false, nil
}

// whoCanMessage menyusun pesan 403 dengan format yang sama seperti
// domain.WhoCan, tetapi nama role diambil dari registri aktif.
func (s *Server) whoCanMessage(ctx context.Context, perm string) string {
	reg, err := s.roleRegistry(ctx)
	if err != nil {
		return domain.WhoCan(domain.Permission(perm))
	}
	names := []string{}
	for _, rec := range reg {
		for _, p := range rec.Permissions {
			if p == perm {
				names = append(names, rec.Nama)
				break
			}
		}
	}
	if len(names) == 0 {
		return "Hanya pengguna berwenang yang dapat melakukan ini."
	}
	if len(names) == 1 {
		return "Hanya " + names[0] + " yang dapat melakukan ini."
	}
	return "Hanya " + strings.Join(names, " atau ") + " yang dapat melakukan ini."
}

// permissionsOf mengembalikan daftar permission role dari registri aktif
// (nil bila role tidak dikenal), terurut menaik.
func (s *Server) permissionsOf(ctx context.Context, roleID string) ([]string, error) {
	reg, err := s.roleRegistry(ctx)
	if err != nil {
		return nil, err
	}
	rec, ok := cariRole(reg, roleID)
	if !ok {
		return nil, nil
	}
	out := append([]string{}, rec.Permissions...)
	sort.Strings(out)
	return out, nil
}

// displayRole mengembalikan label dan inisial untuk tampilan sesi: empat
// role lama memakai persona domain.RolesMap, role lain memakai nama
// registri dengan inisial turunan.
func displayRole(rec RoleRecord) (label, user, initials string) {
	if def, ok := domain.RolesMap[domain.RoleId(rec.RoleID)]; ok {
		return def.Label, def.User, def.Initials
	}
	return rec.Nama, rec.Nama, inisialNama(rec.Nama)
}

func inisialNama(nama string) string {
	words := strings.Fields(nama)
	if len(words) == 0 {
		return "?"
	}
	if len(words) == 1 {
		r := []rune(words[0])
		if len(r) >= 2 {
			return strings.ToUpper(string(r[:2]))
		}
		return strings.ToUpper(words[0])
	}
	return strings.ToUpper(string([]rune(words[0])[0])) + strings.ToUpper(string([]rune(words[1])[0]))
}

var polaRoleID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validasiPermissions memeriksa daftar permission terhadap katalog,
// menolak nama tak dikenal (422), lalu mengembalikan daftar bersih
// (dedupe + terurut).
func validasiPermissions(perms []string) ([]string, error) {
	if len(perms) > len(katalogPermission) {
		return nil, invalid("Jumlah permission melebihi katalog.", "permissions")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, p := range perms {
		if !permissionDikenal(p) {
			return nil, invalid("Permission tidak dikenal: "+p, "permissions")
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

func validasiNamaRole(nama string) (string, error) {
	nama = strings.TrimSpace(nama)
	if nama == "" {
		return "", invalid("Nama role wajib diisi.", "nama")
	}
	if utf8.RuneCountInString(nama) > 100 {
		return "", invalid("Nama role maksimal 100 karakter.", "nama")
	}
	return nama, nil
}

// basisRoles menafsirkan isi state store di dalam transaksi perubahan:
// kosong berarti registri belum pernah ditulis, jadi basisnya seed.
func basisRoles(raw json.RawMessage) ([]RoleRecord, error) {
	if len(raw) == 0 {
		return seedRoleRegistry(), nil
	}
	var list []RoleRecord
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return seedRoleRegistry(), nil
	}
	return list, nil
}

func (s *Server) registerRoles(register func(string, string, endpoint)) {
	// GET /roles mempertahankan bentuk lama {roles, permissions} dari
	// seed/roles.json, kini dibangun dari registri aktif. Field lama
	// (label, user, initials, focus) tetap ada; yang baru ditambah
	// builtin dan permissions pada tiap role.
	register("GET /api/v1/roles", "", func(r *http.Request) (any, error) {
		reg, err := s.roleRegistry(r.Context())
		if err != nil {
			return nil, err
		}
		roles := map[string]any{}
		for _, rec := range reg {
			label, user, initials := displayRole(rec)
			entry := map[string]any{
				"label":       label,
				"user":        user,
				"initials":    initials,
				"focus":       "",
				"builtin":     rec.Builtin,
				"permissions": rec.Permissions,
			}
			if def, ok := domain.RolesMap[domain.RoleId(rec.RoleID)]; ok {
				entry["focus"] = def.Focus
			}
			roles[rec.RoleID] = entry
		}
		perms := map[string]any{}
		for _, p := range katalogPermission {
			holders := []string{}
			for _, rec := range reg {
				for _, held := range rec.Permissions {
					if held == p.Name {
						holders = append(holders, rec.RoleID)
						break
					}
				}
			}
			perms[p.Name] = map[string]any{"label": p.Label, "roles": holders}
		}
		return map[string]any{"roles": roles, "permissions": perms}, nil
	})
	register("GET /api/v1/permissions", "", func(r *http.Request) (any, error) {
		return katalogPermission, nil
	})
	register("POST /api/v1/roles", "role.manage", func(r *http.Request) (any, error) {
		if err := s.dbRequired(); err != nil {
			return nil, err
		}
		var b struct {
			RoleID      string   `json:"role_id"`
			Nama        string   `json:"nama"`
			Permissions []string `json:"permissions"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		b.RoleID = strings.TrimSpace(b.RoleID)
		if len(b.RoleID) < 2 || len(b.RoleID) > 32 || !polaRoleID.MatchString(b.RoleID) {
			return nil, invalid("Role id harus 2-32 karakter huruf kecil, angka, atau tanda hubung.", "role_id")
		}
		nama, e := validasiNamaRole(b.Nama)
		if e != nil {
			return nil, e
		}
		perms, e := validasiPermissions(b.Permissions)
		if e != nil {
			return nil, e
		}
		a, _ := s.authenticate(r)
		created := RoleRecord{RoleID: b.RoleID, Nama: nama, Builtin: false, Permissions: perms}
		result, e := s.change(r.Context(), "role", "current", func(tx *store.Tx, raw json.RawMessage) (store.Change, error) {
			list, err := basisRoles(raw)
			if err != nil {
				return store.Change{}, err
			}
			if _, exists := cariRole(list, b.RoleID); exists {
				return store.Change{}, invalid("Role dengan id ini sudah ada.", "role_id")
			}
			list = append(list, created)
			return store.Change{Value: list, Log: auditEntry(a, "role", "Role baru dibuat", b.RoleID+" · "+nama, "", "neutral"), Event: "role.created"}, nil
		})
		if e != nil {
			return nil, e
		}
		out := result.(map[string]any)
		out["result"] = created
		return out, nil
	})
	register("PUT /api/v1/roles/{role_id}", "role.manage", func(r *http.Request) (any, error) {
		if err := s.dbRequired(); err != nil {
			return nil, err
		}
		id := r.PathValue("role_id")
		var b struct {
			Nama        *string   `json:"nama"`
			Permissions *[]string `json:"permissions"`
		}
		if e := decode(r, &b); e != nil {
			return nil, e
		}
		var namaBaru string
		if b.Nama != nil {
			n, e := validasiNamaRole(*b.Nama)
			if e != nil {
				return nil, e
			}
			namaBaru = n
		}
		var permsBaru []string
		if b.Permissions != nil {
			p, e := validasiPermissions(*b.Permissions)
			if e != nil {
				return nil, e
			}
			permsBaru = p
		}
		a, _ := s.authenticate(r)
		var updated RoleRecord
		result, e := s.change(r.Context(), "role", "current", func(tx *store.Tx, raw json.RawMessage) (store.Change, error) {
			list, err := basisRoles(raw)
			if err != nil {
				return store.Change{}, err
			}
			idx := -1
			for i, rec := range list {
				if rec.RoleID == id {
					idx = i
					break
				}
			}
			if idx < 0 {
				return store.Change{}, missing()
			}
			rec := list[idx]
			if b.Nama != nil {
				rec.Nama = namaBaru
			}
			if b.Permissions != nil {
				// Pengaman: superadmin tidak boleh kehilangan role.manage,
				// kalau tidak, tidak ada lagi yang bisa mengelola role.
				if id == "superadmin" {
					punya := false
					for _, p := range permsBaru {
						if p == "role.manage" {
							punya = true
						}
					}
					if !punya {
						return store.Change{}, invalid("Superadmin harus tetap memegang role.manage.", "permissions")
					}
				}
				rec.Permissions = permsBaru
			}
			list[idx] = rec
			updated = rec
			return store.Change{Value: list, Log: auditEntry(a, "role", "Role diubah", id+" · "+rec.Nama, "", "neutral"), Event: "role.updated"}, nil
		})
		if e != nil {
			return nil, e
		}
		out := result.(map[string]any)
		out["result"] = updated
		return out, nil
	})
	register("DELETE /api/v1/roles/{role_id}", "role.manage", func(r *http.Request) (any, error) {
		if err := s.dbRequired(); err != nil {
			return nil, err
		}
		id := r.PathValue("role_id")
		if e := decodeOptional(r, &struct{}{}); e != nil {
			return nil, e
		}
		a, _ := s.authenticate(r)
		var deleted RoleRecord
		result, e := s.change(r.Context(), "role", "current", func(tx *store.Tx, raw json.RawMessage) (store.Change, error) {
			list, err := basisRoles(raw)
			if err != nil {
				return store.Change{}, err
			}
			idx := -1
			for i, rec := range list {
				if rec.RoleID == id {
					idx = i
					break
				}
			}
			if idx < 0 {
				return store.Change{}, missing()
			}
			if list[idx].Builtin {
				return store.Change{}, invalid("Role bawaan tidak dapat dihapus.", "role_id")
			}
			deleted = list[idx]
			list = append(list[:idx], list[idx+1:]...)
			return store.Change{Value: list, Log: auditEntry(a, "role", "Role dihapus", id+" · "+deleted.Nama, "", "warn"), Event: "role.deleted"}, nil
		})
		if e != nil {
			return nil, e
		}
		out := result.(map[string]any)
		out["result"] = deleted
		return out, nil
	})
}
