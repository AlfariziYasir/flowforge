# Implementation Plan — Test Infrastructure Hardening

**Status:** Siap dieksekusi · **Ukuran:** ~40 baris di 3 file · **Dibuat:** 2026-08-04

Menutup satu celah: integration test **melewati dirinya sendiri secara diam-diam**, sehingga suite lulus hijau tanpa pernah menyentuh database.

---

## 1. Konteks

Sepanjang Phase 1–3, **tidak ada satu pun test yang pernah menyentuh Postgres atau Redis sungguhan.** Bukan karena testnya tidak ada — tapi karena test yang ada melewati dirinya sendiri:

```
=== RUN   TestBaseRepository_Integration
    skipping live database test: dial tcp 127.0.0.1:5432: connect: connection refused
--- SKIP
ok  	flowforge/internal/platform/postgres	0.004s      ← tetap hijau
```

Bahayanya bukan "belum diuji" — tapi **membuat kita percaya ada tes padahal tidak ada.** `make ci` tidak akan pernah gagal karena database mati.

Ini penting karena kelas bug yang berulang ditemukan di review Phase 2–3 persis kelas yang hanya database yang bisa menangkap:

| Bug | Ditemukan lewat |
|---|---|
| Nama status tidak cocok CHECK constraint (D-4) | Review manual |
| Urutan FK pada `ReplaceGraph` | Review manual |
| `db` tag vs kolom asli | Review manual |
| `uuid.UUID` vs `string` beda antara `auth` dan `workflow` | Review manual |

Semuanya lolos unit test. Semuanya akan tertangkap oleh satu kali koneksi database sungguhan.

> [!NOTE]
> Rencana ini **tidak** mewajibkan integration test dijalankan tiap fase. Pengguna sudah memutuskan integration testing menyeluruh dilakukan di fase akhir, dan keputusan itu dihormati. Yang diperbaiki di sini hanya satu hal: **kalau Anda bermaksud menjalankannya, kegagalannya harus terlihat.**

---

## 2. Yang Sudah Ada — Jangan Dibuat Ulang

`docker-compose.yml` (dari Phase 1) sudah lengkap dan benar:

| Service | Fungsi |
|---|---|
| `postgres` | PostgreSQL 17 + healthcheck `pg_isready` |
| `redis` | Redis 7 + healthcheck `redis-cli ping` |
| `migrate` | `migrate/migrate:v4.18.2`, jalan setelah postgres sehat |
| `seed` | `psql -f seed.sql`, jalan setelah migrate selesai |
| `api`, `worker` | Aplikasi, dengan `depends_on` yang benar |

**Tidak ada yang perlu ditambahkan di sini.** Yang hilang cuma jembatan antara compose dan test suite.

---

## 3. Tiga Celah

| # | Lokasi | Masalah |
|---|---|---|
| **T-1** | `internal/platform/postgres/unit_of_work_test.go:29-42` | `getTestPool` memanggil `t.Skipf` saat koneksi gagal — tidak bisa dibedakan antara "sengaja tidak menjalankan integration" dan "bermaksud menjalankan tapi gagal" |
| **T-2** | `internal/platform/postgres/repository_test.go:161` | `t.Skip("seed user not found...")` — data seed hilang juga lolos diam-diam, padahal compose stack menyediakannya |
| **T-3** | `Makefile` | Tidak ada target untuk menjalankan integration test atau menyalakan stack |

---

## 4. Perubahan

### [MODIFY] `internal/platform/postgres/unit_of_work_test.go`

