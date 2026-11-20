package coordinator

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

// RecoverClosedSlugs records, on items closed before closed slugs were
// kept, the slug their close event says they had, so that slug still names
// them. Items that already have one, or whose history names none, are left
// as they are; later starts find nothing to do.
func (d *Database) RecoverClosedSlugs(now time.Time) (int, error) {
	manifests, err := d.ListManifests()
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, manifest := range manifests {
		if manifest.State != model.StateClosed || manifest.ClosedSlug != "" {
			continue
		}
		events, err := d.ListItemEvents(manifest.ID)
		if err != nil {
			return recovered, err
		}
		slug := ""
		for _, event := range events {
			var payload struct {
				NewState     string `json:"new_state"`
				PreviousSlug string `json:"previous_slug"`
			}
			if json.Unmarshal(event.Payload, &payload) == nil && payload.NewState == model.StateClosed && payload.PreviousSlug != "" {
				slug = payload.PreviousSlug
			}
		}
		if _, valid := model.NormalizeSlugSelector(slug); !valid {
			continue
		}
		err = d.db.Update(func(tx *bolt.Tx) error {
			projection := tx.Bucket(bucketProjections).Bucket([]byte(ManifestProjection))
			if projection == nil || projection.Get([]byte(manifest.ID)) == nil {
				return nil
			}
			var current model.Manifest
			if err := json.Unmarshal(projection.Get([]byte(manifest.ID)), &current); err != nil {
				return err
			}
			if current.State != model.StateClosed || current.ClosedSlug != "" {
				return nil
			}
			current.ClosedSlug = slug
			payload, _ := json.Marshal(map[string]any{"closed_slug": slug})
			_, _, err = commitManifestEvents(tx, current, "closed-slug-"+manifest.ID, "wi", now.UTC(), []commandEvent{{Type: "work_item.closed_slug_recorded", Payload: payload}})
			return err
		})
		if err != nil {
			return recovered, fmt.Errorf("record closed slug of %s: %w", manifest.ID, err)
		}
		recovered++
	}
	if recovered > 0 {
		d.changes.recordAll()
	}
	return recovered, nil
}
