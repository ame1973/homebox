package store

import (
	"context"
	"database/sql"
)

// Media contains retained source claims and their independent membership
// revision. It never exposes a storage key or assumes a legacy file is safe.
type Media struct {
	ID           string  `json:"id"`
	Revision     int64   `json:"revision"`
	SHA256       string  `json:"sha256"`
	MIMEType     string  `json:"mime_type"`
	Verification string  `json:"verification"`
	Width        int64   `json:"width"`
	Height       int64   `json:"height"`
	Bytes        int64   `json:"bytes"`
	ThumbURL     *string `json:"thumb_url"`
	ThumbSHA256  *string `json:"thumb_sha256"`
}

// MediaLink is a separately versioned row; its ETag never covers Media bytes.
type MediaLink struct {
	ID          string `json:"id"`
	Revision    int64  `json:"revision"`
	MediaID     string `json:"media_id"`
	OwnerType   string `json:"owner_type"`
	OwnerID     string `json:"owner_id"`
	IsCover     bool   `json:"is_cover"`
	SortOrder   int64  `json:"sort_order"`
	Description string `json:"description"`
}

// MediaLinkView joins separately versioned source for a live, non-ETagged page.
type MediaLinkView struct {
	MediaLink
	Media Media `json:"media"`
}

type MediaLinkList = metadataList[MediaLinkView]

// MediaLibrary owns private authoritative media reads; image streaming and
// uploads require separate file-integrity and transport gates.
type MediaLibrary struct{ auth *Auth }

func NewMediaLibrary(a *Auth) (*MediaLibrary, error) {
	if a == nil || a.store == nil {
		return nil, ErrDomainInput
	}
	return a.store.NewMediaLibrary(a)
}

func (s *Store) NewMediaLibrary(a *Auth) (*MediaLibrary, error) {
	if s == nil || a == nil || a.store != s {
		return nil, ErrDomainInput
	}
	return &MediaLibrary{auth: a}, nil
}

func publicMedia(value retainedMedia) Media {
	return Media{ID: value.ID, Revision: value.Revision, SHA256: value.SHA256, MIMEType: value.MIMEType,
		Verification: value.Verification, Width: value.Width, Height: value.Height, Bytes: value.Bytes,
		ThumbURL: value.ThumbURL, ThumbSHA256: value.ThumbSHA256}
}

func publicMediaLink(value retainedMediaLink) MediaLink {
	return MediaLink{ID: value.ID, Revision: value.Revision, MediaID: value.MediaID,
		OwnerType: value.OwnerType, OwnerID: value.OwnerID, IsCover: value.IsCover,
		SortOrder: value.SortOrder, Description: value.Description}
}

func (library *MediaLibrary) Get(ctx context.Context, token, id string) (Media, error) {
	if !validDomainID(id) {
		return Media{}, ErrDomainInput
	}
	return readDomain(ctx, library.auth, token, func(tx *sql.Tx) (Media, error) {
		value, err := loadMedia(ctx, tx, id)
		if err != nil {
			return Media{}, err
		}
		return publicMedia(value), nil
	})
}

// PageOwnerLinks completes only a selected actual owner collection. A caller
// can bracket pages with matching owner reads; embedded Media has its own proof.
func (library *MediaLibrary) PageOwnerLinks(ctx context.Context, token, ownerType, ownerID, cursor string, limit int) (MediaLinkList, error) {
	var column string
	switch ownerType {
	case "item":
		column = "item_id"
	case "unit":
		column = "item_unit_id"
	case "location":
		column = "location_id"
	default:
		return MediaLinkList{}, ErrDomainInput
	}
	if !validDomainID(ownerID) {
		return MediaLinkList{}, ErrDomainInput
	}
	purpose := "media-owner-links-cursor-v1\x00" + projectionPair(ownerType, ownerID)
	return readMediaLinksPage(ctx, library.auth, token, cursor, limit, purpose, column, ownerID, func(tx *sql.Tx) error {
		return loadMediaOwner(ctx, tx, ownerType, ownerID)
	})
}

// PageMediaLinks permits a complete Media-before/pages/Media-after review of
// every real owner. Signed continuations are live, not frozen sync snapshots.
func (library *MediaLibrary) PageMediaLinks(ctx context.Context, token, mediaID, cursor string, limit int) (MediaLinkList, error) {
	if !validDomainID(mediaID) {
		return MediaLinkList{}, ErrDomainInput
	}
	purpose := "media-membership-links-cursor-v1\x00" + mediaID
	return readMediaLinksPage(ctx, library.auth, token, cursor, limit, purpose, "media_id", mediaID, func(tx *sql.Tx) error {
		_, err := loadMedia(ctx, tx, mediaID)
		return err
	})
}

func readMediaLinksPage(ctx context.Context, auth *Auth, token, cursor string, limit int, purpose, column, id string,
	owner func(*sql.Tx) error,
) (MediaLinkList, error) {
	return readMetadataPage(ctx, auth, token, cursor, limit, purpose, func(ctx context.Context, tx *sql.Tx, afterID string, size int) (metadataPage[MediaLinkView], error) {
		if err := owner(tx); err != nil {
			return metadataPage[MediaLinkView]{}, err
		}
		rows, err := tx.QueryContext(ctx, mediaLinkSelect+" WHERE l."+column+"=? AND l.id>? ORDER BY l.id LIMIT ?", id, afterID, size+1)
		if err != nil {
			return metadataPage[MediaLinkView]{}, err
		}
		defer rows.Close()
		page := metadataPage[MediaLinkView]{Data: make([]MediaLinkView, 0)}
		for rows.Next() {
			link, err := scanMediaLink(rows)
			if err == nil {
				err = validateMediaLinkAuthority(ctx, tx, link)
			}
			if err != nil {
				return metadataPage[MediaLinkView]{}, err
			}
			image, err := loadMedia(ctx, tx, link.MediaID)
			if err != nil {
				return metadataPage[MediaLinkView]{}, mediaAuthorityError(err)
			}
			page.Data = append(page.Data, MediaLinkView{MediaLink: publicMediaLink(link), Media: publicMedia(image)})
		}
		if err := rows.Err(); err != nil {
			return metadataPage[MediaLinkView]{}, err
		}
		if len(page.Data) > size {
			page.Data = page.Data[:size]
			page.NextID = page.Data[size-1].ID
		}
		return page, nil
	})
}
