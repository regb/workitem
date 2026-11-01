package coordinator

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/regb/workitem/internal/model"
	bolt "go.etcd.io/bbolt"
)

const (
	AgentProfilesProjection  = "agent_profiles"
	MethodAgentProfiles      = "agent_profiles"
	MethodChangeAgentProfile = "agent_profile_change"
	agentProfilesKey         = "profiles"
	maxAgentProfiles         = 100
	maxAgentProfilesBytes    = 256 << 10
)

// ChangeAgentProfileCommand changes the agent profiles: Put adds or replaces
// one, Remove deletes one, Default makes one the default. Items that use a
// removed profile keep its name and say so when they next start.
type ChangeAgentProfileCommand struct {
	ExpectedRevision *uint64             `json:"expected_revision,omitempty"`
	Put              *model.AgentProfile `json:"put,omitempty"`
	Remove           string              `json:"remove,omitempty"`
	Default          string              `json:"default,omitempty"`
}

func (c *Client) AgentProfiles(ctx context.Context) (model.AgentProfiles, error) {
	var result model.AgentProfiles
	err := c.Call(ctx, MethodAgentProfiles, struct{}{}, &result)
	return result, err
}

func (c *Client) ChangeAgentProfile(ctx context.Context, command ChangeAgentProfileCommand) (model.AgentProfiles, error) {
	var result model.AgentProfiles
	err := c.Call(ctx, MethodChangeAgentProfile, command, &result)
	return result, err
}

func readAgentProfilesTx(tx *bolt.Tx) (model.AgentProfiles, error) {
	record := model.AgentProfiles{Profiles: []model.AgentProfile{}}
	bucket := tx.Bucket(bucketProjections).Bucket([]byte(AgentProfilesProjection))
	if bucket == nil {
		return record, nil
	}
	raw := bucket.Get([]byte(agentProfilesKey))
	if raw == nil {
		return record, nil
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return record, fmt.Errorf("decode agent profiles: %w", err)
	}
	if record.Profiles == nil {
		record.Profiles = []model.AgentProfile{}
	}
	return record, nil
}

func (d *Database) AgentProfiles() (model.AgentProfiles, error) {
	var result model.AgentProfiles
	err := d.db.View(func(tx *bolt.Tx) (err error) {
		result, err = readAgentProfilesTx(tx)
		return err
	})
	return result, err
}

// ChangeAgentProfile applies one change and records which profile changed.
// The first profile added becomes the default.
func (d *Database) ChangeAgentProfile(command ChangeAgentProfileCommand) (model.AgentProfiles, error) {
	changes := 0
	for _, set := range []bool{command.Put != nil, command.Remove != "", command.Default != ""} {
		if set {
			changes++
		}
	}
	if changes != 1 {
		return model.AgentProfiles{}, fmt.Errorf("an agent profile change adds or replaces, removes, or sets the default profile")
	}
	if command.Put != nil {
		if err := command.Put.Validate(); err != nil {
			return model.AgentProfiles{}, err
		}
	}
	var result model.AgentProfiles
	err := d.db.Update(func(tx *bolt.Tx) error {
		profiles, err := readAgentProfilesTx(tx)
		if err != nil {
			return err
		}
		if command.ExpectedRevision != nil && *command.ExpectedRevision != profiles.Revision {
			return fmt.Errorf("the agent profiles changed since they were read; reload and try again")
		}
		name, action := "", ""
		switch {
		case command.Put != nil:
			name, action = command.Put.Name, "put"
			at := slices.IndexFunc(profiles.Profiles, func(p model.AgentProfile) bool { return p.Name == name })
			if at >= 0 {
				profiles.Profiles[at] = *command.Put
			} else {
				if len(profiles.Profiles) >= maxAgentProfiles {
					return fmt.Errorf("at most %d agent profiles", maxAgentProfiles)
				}
				profiles.Profiles = append(profiles.Profiles, *command.Put)
			}
			if profiles.Default == "" {
				profiles.Default = name
			}
		case command.Remove != "":
			name, action = command.Remove, "remove"
			if _, ok := profiles.Find(name); !ok {
				return fmt.Errorf("no agent profile %s", name)
			}
			if profiles.Default == name && len(profiles.Profiles) > 1 {
				return fmt.Errorf("%s is the default agent profile; make another the default first", name)
			}
			profiles.Profiles = slices.DeleteFunc(profiles.Profiles, func(p model.AgentProfile) bool { return p.Name == name })
			if profiles.Default == name {
				profiles.Default = ""
			}
		default:
			name, action = command.Default, "default"
			if _, ok := profiles.Find(name); !ok {
				return fmt.Errorf("no agent profile %s", name)
			}
			profiles.Default = name
		}
		profiles.Revision++
		data, err := json.Marshal(profiles)
		if err != nil {
			return err
		}
		if len(data) > maxAgentProfilesBytes {
			return fmt.Errorf("agent profiles exceed 256 KiB")
		}
		bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(AgentProfilesProjection))
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(agentProfilesKey), data); err != nil {
			return err
		}
		result = profiles
		payload, _ := json.Marshal(map[string]any{"profile": name, "change": action, "revision": profiles.Revision})
		_, err = appendDomainEventTx(tx, DomainEvent{ID: fmt.Sprintf("agent-profiles-%d", profiles.Revision), Type: "agent_profile.changed", Timestamp: time.Now().UTC(), Actor: "user", Payload: payload})
		return err
	})
	return result, err
}
