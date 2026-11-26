package daemonapi

import (
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// Every procedure is decided for an item's agent: on an item at a level,
// for a person only, or open to every item. Adding an RPC forces it.
func TestEveryProcedureIsClassifiedForItemsAgents(t *testing.T) {
	_ = v1.File_wi_core_v1_items_proto
	seen := map[string]bool{}
	defer func() {
		for procedure := range procedures {
			if !seen[procedure] {
				t.Errorf("procedures describes %s, which the API does not have", procedure)
			}
		}
	}()
	protoregistry.GlobalFiles.RangeFilesByPackage("wi.core.v1", func(file protoreflect.FileDescriptor) bool {
		services := file.Services()
		for i := range services.Len() {
			methods := services.Get(i).Methods()
			for j := range methods.Len() {
				procedure := "/" + string(services.Get(i).FullName()) + "/" + string(methods.Get(j).Name())
				_, level := itemLevels[procedure]
				_, users := usersOnly[procedure]
				count := 0
				for _, in := range []bool{level, users, anyItem[procedure]} {
					if in {
						count++
					}
				}
				if count != 1 {
					t.Errorf("%s must be in exactly one of itemLevels, usersOnly and anyItem", procedure)
				}
				rule, described := procedures[procedure]
				if !described {
					t.Errorf("%s is not described in procedures", procedure)
					continue
				}
				if level && rule.item == nil {
					t.Errorf("%s has a level but names no item", procedure)
				}
				describedTo(t, procedure, rule, methods.Get(j))
				seen[procedure] = true
			}
		}
		return true
	})
}

// describedTo checks a procedure's description against its request: what it
// reads exists, and what it locks it names.
func describedTo(t *testing.T, procedure string, rule procedure, method protoreflect.MethodDescriptor) {
	t.Helper()
	kind, err := protoregistry.GlobalTypes.FindMessageByName(method.Input().FullName())
	if err != nil {
		t.Fatal(err)
	}
	request := kind.New().Interface()
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Errorf("%s: reading its request: %v", procedure, recovered)
			}
		}()
		if rule.item != nil {
			rule.item(request)
		}
		if rule.repository != nil {
			rule.repository(request)
		}
		if rule.lockIf != nil {
			rule.lockIf(request)
		}
	}()
	fields := method.Input().Fields()
	if rule.item == nil && fields.ByName("item_id") != nil {
		t.Errorf("%s names an item_id the description does not read", procedure)
	}
	if rule.repository == nil && fields.ByName("repository_id") != nil {
		t.Errorf("%s names a repository_id the description does not read", procedure)
	}
	switch rule.lock {
	case lockItem:
		if rule.item == nil {
			t.Errorf("%s locks an item it does not name", procedure)
		}
	case lockRepository:
		if rule.repository == nil {
			t.Errorf("%s locks a repository it does not name", procedure)
		}
	}
	if (method.IsStreamingServer() || method.IsStreamingClient()) && rule.lock != lockNone {
		t.Errorf("%s is a stream, which takes no lock", procedure)
	}
}
