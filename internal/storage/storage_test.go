package storage

import (
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return Open(filepath.Join(t.TempDir(), "test.db"))
}

func TestCreateListingIfNotExistsDeduplicates(t *testing.T) {
	s := newStore(t)
	l := func() *Listing {
		return &Listing{SearchID: 1, Source: "olx", ExternalID: "abc", Title: "t", URL: "u", PostedAt: time.Now()}
	}

	created, err := s.CreateListingIfNotExists(l())
	if err != nil || !created {
		t.Fatalf("first insert: created=%v err=%v", created, err)
	}
	created, err = s.CreateListingIfNotExists(l())
	if err != nil || created {
		t.Fatalf("second insert: created=%v err=%v, want created=false", created, err)
	}
}

func TestCreateListingBackfillsSearchID(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateListingIfNotExists(&Listing{Source: "olx", ExternalID: "legacy", Title: "t"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := s.CreateListingIfNotExists(&Listing{SearchID: 7, Source: "olx", ExternalID: "legacy", Title: "t"}); err != nil {
		t.Fatalf("reinsert: %v", err)
	}

	var got Listing
	if err := s.db.Where("external_id = ?", "legacy").First(&got).Error; err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.SearchID != 7 {
		t.Fatalf("SearchID = %d, want 7", got.SearchID)
	}
}

// Clearing must actually remove the listings of that search, so the next run
// reports them as new again.
func TestClearListingsForSearchID(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSearchIfNotExists("Dr. Martens", "https://olx.ua/q-dr-martens/", "olx", true); err != nil {
		t.Fatalf("create search: %v", err)
	}
	search, err := s.GetSearchByID(1)
	if err != nil {
		t.Fatalf("get search: %v", err)
	}

	for _, id := range []string{"a", "b"} {
		if _, err := s.CreateListingIfNotExists(&Listing{SearchID: search.ID, Source: "olx", ExternalID: id}); err != nil {
			t.Fatalf("insert listing: %v", err)
		}
	}
	if _, err := s.CreateListingIfNotExists(&Listing{SearchID: 999, Source: "olx", ExternalID: "other"}); err != nil {
		t.Fatalf("insert listing: %v", err)
	}

	if err := s.ClearListingsForSearchID(search.ID); err != nil {
		t.Fatalf("clear: %v", err)
	}

	var count int64
	s.db.Model(&Listing{}).Where("search_id = ?", search.ID).Count(&count)
	if count != 0 {
		t.Fatalf("listings left for cleared search: %d", count)
	}
	s.db.Model(&Listing{}).Count(&count)
	if count != 1 {
		t.Fatalf("total listings = %d, want 1 (other search untouched)", count)
	}

	// The listings can be collected again after clearing.
	created, err := s.CreateListingIfNotExists(&Listing{SearchID: search.ID, Source: "olx", ExternalID: "a"})
	if err != nil || !created {
		t.Fatalf("re-insert after clear: created=%v err=%v", created, err)
	}
}

func TestDeleteSearchRemovesItsListings(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSearchIfNotExists("x", "https://olx.ua/q-x/", "olx", true); err != nil {
		t.Fatalf("create search: %v", err)
	}
	if _, err := s.CreateListingIfNotExists(&Listing{SearchID: 1, Source: "olx", ExternalID: "a"}); err != nil {
		t.Fatalf("insert listing: %v", err)
	}
	if err := s.DeleteSearchByID(1); err != nil {
		t.Fatalf("delete: %v", err)
	}

	var count int64
	s.db.Model(&Listing{}).Count(&count)
	if count != 0 {
		t.Fatalf("orphan listings left: %d", count)
	}
	if _, err := s.GetSearchByID(1); err != ErrSearchNotFound {
		t.Fatalf("GetSearchByID error = %v, want ErrSearchNotFound", err)
	}
}

func TestSearchLifecycle(t *testing.T) {
	s := newStore(t)
	created, err := s.CreateSearchIfNotExists("x", "https://olx.ua/q-x/", "olx", true)
	if err != nil || !created {
		t.Fatalf("create: created=%v err=%v", created, err)
	}
	if created, _ := s.CreateSearchIfNotExists("x again", "https://olx.ua/q-x/", "olx", true); created {
		t.Fatal("duplicate URL was inserted")
	}

	if err := s.DeactivateSearch(1); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	active, err := s.ListActiveSearches("olx")
	if err != nil || len(active) != 0 {
		t.Fatalf("active = %v, err = %v, want none", active, err)
	}

	if err := s.ActivateSearch(1); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := s.UpdateSearch(1, "renamed", "https://olx.ua/q-y/"); err != nil {
		t.Fatalf("update: %v", err)
	}
	active, err = s.ListActiveSearches("olx")
	if err != nil || len(active) != 1 || active[0].Name != "renamed" || active[0].URL != "https://olx.ua/q-y/" {
		t.Fatalf("active = %+v, err = %v", active, err)
	}
}

// Записи, створені до появи SearchID, лежать із NULL і не показуються на
// жодній сторінці пошуку — їх має бути видно як «осиротілі» й можна прибрати.
func TestOrphanListings(t *testing.T) {
	s := newStore(t)
	if _, err := s.CreateSearchIfNotExists("x", "https://olx.ua/q-x/", "olx", true); err != nil {
		t.Fatalf("create search: %v", err)
	}
	// прив'язаний запис
	if _, err := s.CreateListingIfNotExists(&Listing{SearchID: 1, Source: "olx", ExternalID: "bound"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// запис із SearchID=0 і запис із NULL (як після AutoMigrate старої бази)
	if _, err := s.CreateListingIfNotExists(&Listing{Source: "olx", ExternalID: "zero"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.db.Exec("insert into listings (source, external_id, search_id) values (?, ?, null)", "olx", "legacy").Error; err != nil {
		t.Fatalf("insert legacy: %v", err)
	}

	count, err := s.CountOrphanListings()
	if err != nil || count != 2 {
		t.Fatalf("CountOrphanListings = %d, err = %v, want 2", count, err)
	}

	deleted, err := s.DeleteOrphanListings()
	if err != nil || deleted != 2 {
		t.Fatalf("DeleteOrphanListings = %d, err = %v, want 2", deleted, err)
	}

	count, _ = s.CountOrphanListings()
	if count != 0 {
		t.Fatalf("після видалення лишилось %d осиротілих", count)
	}

	var total int64
	s.db.Model(&Listing{}).Count(&total)
	if total != 1 {
		t.Fatalf("всього записів %d, очікувався 1 (прив'язаний не чіпаємо)", total)
	}
}
