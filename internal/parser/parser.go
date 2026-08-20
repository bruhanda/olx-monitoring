package parser

import "time"

// Item is a normalized output from any concrete parser.
type Item struct {
	ExternalID string
	Title      string
	URL        string
	Price      string
	Location   string
	PostedAt   time.Time
}

// Parser defines a source-specific parser (e.g., OLX).
type Parser interface {
	Source() string
	Label() string // Returns the search name/label
	URL() string   // Returns the search URL
	Fetch() ([]Item, error)
}
