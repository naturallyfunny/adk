# ADK Toolset — `gworkspace`

Satu toolset ADK yang mengekspos Google Workspace (Calendar, Gmail, Contacts)
ke LLM agent sebagai function tools. Hidup di repo
`go.naturallyfunny.dev/adk` (direktori `/Users/ardian/dev/adk/`), dalam satu
subdirektori:

```
adk/gworkspace/  → go.naturallyfunny.dev/adk/gworkspace
```

Awalnya diproposalkan sebagai tiga package terpisah (`gcal`, `gmail`,
`contact`), tapi digabung jadi satu package karena ketiganya adalah adapter
untuk domain yang sama (`go.naturallyfunny.dev/gworkspace`) dan sering dipakai
berpasangan oleh consumer yang sama (mis. rafal pakai Calendar + Contacts).

## Konteks & posisi dalam arsitektur

```
go.naturallyfunny.dev/gworkspace     ← domain client (sudah ada)
  └── calendar.Service               ← disatukan oleh CalendarTools
  └── gmail.Service                  ← disatukan oleh GmailTools
  └── contact.Service                ← disatukan oleh ContactTools

go.naturallyfunny.dev/adk/gworkspace ← toolset ini (repo: adk)

go.avagenc.com/rafal                 ← Calendar agent, pakai CalendarTools + ContactTools
go.avagenc.com/yori                  ← Gmail agent, pakai GmailTools + ContactTools
github.com/avagenc/chat              ← wire rafal + yori sebagai sub-agent
```

