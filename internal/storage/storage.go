package storage

import (
	"errors"
	"log"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Listing represents a parsed listing normalized for deduplication and notification.
type Listing struct {
	ID        uint `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	SearchID   uint      `gorm:"index"`                                     // owning SavedSearch, 0 for legacy rows
	Source     string    `gorm:"index:idx_source_external,unique;size:32"`  // e.g., "olx"
	ExternalID string    `gorm:"index:idx_source_external,unique;size:128"` // source-specific id or URL hash
	Title      string    `gorm:"size:512"`
	URL        string    `gorm:"size:1024"`
	Price      string    `gorm:"size:128"`
	Location   string    `gorm:"size:256"`
	PostedAt   time.Time `gorm:"index"`
}

// SavedSearch represents a user-configured search to monitor.
type SavedSearch struct {
	ID        uint `gorm:"primaryKey"`
	CreatedAt time.Time
	UpdatedAt time.Time

	Name   string `gorm:"size:128;not null"`
	URL    string `gorm:"size:1024;not null;uniqueIndex"`
	Active bool   `gorm:"not null;default:true"`
	Source string `gorm:"size:32;not null;default:'olx'"`
}

type Store struct {
	db *gorm.DB
}

func Open(databasePath string) *Store {
	db, err := gorm.Open(sqlite.Open(databasePath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	if err := db.AutoMigrate(&Listing{}, &SavedSearch{}); err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}
	return &Store{db: db}
}

// CreateListingIfNotExists inserts listing if unique key not present; returns true if created.
func (s *Store) CreateListingIfNotExists(listing *Listing) (bool, error) {
	var existing Listing
	tx := s.db.Where("source = ? AND external_id = ?", listing.Source, listing.ExternalID).First(&existing)
	if tx.Error == nil {
		// Backfill the owning search for rows stored before SearchID existed.
		if existing.SearchID == 0 && listing.SearchID != 0 {
			if err := s.db.Model(&Listing{}).Where("id = ?", existing.ID).Update("search_id", listing.SearchID).Error; err != nil {
				return false, err
			}
		}
		return false, nil
	}
	if !errors.Is(tx.Error, gorm.ErrRecordNotFound) {
		return false, tx.Error
	}
	if err := s.db.Create(listing).Error; err != nil {
		return false, err
	}
	return true, nil
}

// CreateSearchIfNotExists inserts a saved search if URL not present; returns true if created.
func (s *Store) CreateSearchIfNotExists(name, url, source string, active bool) (bool, error) {
	var existing SavedSearch
	tx := s.db.Where("url = ?", url).First(&existing)
	if tx.Error == nil {
		return false, nil
	}
	if !errors.Is(tx.Error, gorm.ErrRecordNotFound) {
		return false, tx.Error
	}
	rec := SavedSearch{Name: name, URL: url, Source: source, Active: active}
	if err := s.db.Create(&rec).Error; err != nil {
		return false, err
	}
	return true, nil
}

// ListActiveSearches returns active searches for a source.
func (s *Store) ListActiveSearches(source string) ([]SavedSearch, error) {
	var list []SavedSearch
	if err := s.db.Where("source = ? AND active = ?", source, true).Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ListAllSearches returns all searches (active and inactive) for a source.
func (s *Store) ListAllSearches(source string) ([]SavedSearch, error) {
	var list []SavedSearch
	if err := s.db.Where("source = ?", source).Order("id ASC").Find(&list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

// ErrSearchNotFound is returned when a saved search does not exist.
var ErrSearchNotFound = errors.New("search not found")

// GetSearchByID returns a single saved search.
func (s *Store) GetSearchByID(id uint) (SavedSearch, error) {
	var rec SavedSearch
	tx := s.db.First(&rec, id)
	if errors.Is(tx.Error, gorm.ErrRecordNotFound) {
		return rec, ErrSearchNotFound
	}
	return rec, tx.Error
}

// DeleteSearchByID deletes a saved search together with its listings.
func (s *Store) DeleteSearchByID(id uint) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("search_id = ?", id).Delete(&Listing{}).Error; err != nil {
			return err
		}
		return tx.Delete(&SavedSearch{}, id).Error
	})
}

// ClearListingsForSearchID deletes all listings collected for a saved search,
// so the next run reports its results as new again.
func (s *Store) ClearListingsForSearchID(id uint) error {
	return s.db.Where("search_id = ?", id).Delete(&Listing{}).Error
}

// DeactivateSearch sets a search as inactive.
func (s *Store) DeactivateSearch(id uint) error {
	return s.db.Model(&SavedSearch{}).Where("id = ?", id).Update("active", false).Error
}

// ActivateSearch sets a search as active.
func (s *Store) ActivateSearch(id uint) error {
	return s.db.Model(&SavedSearch{}).Where("id = ?", id).Update("active", true).Error
}

// UpdateSearch updates a saved search with new name and URL.
func (s *Store) UpdateSearch(id uint, name, url string) error {
	return s.db.Model(&SavedSearch{}).Where("id = ?", id).Updates(map[string]interface{}{
		"name": name,
		"url":  url,
	}).Error
}