Ganti `getTestPool` ([:29-42](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/unit_of_work_test.go#L29-L42)) dengan gerbang eksplisit:

```go
// getTestPool membuka koneksi ke database sungguhan untuk integration test.
//
// Perilakunya sengaja dibuat dua kutub:
//   - FLOWFORGE_INTEGRATION tidak diset → skip (loop unit test lokal tetap cepat)
//   - FLOWFORGE_INTEGRATION=1 tapi koneksi gagal → FATAL
//
// Kutub kedua itu intinya: kalau Anda meminta integration test, kegagalan
// koneksi adalah kegagalan test — bukan alasan untuk melewatinya diam-diam.
func getTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	if os.Getenv("FLOWFORGE_INTEGRATION") == "" {
		t.Skip("integration test dilewati — set FLOWFORGE_INTEGRATION=1 untuk menjalankan (butuh `make up`)")
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/flowforge?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := postgres.NewPool(ctx, dbURL)
	if err != nil {
		t.Fatalf("FLOWFORGE_INTEGRATION=1 tetapi tidak bisa terhubung ke %s: %v",
			logger.RedactURL(dbURL), err)
	}
	return pool
}
```

Dua catatan untuk eksekutor:

- **Pakai `logger.RedactURL`** dari `flowforge/internal/platform/logger` — sudah ada dan sudah diuji, jangan tulis ulang. Ini mencegah password bocor ke output CI.
- Timeout dinaikkan 2s → 5s. Container yang baru bangun kadang butuh lebih lama, dan timeout terlalu pendek akan terlihat seperti bug padahal bukan.

### [MODIFY] `internal/platform/postgres/repository_test.go`

Pada [:161](file:///home/mohyasiralfarizi/Golang/flowforge/internal/platform/postgres/repository_test.go#L161), ganti skip dengan kegagalan:

```go
// Sebelum: t.Skip("seed user not found in database, skipping live row assertion")
// Sesudah:
require.NoError(t, err, "seed user tidak ditemukan — jalankan `make up` yang memuat migrations/seed.sql")
```

Alasannya: kita sudah berada di dalam integration test (T-1 sudah menjamin itu). Data seed **disediakan oleh compose stack**, jadi ketidakhadirannya berarti stack-nya tidak lengkap — itu kegagalan, bukan kondisi normal.

### [MODIFY] `Makefile`

Tambahkan tiga target dan daftarkan di `.PHONY`:

```make
# Nyalakan dependensi untuk integration test (tanpa api/worker)
up:
	docker compose up -d postgres redis migrate seed

down:
	docker compose down

# Integration test — butuh `make up` lebih dulu
test-integration:
	FLOWFORGE_INTEGRATION=1 go test ./internal/... -race -count=1
```

> [!IMPORTANT]
> **`make ci` sengaja TIDAK diubah.** Loop pengembangan harian harus tetap cepat dan tidak butuh Docker. Integration test dijalankan terpisah lewat `make test-integration`, sesuai keputusan menunda integration testing menyeluruh ke fase akhir.

---

## 5. Verifikasi

Eksekutor wajib membuktikan **keduanya**, bukan hanya yang hijau:

```bash
# 1. Tanpa Docker → skip yang jelas, ci tetap hijau
make ci
# harapan: lulus. Output test memuat "integration test dilewati".

# 2. Meminta integration tanpa stack → HARUS GAGAL
make test-integration
# harapan: FAIL dengan "FLOWFORGE_INTEGRATION=1 tetapi tidak bisa terhubung ke
# postgres://postgres:*****@localhost:5432/..." — perhatikan password ter-redact.

# 3. Dengan stack → benar-benar menyentuh database
make up
sleep 10                      # tunggu healthcheck + migrate + seed
make test-integration
# harapan: LULUS, dan TestBaseRepository_Integration benar-benar RUN, bukan SKIP.
make down
```

Langkah 2 adalah inti rencana ini. **Kalau langkah 2 lulus, perbaikannya gagal** — artinya skip senyapnya masih ada.

Cara cepat memastikan test benar-benar jalan:

```bash
make test-integration 2>&1 | grep -E "^(=== RUN|--- (PASS|SKIP|FAIL)).*Integration"
# harus muncul "--- PASS", bukan "--- SKIP"
```

---

## 6. Posisi dalam Antrean Eksekusi

Tiga pekerjaan menunggu; ini urutan yang saya sarankan:

| Urutan | Pekerjaan | Rencana | Ukuran |
|---|---|---|---|
| 1 | **V-1** — kembalikan `ContextWithTx`/`TxFromContext` ke `pgx.Tx` | [phase_3_close_out_review.md](phase_3_close_out_review.md) §Phase AD | ~20 menit |
| 2 | **Rencana ini** — test infrastructure | dokumen ini | ~30 menit |
| 3 | **Phase 4** — Workflow Engine Core | [phase_4_workflow_engine_core.md](phase_4_workflow_engine_core.md) v2.2 | Besar |

Alasan urutannya:

- **V-1 sebelum Phase 4**, karena Phase 4 dibangun di atas `internal/engine` dan tidak boleh mewarisi tanda tangan `TxFromContext` yang sudah melebar artinya.
- **Rencana ini sebelum Phase 4**, karena Phase 4 membawa migration `000002` — migration pertama sejak `000001`, dan yang pertama akan diuji orang. Lebih baik jembatannya sudah ada saat itu terjadi.

> [!NOTE]
> Cabang ini **masih belum di-commit sama sekali**. Seluruh Phase 2 dan Phase 3 ada di working tree. Sebelum Phase 4 menambah dependensi `go.mod` dan migration baru di atasnya, sebaiknya semuanya dijadikan commit yang bisa direview lebih dulu.

---

## 7. Yang Sengaja Tidak Dikerjakan

- **Menjalankan integration test di CI otomatis** — di luar cakupan; keputusan pengguna adalah menjalankannya di fase akhir. Rencana ini hanya membuat kegagalannya terlihat saat dijalankan.
- **Build tag `//go:build integration`** — lebih idiomatis Go, tapi menuntut pemecahan file karena `repository_test.go` memuat unit **dan** integration test dalam satu berkas. Gerbang env var memberi manfaat yang sama tanpa refactor.
- **Integration test untuk Redis** — `internal/platform/redis/redis_test.go` sudah ada; kalau nanti ia juga punya skip senyap, terapkan pola yang sama.