Blueprint pola dasar: `adk/tuya/toolset.go` — struktur tiap file (`Client`
interface → args → output types → `Tools()` → converter) ikut pola itu
persis, hanya di-multiply per produk dalam satu package. Beda dari tuya:
tidak ada `forAgent` bersama (lihat prinsip #4).

## Prinsip yang mengikat (non-negotiable)

1. **Nol coupling ke avagenc.** Package tidak boleh tahu tentang `rafal`,
   `yori`, `ava`, atau `chat`. Setiap konsumen boleh pakai toolset ini secara
   independen, dan boleh hanya pakai sebagian (mis. rafal tidak butuh
   `GmailTools`).
2. **Consumer-defined `*Client` interface per produk.** `CalendarClient`,
   `GmailClient`, `ContactClient` masing-masing narrow dan independen — bukan
   satu mega-interface. Ini membuat tiap toolset bisa di-test tanpa
   gworkspace sama sekali, dan `*calendar.Service` / `*gmail.Service` /
   `*contact.Service` memenuhi interface masing-masing secara implisit tanpa
   perlu mengimplementasikan method produk lain.
3. **`userID` via `toolCtx.UserID()`.** Toolset tidak menerima `ownerID`
   sebagai argumen tool — ia diambil dari `adktool.Context` (ADK session
   context). Sama persis dengan tuya.
4. **Tiap tool urus terjemahan errornya sendiri, inline.** Tidak ada fungsi
   `forAgent` bersama. Error sentinel gworkspace (`ErrNotConnected`, dst.)
   diperiksa dengan `errors.Is` langsung di titik pemanggilan client, di
   dalam closure tool masing-masing — meski itu berarti duplikasi
   `if errors.Is(...)` antar tool dalam satu produk. Sengaja begini: sebuah
   fungsi switch bersama gampang membengkak jadi mini-framework yang harus
   di-increment tiap ada sentinel baru, padahal belum tentu semua tool
   peduli semua sentinel yang sama.
5. **`XTools(c XClient) ([]adktool.Tool, error)` per produk.** Tiga fungsi
   publik: `CalendarTools`, `GmailTools`, `ContactTools`. Masing-masing
   mengembalikan error jika `c == nil`, sehingga kegagalan wiring terdeteksi
   saat startup bukan saat tool dipanggil pertama kali.

---

## 1. Calendar — `CalendarTools`

### Client interface

```go
// CalendarClient adalah surface sempit yang dibutuhkan toolset Calendar.
// *gworkspace.Calendar memenuhi interface ini secara implisit.
type CalendarClient interface {
    GetEvents(ctx context.Context, ownerID string, q gworkspace.EventQuery) ([]gworkspace.Event, error)
    AddEvent(ctx context.Context, ownerID string, in gworkspace.EventInput) (gworkspace.Event, error)
}
```

Import dari `go.naturallyfunny.dev/gworkspace` (root package — tidak ada
subdirektori). Tipe `EventQuery`, `EventInput`, `Event` ada langsung di
package `gworkspace`.

### Tools

#### `get_events`

```
WHEN TO USE:
- Manusia meminta jadwal, agenda, atau daftar event
- Sebelum membuat event baru, untuk cek konflik waktu
- Untuk mencari event berdasarkan kata kunci, rentang waktu, atau tamu

HOW TO USE:
- query: teks pencarian bebas (nama event, lokasi, dll). Kosongkan jika tidak ada filter teks.
- time_min / time_max: filter waktu, format RFC3339. time_min default ke sekarang jika kosong.
- limit: maksimum event yang dikembalikan. Kosongkan untuk default Google.

WHAT I GET BACK:
- Daftar event dengan id, summary, description, location, start, end, attendees, html_link.
  Gunakan html_link untuk referensi ke event, id untuk operasi selanjutnya.
```

#### `add_event`

```
WHEN TO USE:
- Manusia meminta membuat atau menjadwalkan event / meeting / janji
- Konfirmasi detail (waktu, judul, tamu) sebelum membuat jika belum jelas

HOW TO USE:
- summary: judul event (wajib)
- start / end: waktu mulai dan selesai, format RFC3339 (wajib)
- description: keterangan event (opsional)
- location: lokasi (opsional)
- guests: daftar email tamu (opsional)

WHAT I GET BACK:
- Event yang berhasil dibuat, termasuk html_link untuk dibagikan ke manusia.
```

### Error handling

Di dalam `GetEvents`/`AddEvent`, cek `errors.Is(err, gworkspace.ErrNotConnected)`
inline dan kembalikan pesan yang bisa dimengerti model:

```go
if errors.Is(err, gworkspace.ErrNotConnected) {
    return eventsOutput{}, errors.New("manusia belum menghubungkan akun Google mereka — arahkan ke flow OAuth sebelum mengakses Calendar")
}
return eventsOutput{}, err
```

---

## 2. Gmail — `GmailTools`

### Client interface

```go
// GmailClient adalah surface sempit yang dibutuhkan toolset Gmail.
// *gworkspace.Gmail memenuhi interface ini secara implisit.
type GmailClient interface {
    ReadMessages(ctx context.Context, ownerID string, q gworkspace.MessageQuery) ([]gworkspace.Message, error)
    SendEmail(ctx context.Context, ownerID, to, subject, body string) error
}
```

Import dari `go.naturallyfunny.dev/gworkspace` (root package). Tipe
`MessageQuery` dan `Message` ada langsung di package `gworkspace`.

> **Catatan**: `*gworkspace.Gmail` juga punya method `GetLabels`,
> `GetMessagesByLabel`, `CreateLabel`, `ApplyLabel` — bisa dijadikan tools
> tambahan jika dibutuhkan.

### Tools

#### `read_messages`

```
WHEN TO USE:
- Manusia meminta membaca email, mengecek kotak masuk, atau mencari pesan tertentu
- Gunakan query Gmail (mis. "is:unread", "from:alice@example.com", "subject:invoice")

HOW TO USE:
- query: Gmail search query. Kosongkan untuk pesan terbaru.
- limit: maksimum pesan yang dikembalikan. Default ke beberapa pesan saja — jangan ambil ratusan.

WHAT I GET BACK:
- Daftar pesan dengan id, from, to, subject, snippet, body (plain text), date.
```

#### `send_email`

```
WHEN TO USE:
- Manusia meminta mengirim email
- Konfirmasi penerima, subjek, dan isi sebelum mengirim jika belum eksplisit

HOW TO USE:
- to: alamat email penerima (wajib)
- subject: subjek email (wajib)
- body: isi email plain text (wajib)

WHAT I GET BACK:
- Konfirmasi pengiriman berhasil (ok: true). Error jika gagal.
```

### Error handling

```go
if errors.Is(err, gworkspace.ErrNotConnected) {
    return msgsOutput{}, errors.New("manusia belum menghubungkan akun Gmail mereka — arahkan ke flow OAuth sebelum mengakses Gmail")
}
return msgsOutput{}, err
```

---

## 3. Contacts — `ContactTools`

### Client interface

```go
// ContactClient adalah surface sempit yang dibutuhkan toolset Contacts.
// *gworkspace.Contacts memenuhi interface ini secara implisit.
type ContactClient interface {
    GetContacts(ctx context.Context, ownerID string, q gworkspace.ContactQuery) ([]gworkspace.Contact, error)
    AddContact(ctx context.Context, ownerID string, in gworkspace.ContactInput) (gworkspace.Contact, error)
}
```

Import dari `go.naturallyfunny.dev/gworkspace` (root package). Tipe
`ContactQuery`, `ContactInput`, `Contact` ada langsung di package
`gworkspace`. Tipe konkret yang memenuhi interface: `*gworkspace.Contacts`
(perhatikan: plural).

### Tools

#### `get_contacts`

```
WHEN TO USE:
- Perlu mencari email atau nomor telepon seseorang di kontak manusia
- Manusia menyebut nama orang tapi tidak memberikan email secara eksplisit
- Sebelum add_event dengan tamu, untuk memastikan email tamu yang benar

HOW TO USE:
- limit: maksimum kontak yang dikembalikan. Default ke semua kontak (Google limit berlaku).
  Set limit kecil jika hanya butuh beberapa, untuk menghindari response besar.

WHAT I GET BACK:
- Daftar kontak dengan resource_name, name, emails, phones.
  Gunakan email dari sini untuk mengisi tamu event atau penerima email.
```

#### `add_contact`

```
WHEN TO USE:
- Manusia meminta menyimpan kontak baru
- Konfirmasi nama, email, dan telepon sebelum menyimpan

HOW TO USE:
- name: nama lengkap orang (wajib)
- emails: daftar alamat email (opsional tapi direkomendasikan)
- phones: daftar nomor telepon (opsional)

WHAT I GET BACK:
- Kontak yang berhasil dibuat dengan resource_name-nya.
```

### Error handling

```go
if errors.Is(err, gworkspace.ErrNotConnected) {
    return contactsOutput{}, errors.New("manusia belum menghubungkan akun Google mereka — arahkan ke flow OAuth sebelum mengakses Contacts")
}
return contactsOutput{}, err
```

---

## Struktur file (implemented)

```
adk/gworkspace/
  calendar.go        CalendarClient, CalendarTools(), eventView, converters
  calendar_test.go   calendarStub, TestCalendarToolsNilClient, TestCalendarToolsNames
  gmail.go           GmailClient, GmailTools(), msgView, converters
  gmail_test.go      gmailStub, TestGmailToolsNilClient, TestGmailToolsNames
  contact.go         ContactClient, ContactTools(), contactView, converters
  contact_test.go    contactStub, TestContactToolsNilClient, TestContactToolsNames
```

Package doc comment (`// Package gworkspace exposes...`) hidup di
`calendar.go`, file pertama secara alfabetis dalam package.

Tidak ada `go.mod` terpisah — semuanya hidup di dalam module
`go.naturallyfunny.dev/adk` yang sudah ada:

```
require (
    go.naturallyfunny.dev/gworkspace vX.Y.Z
    ...
)
```

**Catatan penamaan**: package toolset ini bernama `gworkspace`, sama dengan
nama package domain client eksternal yang di-import
(`go.naturallyfunny.dev/gworkspace`). Ini bekerja tanpa alias karena Go tidak
mewajibkan file untuk mereferensikan nama package-nya sendiri, tapi saat
membaca kode ingat: identifier `gworkspace.X` di dalam file-file ini SELALU
merujuk ke domain client eksternal, bukan ke package ini sendiri.

---

## Pola implementasi (ikut tuya, di-multiply per produk)

### Urutan code dalam tiap file produk (`calendar.go` / `gmail.go` / `contact.go`)

1. `package` + import (doc comment package hanya di `calendar.go`)
2. `XClient` interface
3. Tipe args (`getEventsArgs`, dll.)
4. Tipe output (`eventsOutput`, `eventView`, dll.)
5. `XTools(c XClient) ([]adktool.Tool, error)` — satu closure per tool, return slice
6. Converter helpers (`toEventView`, dll.)

### Tiap tool di dalam `XTools()`

```go
getThing, err := functiontool.New(
    functiontool.Config{
        Name:        "tool_name",
        Description: `...WHEN TO USE / HOW TO USE / WHAT I GET BACK...`,
    },
    func(toolCtx adktool.Context, in argsType) (outputType, error) {
        result, err := c.Method(toolCtx, toolCtx.UserID(), ...)
        if err != nil {
            if errors.Is(err, gworkspace.ErrNotConnected) {
                return outputType{}, errors.New("...pesan spesifik tool ini...")
            }
            return outputType{}, err
        }
        return toOutputType(result), nil
    },
)
if err != nil {
    return nil, err
}
```

Terjemahan sentinel ditulis di tempat, bukan didelegasikan ke fungsi
`forAgent` bersama — lihat prinsip #4.

### Test pattern (ikut `tuya/toolset_test.go`, prefixed per produk)

```go
type calendarStub struct{}
// implementasi semua method CalendarClient dengan return zero value

func TestCalendarToolsNilClient(t *testing.T) { ... }
func TestCalendarToolsNames(t *testing.T) { /* verifikasi nama tool */ }
```

Karena terjemahan error sekarang inline di closure tool (bukan fungsi
`forAgent` yang testable secara terpisah), tidak ada unit test khusus untuk
pesan sentinel — hanya nil-client guard dan nama tool yang diverifikasi.
Trade-off ini disengaja mengikuti prinsip #4.

---

## Yang sengaja dibiarkan terbuka

- **Bahasa pesan error**: contoh di atas memakai Indonesia — boleh diganti
  Inggris atau dibuat configurable; yang penting konsisten per produk.
- **Date format di args**: RFC3339 string sudah benar untuk input dari model.
  Alternatif: terima format lenient ("2026-07-01 09:00") dan parse — serahkan
  ke implementor tergantung seberapa forgiving model harus diperlakukan.
- **Tool `get_events` dengan `calendar_id`**: gworkspace mendukung
  non-primary calendar. Untuk versi pertama cukup hardcode primary; tambah
  `calendar_id` arg belakangan jika ada kebutuhan nyata.

---

## Definition of done

- [x] `go build ./gworkspace/...` dan `go vet ./gworkspace/...` bersih.
- [x] `adk/gworkspace/calendar.go`: `CalendarClient`, `CalendarTools`, view
      types, error handling inline.
- [x] `adk/gworkspace/gmail.go`: `GmailClient`, `GmailTools`, view types,
      error handling inline.
- [x] `adk/gworkspace/contact.go`: `ContactClient`, `ContactTools`, view
      types, error handling inline.
- [x] Test per produk: nil-client guard, tool names.
- [x] Nol referensi ke avagenc di seluruh tree package.
- [x] `*gworkspace.Calendar`, `*gworkspace.Gmail`, `*gworkspace.Contacts`
      masing-masing memenuhi interface `XClient` toolset-nya — diverifikasi
      dengan compile-time assertion di test file masing-masing.
