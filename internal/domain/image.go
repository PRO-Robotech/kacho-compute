package domain

import "time"

// Image — каталожный образ (read-only, seed data).
type Image struct {
	UID               string
	FolderID          string
	Name              string
	Labels            map[string]string
	CreationTimestamp time.Time
	ResourceVersion   int64
	Generation        int64

	// Spec
	DisplayName string
	Description string
	Family      string
	ZoneID      string
	Size        string

	// Status — всегда READY
	State ImageState
}
