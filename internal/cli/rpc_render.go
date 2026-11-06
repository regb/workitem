package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"github.com/regb/workitem/internal/apiview"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Human output is independent of protojson. Specific views keep concise tables;
// the reflection fallback renders new response fields as readable labeled text.
func renderRPC(w io.Writer, message proto.Message) error {
	switch value := message.(type) {
	case *v1.GetItemResponse:
		return renderItem(w, value.Item, value.Description)
	case *v1.ListNotesResponse:
		if len(value.Notes) == 0 {
			_, err := fmt.Fprintln(w, "no notes")
			return err
		}
		for _, note := range value.Notes {
			fmt.Fprintf(w, "%s", humanTime(note.Time))
			if note.Head != "" {
				fmt.Fprintf(w, "  %s", note.Head)
			}
			fmt.Fprintf(w, "\n%s\n", note.Text)
			if note.Attachment != nil {
				fmt.Fprintf(w, "Attachment: %s (%d bytes)\n", note.Attachment.Name, note.Attachment.Size)
			}
		}
		return nil
	case *v1.GetAgentSettingsResponse:
		fmt.Fprintf(w, "Driver: %s\n", value.Driver)
		keys := make([]string, 0, len(value.Values))
		for key := range value.Values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			origin := "profile"
			if _, ok := value.Overrides[key]; ok {
				origin = "item override"
			}
			fmt.Fprintf(w, "%s: %s (%s)", key, value.Values[key], origin)
			if running := value.Running[key]; running != "" {
				fmt.Fprintf(w, "; running: %s", running)
			}
			fmt.Fprintln(w)
		}
		if value.CatalogError != "" {
			fmt.Fprintln(w, "Catalog:", value.CatalogError)
		}
		return nil
	case *v1.GetRepositorySettingsResponse:
		for _, setting := range value.Settings {
			v := any("[redacted]")
			if setting.Effective != nil {
				v = cliSettingAny(setting.Effective)
			}
			fmt.Fprintf(w, "%s = %v (%s)\n", setting.Key, v, setting.Origin)
		}
		fmt.Fprintf(w, "Revision: %d\n", value.Revision)
		return nil
	case *v1.ListRepositoriesResponse:
		table := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		fmt.Fprintln(table, "NAME	ID	BRANCH	UPSTREAM	STATE")
		for _, repo := range value.Repositories {
			upstream := "local"
			if repo.Upstream != nil {
				upstream = repo.Upstream.Url + " " + repo.Upstream.Branch
			}
			status := "active"
			if repo.Archived {
				status = "archived"
			}
			fmt.Fprintf(table, "%s	%s	%s	%s	%s\n", repo.Name, repo.Id, repo.IntegrationBranch, upstream, status)
		}
		return table.Flush()
	case *v1.GetEnvironmentResponse:
		for _, variable := range value.Variables {
			fmt.Fprintf(w, "%s=%s\n", variable.Key, variable.Value)
		}
		fmt.Fprintf(w, "Revision: %d\n", value.Revision)
		return nil
	case *v1.GetAgentResponse:
		if value.Agent == nil {
			return nil
		}
		fmt.Fprintf(w, "Agent: %s (%s)\nOnline: %t\nMode: %s\nStatus: %s\nGeneration: %d\n", value.Agent.AgentProfile, value.Agent.Driver, value.Agent.Online, apiview.Mode(value.Agent.Mode), apiview.Status(value.Agent.Status), value.Agent.Generation)
		if value.Agent.Reason != "" {
			fmt.Fprintln(w, "Reason:", value.Agent.Reason)
		}
		if terminal := value.Agent.Terminal; terminal != nil {
			fmt.Fprintf(w, "Terminal: %s:%s\nTmux socket: %s\nAttached clients: %d\n", terminal.Session, terminal.Window, terminal.TmuxSocket, terminal.AttachedClients)
		}
		for _, warning := range value.Warnings {
			fmt.Fprintln(w, "warning:", warning)
		}
		return nil
	case *v1.AbortTurnResponse:
		_, err := fmt.Fprintln(w, "submitted abort control command")
		return err
	case *v1.SendMessageResponse:
		fmt.Fprintf(w, "submitted message to agent generation %d\n", value.Generation)
		if value.Job != nil {
			return renderRPC(w, value.Job)
		}
		return nil
	case *v1.StopAgentResponse:
		fmt.Fprintln(w, "agent stopped")
		for _, warning := range value.Warnings {
			fmt.Fprintln(w, "warning:", warning)
		}
		return nil
	case *v1.MarkReadResponse:
		_, err := fmt.Fprintln(w, "marked read")
		return err
	case *v1.DeferItemResponse:
		_, err := fmt.Fprintln(w, "deferred item")
		return err
	}
	return renderMessage(w, message.ProtoReflect(), "")
}
func humanTime(value *timestamppb.Timestamp) string {
	if value == nil {
		return "(none)"
	}
	return value.AsTime().Local().Format(timeFormatHuman)
}
func humanEnum(field protoreflect.FieldDescriptor, value protoreflect.Value) string {
	enum := field.Enum().Values().ByNumber(value.Enum())
	if enum == nil {
		return fmt.Sprint(value.Enum())
	}
	name := string(enum.Name())
	prefix := string(field.Enum().Values().Get(0).Name())
	prefix = strings.TrimSuffix(prefix, "UNSPECIFIED")
	return strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(name, prefix), "_", " "))
}
func renderMessage(w io.Writer, m protoreflect.Message, indent string) error {
	if value, ok := m.Interface().(*structpb.Struct); ok {
		return printConfigTree(w, strings.TrimSpace(indent), value.AsMap())
	}
	if timestamp, ok := m.Interface().(*timestamppb.Timestamp); ok {
		_, err := fmt.Fprintln(w, indent+humanTime(timestamp))
		return err
	}
	fields := m.Descriptor().Fields()
	shown := false
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if !m.Has(field) && field.Kind() != protoreflect.BoolKind {
			continue
		}
		shown = true
		label := strings.ReplaceAll(string(field.Name()), "_", " ")
		value := m.Get(field)
		if field.IsMap() {
			var keys []string
			value.Map().Range(func(key protoreflect.MapKey, _ protoreflect.Value) bool {
				keys = append(keys, key.String())
				return true
			})
			sort.Strings(keys)
			for _, key := range keys {
				fmt.Fprintf(w, "%s%s.%s: %v\n", indent, label, key, value.Map().Get(protoreflect.ValueOfString(key).MapKey()))
			}
			continue
		}
		if field.IsList() {
			list := value.List()
			fmt.Fprintf(w, "%s%s:\n", indent, label)
			for j := 0; j < list.Len(); j++ {
				if field.Kind() == protoreflect.MessageKind {
					fmt.Fprintf(w, "%s  -\n", indent)
					if err := renderMessage(w, list.Get(j).Message(), indent+"    "); err != nil {
						return err
					}
				} else {
					fmt.Fprintf(w, "%s  %v\n", indent, list.Get(j))
				}
			}
			continue
		}
		if field.Kind() == protoreflect.MessageKind {
			if timestamp, ok := value.Message().Interface().(*timestamppb.Timestamp); ok {
				fmt.Fprintf(w, "%s%s: %s\n", indent, label, humanTime(timestamp))
			} else {
				fmt.Fprintf(w, "%s%s:\n", indent, label)
				if err := renderMessage(w, value.Message(), indent+"  "); err != nil {
					return err
				}
			}
			continue
		}
		if field.Kind() == protoreflect.EnumKind {
			fmt.Fprintf(w, "%s%s: %s\n", indent, label, humanEnum(field, value))
		} else if field.Kind() == protoreflect.BytesKind {
			fmt.Fprintf(w, "%s%s: %d bytes\n", indent, label, len(value.Bytes()))
		} else {
			fmt.Fprintf(w, "%s%s: %v\n", indent, label, value.Interface())
		}
	}
	if !shown {
		_, err := fmt.Fprintln(w, indent+"done")
		return err
	}
	return nil
}
func renderItem(w io.Writer, item *v1.Item, description string) error {
	if item == nil {
		return nil
	}
	fmt.Fprintf(w, "ID: %s\nSlug: %s\nTitle: %s\nState: %s\nDeep work: %t\nLabels: %s\n", item.Id, item.Slug, item.Title, apiview.State(item.State), item.DeepWork, strings.Join(item.Labels, ", "))
	if item.Resolution != 0 {
		fmt.Fprintln(w, "Resolution:", apiview.Resolution(item.Resolution))
	}
	if description != "" {
		fmt.Fprintln(w, "Description:", description)
	}
	if item.Repository != nil {
		fmt.Fprintf(w, "Repository: %s (%s)\n", item.Repository.Name, item.Repository.Id)
	}
	fmt.Fprintln(w, "Created from commit:", item.CreatedFromCommit)
	if checkout := item.Checkout; checkout != nil {
		fmt.Fprintf(w, "Workspace kind: %s\nBranch: %s\n", checkout.Kind, checkout.Branch)
		if checkout.Claim == nil {
			fmt.Fprintln(w, "Checkout: absent")
		} else {
			fmt.Fprintln(w, "Checkout: present\nCheckout path:", checkout.Claim.Path)
		}
	}
	if conversation := item.Conversation; conversation != nil {
		fmt.Fprintf(w, "Primary conversation: %s (%s)\n", conversation.Id, conversation.Driver)
	}
	fmt.Fprintf(w, "Created: %s\nUpdated: %s\n", humanTime(item.CreatedAt), humanTime(item.UpdatedAt))
	return nil
}
