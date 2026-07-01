package gworkspace

import (
	"context"
	"errors"

	adktool "google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"

	"go.naturallyfunny.dev/gworkspace"
)

type contactView struct {
	ResourceName string   `json:"resource_name"`
	Name         string   `json:"name"`
	Emails       []string `json:"emails,omitempty"`
	Phones       []string `json:"phones,omitempty"`
}

func toContactView(c gworkspace.Contact) contactView {
	return contactView{
		ResourceName: c.ResourceName,
		Name:         c.Name,
		Emails:       c.Emails,
		Phones:       c.Phones,
	}
}

// ContactClient is the surface required by the Contacts toolset.
// *gworkspace.Contacts satisfies this interface implicitly.
type ContactClient interface {
	GetContacts(ctx context.Context, ownerID string, q gworkspace.ContactQuery) ([]gworkspace.Contact, error)
	AddContact(ctx context.Context, ownerID string, in gworkspace.ContactInput) (gworkspace.Contact, error)
}

// ContactTools returns the Google Contacts toolset bound to c. It errors if
// c is nil.
func ContactTools(c ContactClient) ([]adktool.Tool, error) {
	if c == nil {
		return nil, errors.New("adk: ContactTools: client must not be nil")
	}
	type contactsOutput struct {
		Contacts []contactView `json:"contacts"`
	}
	type getContactsArgs struct {
		Limit int `json:"limit,omitempty"`
	}
	getContacts, err := functiontool.New(
		functiontool.Config{
			Name: "get_contacts",
			Description: `WHEN TO USE:
- Perlu mencari email atau nomor telepon seseorang di kontak manusia
- Manusia menyebut nama orang tapi tidak memberikan email secara eksplisit
- Sebelum add_event dengan tamu, untuk memastikan email tamu yang benar

HOW TO USE:
- limit: maksimum kontak yang dikembalikan. Default ke semua kontak (Google limit berlaku).
  Set limit kecil jika hanya butuh beberapa, untuk menghindari response besar.

WHAT I GET BACK:
- Daftar kontak dengan resource_name, name, emails, phones.
  Gunakan email dari sini untuk mengisi tamu event atau penerima email.`,
		},
		func(toolCtx adktool.Context, in getContactsArgs) (contactsOutput, error) {
			q := gworkspace.ContactQuery{
				Limit: in.Limit,
			}
			contacts, err := c.GetContacts(toolCtx, toolCtx.UserID(), q)
			if err != nil {
				if errors.Is(err, gworkspace.ErrNotConnected) {
					return contactsOutput{}, errors.New("manusia belum menghubungkan akun Google mereka — arahkan ke flow OAuth sebelum mengakses Contacts")
				}
				return contactsOutput{}, err
			}
			views := make([]contactView, len(contacts))
			for i, ct := range contacts {
				views[i] = toContactView(ct)
			}
			return contactsOutput{Contacts: views}, nil
		},
	)
	if err != nil {
		return nil, err
	}
	type addContactArgs struct {
		Name   string   `json:"name"`
		Emails []string `json:"emails,omitempty"`
		Phones []string `json:"phones,omitempty"`
	}
	addContact, err := functiontool.New(
		functiontool.Config{
			Name: "add_contact",
			Description: `WHEN TO USE:
- Manusia meminta menyimpan kontak baru
- Konfirmasi nama, email, dan telepon sebelum menyimpan

HOW TO USE:
- name: nama lengkap orang (wajib)
- emails: daftar alamat email (opsional tapi direkomendasikan)
- phones: daftar nomor telepon (opsional)

WHAT I GET BACK:
- Kontak yang berhasil dibuat dengan resource_name-nya.`,
		},
		func(toolCtx adktool.Context, in addContactArgs) (contactView, error) {
			if in.Name == "" {
				return contactView{}, errors.New("name is required")
			}

			input := gworkspace.ContactInput{
				Name:   in.Name,
				Emails: in.Emails,
				Phones: in.Phones,
			}

			created, err := c.AddContact(toolCtx, toolCtx.UserID(), input)
			if err != nil {
				if errors.Is(err, gworkspace.ErrNotConnected) {
					return contactView{}, errors.New("manusia belum menghubungkan akun Google mereka — arahkan ke flow OAuth sebelum mengakses Contacts")
				}
				return contactView{}, err
			}
			return toContactView(created), nil
		},
	)
	if err != nil {
		return nil, err
	}
	return []adktool.Tool{
		getContacts,
		addContact,
	}, nil
}
