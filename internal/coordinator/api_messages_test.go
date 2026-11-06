package coordinator

import (
	"testing"
	"time"
)

func TestAPIMessageReceiptSurvivesRestartAndCannotChangeGeneration(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDatabase(root)
	if err != nil {
		t.Fatal(err)
	}
	value := APIMessageReceipt{ItemID: "item", MessageID: "opaque-message", RuntimeID: "runtime", Generation: 3, CreatedAt: time.Now().UTC()}
	if err := db.RecordAPIMessage(value); err != nil {
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
	got, found, err := db.APIMessage(value.ItemID, value.MessageID)
	if err != nil || !found || got.Delivered || got.RuntimeID != value.RuntimeID {
		t.Fatalf("pending receipt: %+v %v %v", got, found, err)
	}
	replacement := value
	replacement.RuntimeID = "replacement"
	replacement.Generation++
	if err := db.RecordAPIMessage(replacement); err == nil {
		t.Fatal("pending message moved onto replacement generation")
	}
	value.Delivered = true
	if err := db.RecordAPIMessage(value); err != nil {
		t.Fatal(err)
	}
	value.Delivered = false
	if err := db.RecordAPIMessage(value); err != nil {
		t.Fatal(err)
	}
	got, found, err = db.APIMessage(value.ItemID, value.MessageID)
	if err != nil || !found || !got.Delivered {
		t.Fatalf("delivered receipt regressed: %+v %v %v", got, found, err)
	}
	if err := db.ForgetAPIMessage(value.ItemID, value.MessageID); err != nil {
		t.Fatal(err)
	}
	_, found, err = db.APIMessage(value.ItemID, value.MessageID)
	if err != nil || found {
		t.Fatalf("forgot receipt: %v %v", found, err)
	}
}
func TestAPIMessageKeysDoNotAliasAcrossItems(t *testing.T) {
	if apiMessageKey("a", "bc") == apiMessageKey("ab", "c") {
		t.Fatal("ambiguous item/message boundary")
	}
}
func TestRuntimeHostDoesNotExposeOperatorCommands(t *testing.T) {
	for _, method := range []string{MethodCreateItem, MethodManifestCommand, MethodRegisterRepository, MethodRenameRepository, MethodLeaseAcquire, MethodShutdown} {
		if hostMethodAllowed(method) {
			t.Fatalf("host exposes %s", method)
		}
	}
	for _, method := range []string{MethodRuntimeEvent, MethodStoreCommand, MethodItemResources, MethodCanonicalManifest} {
		if !hostMethodAllowed(method) {
			t.Fatalf("host lacks %s", method)
		}
	}
}
