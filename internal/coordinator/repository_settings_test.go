package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/regb/workitem/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/regb/workitem/internal/options"
	bolt "go.etcd.io/bbolt"
)

func settingsRegistry(t *testing.T) *options.Registry {
	t.Helper()
	r, err := options.New(options.Definition{Name: "direnv", Options: []options.Option{{Name: "trust", Type: "string", Default: "prompt", Choices: []string{"prompt", "always", "never"}, Repository: true}, {Name: "load", Type: "bool", Default: true, Repository: true}, {Name: "secret", Type: "string", Default: "", Secret: true, Repository: true}, {Name: "command", Type: "string", Default: ""}}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func settingCommand(id string, rev uint64, key string, value json.RawMessage) SetRepositorySettingCommand {
	return SetRepositorySettingCommand{ID: "setting:" + id, ProtocolVersion: ProtocolVersion, RepositoryID: importTestID, ExpectedRevision: rev, Namespace: "plugins.direnv", Key: key, Value: value, CreatedAt: time.Now().UTC()}
}
func TestRepositorySettingsAreDurableIdempotentAndFollowIdentity(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry := settingsRegistry(t)
	if _, err := db.RegisterRepository(repositoryTestCommand(importTestID, "old-name")); err != nil {
		t.Fatal(err)
	}
	empty, err := db.RepositorySettings("old-name")
	if err != nil || empty.Settings.Revision != 0 || len(empty.Settings.Namespaces) != 0 {
		t.Fatal(empty, err)
	}
	command := settingCommand("first", 0, "trust", json.RawMessage(`"always"`))
	first, err := db.SetRepositorySetting(command, registry)
	if err != nil || !first.Changed || first.Revision != 1 {
		t.Fatal(first, err)
	}
	duplicate, err := db.SetRepositorySetting(command, nil)
	if err != nil || !duplicate.Duplicate || duplicate.Sequence != first.Sequence {
		t.Fatal("ack replay failed after schema removal", duplicate, err)
	}
	changed := command
	changed.Value = json.RawMessage(`"never"`)
	if _, err := db.SetRepositorySetting(changed, registry); err == nil {
		t.Fatal("reused identity with different input")
	}
	if _, err := db.SetRepositorySetting(settingCommand("stale", 0, "load", json.RawMessage(`false`)), registry); err == nil {
		t.Fatal("stale revision accepted")
	}
	noop, err := db.SetRepositorySetting(settingCommand("noop", 1, "trust", json.RawMessage(`"always"`)), registry)
	if err != nil || noop.Changed || noop.Revision != 1 {
		t.Fatal(noop, err)
	}
	status, _ := db.Status()
	if status.GlobalSequence != first.Sequence {
		t.Fatal("no-op appended history")
	}
	if _, err := db.RenameRepository(RenameRepositoryCommand{ID: "rename:settings", ProtocolVersion: ProtocolVersion, RepositoryID: importTestID, PreviousName: "old-name", Name: "new-name", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetRepositoryArchived(archiveTestCommand("settings", false, true)); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stored, err := db.RepositorySettings("new-name")
	if err != nil || !stored.Repository.Archived || string(stored.Settings.Namespaces["plugins.direnv"]["trust"]) != `"always"` {
		t.Fatal(stored, err)
	}
	if _, err := db.RegisterRepository(repositoryTestCommand("01KZYHGDCECSFS4BJ2SNTQP49W", "old-name")); err != nil {
		t.Fatal(err)
	}
	other, err := db.RepositorySettings("old-name")
	if err != nil || len(other.Settings.Namespaces) != 0 {
		t.Fatal("name reuse transferred policy", other, err)
	}
	unset := settingCommand("unset", 1, "trust", nil)
	unset.Unset = true
	removed, err := db.SetRepositorySetting(unset, nil)
	if err != nil || !removed.Changed || removed.Revision != 2 {
		t.Fatal("cannot clean up unavailable schema", removed, err)
	}
	after, err := db.RepositorySettings(importTestID)
	if err != nil || len(after.Settings.Namespaces) != 0 {
		t.Fatal(after, err)
	}
}
func TestSettingsValidateAtWriterAndKeepValuesOutOfHistory(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	registry := settingsRegistry(t)
	if _, err := db.RegisterRepository(repositoryTestCommand(importTestID, "project")); err != nil {
		t.Fatal(err)
	}
	for i, mutate := range []func(*SetRepositorySettingCommand){
		func(c *SetRepositorySettingCommand) { c.Value = json.RawMessage(`"typo"`) }, func(c *SetRepositorySettingCommand) { c.Value = json.RawMessage(`true`) }, func(c *SetRepositorySettingCommand) { c.Key = "missing" }, func(c *SetRepositorySettingCommand) { c.Key = "command" }, func(c *SetRepositorySettingCommand) { c.Namespace = "plugins.missing" }, func(c *SetRepositorySettingCommand) { c.Value = json.RawMessage(`"x" "y"`) }, func(c *SetRepositorySettingCommand) {
			c.Value = json.RawMessage(`"` + strings.Repeat("x", 9<<10) + `"`)
		}, func(c *SetRepositorySettingCommand) { c.Unset = true }, func(c *SetRepositorySettingCommand) { c.RepositoryID = "../escape" },
	} {
		command := settingCommand(string(rune('a'+i)), 0, "trust", json.RawMessage(`"always"`))
		mutate(&command)
		if _, err := db.SetRepositorySetting(command, registry); err == nil {
			t.Fatal("invalid command accepted", i)
		}
	}
	secret := "do-not-retain-this-secret"
	command := settingCommand("secret", 0, "secret", json.RawMessage(`"`+secret+`"`))
	if _, err := db.SetRepositorySetting(command, registry); err != nil {
		t.Fatal(err)
	}
	if err := db.db.View(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{bucketEvents, bucketDomainCommands} {
			if err := tx.Bucket(bucket).ForEach(func(_, value []byte) error {
				if bytes.Contains(value, []byte(secret)) {
					t.Fatal("secret entered history or acknowledgement")
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := db.RepositorySettings(importTestID)
	if err != nil || !strings.Contains(string(stored.Settings.Namespaces["plugins.direnv"]["secret"]), secret) {
		t.Fatal("canonical value missing", err)
	}
}
func TestConcurrentSettingWritersUseRevisionFencing(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.RegisterRepository(repositoryTestCommand(importTestID, "project")); err != nil {
		t.Fatal(err)
	}
	registry := settingsRegistry(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{"one", "two"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_, err := db.SetRepositorySetting(settingCommand(id, 0, "trust", json.RawMessage(`"always"`)), registry)
			results <- err
		}(id)
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("lost revision fence", success)
	}
}
func TestRepositorySettingsAggregateLimitIsAtomic(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.RegisterRepository(repositoryTestCommand(importTestID, "project")); err != nil {
		t.Fatal(err)
	}
	definition := options.Definition{Name: "sample"}
	for i := 0; i < 9; i++ {
		definition.Options = append(definition.Options, options.Option{Name: string(rune('a' + i)), Type: "string", Default: "", Repository: true})
	}
	registry, err := options.New(definition)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 9; i++ {
		command := settingCommand("limit:"+string(rune('a'+i)), uint64(i), string(rune('a'+i)), json.RawMessage(`"`+strings.Repeat("x", 7900)+`"`))
		command.Namespace = "plugins.sample"
		_, err := db.SetRepositorySetting(command, registry)
		if i < 8 && err != nil {
			t.Fatal(err)
		}
		if i == 8 && err == nil {
			t.Fatal("aggregate bound not enforced")
		}
	}
	stored, err := db.RepositorySettings(importTestID)
	if err != nil || stored.Settings.Revision != 8 || len(stored.Settings.Namespaces["plugins.sample"]) != 8 {
		t.Fatal("oversize write partially applied", err)
	}
}

func TestSettingsTransactionRollsBackOnHistoryFailure(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.RegisterRepository(repositoryTestCommand(importTestID, "project")); err != nil {
		t.Fatal(err)
	}
	command := settingCommand("interrupted", 0, "trust", json.RawMessage(`"always"`))
	eventID := domainEventID(command.ID, 0)
	if err := db.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketEventIDs).Put([]byte(eventID), encodeUint64(99)) }); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SetRepositorySetting(command, settingsRegistry(t)); err == nil {
		t.Fatal("history collision accepted")
	}
	state, err := db.RepositorySettings(importTestID)
	if err != nil || state.Settings.Revision != 0 || len(state.Settings.Namespaces) != 0 {
		t.Fatal("partial transaction escaped", state, err)
	}
	if err := db.db.Update(func(tx *bolt.Tx) error { return tx.Bucket(bucketEventIDs).Delete([]byte(eventID)) }); err != nil {
		t.Fatal(err)
	}
	if result, err := db.SetRepositorySetting(command, settingsRegistry(t)); err != nil || result.Revision != 1 || result.Duplicate {
		t.Fatal(result, err)
	}
}
func TestMalformedRepositorySettingsDoNotFallBackToDefaults(t *testing.T) {
	db, err := OpenDatabase(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.RegisterRepository(repositoryTestCommand(importTestID, "project")); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{`, `{"repository_id":"wrong","revision":1,"namespaces":{}}`, `{"repository_id":"` + importTestID + `","revision":0,"namespaces":{}}`, `{"repository_id":"` + importTestID + `","revision":1,"namespaces":{"plugins.direnv":null}}`} {
		if err := db.db.Update(func(tx *bolt.Tx) error {
			bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(RepositorySettingsProjection))
			if err != nil {
				return err
			}
			return bucket.Put([]byte(importTestID), []byte(raw))
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.RepositorySettings(importTestID); err == nil {
			t.Fatal("malformed settings silently defaulted")
		}
	}
}

func TestSettingsEndpointIsOperatorOnly(t *testing.T) {
	root := t.TempDir()
	sockets := testutil.ShortTempDir(t)
	operator := filepath.Join(sockets, "operator.sock")
	agentSocket := filepath.Join(sockets, "agent.sock")
	server, err := NewServerWithAgentSocket(root, operator, agentSocket)
	if err != nil {
		t.Fatal(err)
	}
	server.ConfigureRepositorySettings(settingsRegistry(t))
	defer server.Close()
	if _, err := server.database.RegisterRepository(repositoryTestCommand(importTestID, "project")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go server.Serve(ctx)
	op := &Client{SocketPath: operator}
	if _, err := op.SetRepositorySetting(context.Background(), settingCommand("socket", 0, "trust", json.RawMessage(`"always"`))); err != nil {
		t.Fatal(err)
	}
	restricted := &Client{SocketPath: agentSocket}
	if _, err := restricted.RepositorySettings(context.Background(), importTestID); err == nil || !strings.Contains(err.Error(), "agent endpoint") {
		t.Fatal("agent could read private settings", err)
	}
	if _, err := restricted.SetRepositorySetting(context.Background(), settingCommand("agent", 1, "trust", json.RawMessage(`"never"`))); err == nil || !strings.Contains(err.Error(), "agent endpoint") {
		t.Fatal("agent could modify trust", err)
	}
}

func TestSchema8MovesRepositorySettingsToPluginNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name       string
		namespaces string
		fails      bool
	}{
		{"renamed", `{"extensions.direnv":{"trust":"\"always\""},"extensions.retired":{"flag":"true"}}`, false},
		{"collision", `{"extensions.direnv":{"trust":"\"always\""},"plugins.direnv":{"load":"false"}}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			db, err := OpenDatabase(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.RegisterRepository(repositoryTestCommand(importTestID, "project")); err != nil {
				t.Fatal(err)
			}
			var namespaces map[string]map[string]string
			if err := json.Unmarshal([]byte(tc.namespaces), &namespaces); err != nil {
				t.Fatal(err)
			}
			stored := map[string]map[string]json.RawMessage{}
			for namespace, entries := range namespaces {
				stored[namespace] = map[string]json.RawMessage{}
				for key, value := range entries {
					stored[namespace][key] = json.RawMessage(value)
				}
			}
			legacy, _ := json.Marshal(map[string]any{"repository_id": importTestID, "revision": 3, "namespaces": stored})
			if err := db.db.Update(func(tx *bolt.Tx) error {
				bucket, err := tx.Bucket(bucketProjections).CreateBucketIfNotExists([]byte(RepositorySettingsProjection))
				if err != nil {
					return err
				}
				if err := bucket.Put([]byte(importTestID), legacy); err != nil {
					return err
				}
				return tx.Bucket(bucketMeta).Put(keySchemaVersion, encodeUint64(7))
			}); err != nil {
				t.Fatal(err)
			}
			db.Close()
			upgraded, err := OpenDatabase(root)
			if tc.fails {
				if err == nil {
					upgraded.Close()
					t.Fatal("colliding namespaces were merged silently")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer upgraded.Close()
			if _, err := os.Stat(DatabasePath(root) + ".schema-7.bak"); err != nil {
				t.Fatalf("pre-upgrade backup missing: %v", err)
			}
			settings, err := upgraded.RepositorySettings(importTestID)
			if err != nil {
				t.Fatal(err)
			}
			got := settings.Settings.Namespaces
			if settings.Settings.Revision != 3 || len(got) != 2 || string(got["plugins.direnv"]["trust"]) != `"always"` || string(got["plugins.retired"]["flag"]) != "true" {
				t.Fatalf("settings = %+v", settings.Settings)
			}
		})
	}
}
