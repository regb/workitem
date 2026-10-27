package view

import (
	"sort"
	"strings"
	"unicode"

	"github.com/regb/workitem/internal/model"
)

// Search fields, from the strongest match to the weakest.
const (
	SearchFieldTitle       = "title"
	SearchFieldLabels      = "labels"
	SearchFieldDescription = "description"
	SearchFieldNotes       = "notes"
)

var searchFields = []string{SearchFieldTitle, SearchFieldLabels, SearchFieldDescription, SearchFieldNotes}

// DefaultSearchLimit is how many results a search returns unless asked.
const DefaultSearchLimit = 50

// excerptRunes is about how much text an excerpt shows around a match.
const excerptRunes = 140

// SearchText is an item's searchable text kept outside its manifest: the
// description and the notes log, attachment names included.
type SearchText struct {
	Description string
	Notes       []string
}

type SearchOptions struct {
	Query string
	// State limits results to backlog, active or closed items; empty
	// searches all of them.
	State string
	Limit int
}

// SearchHit is a matching item, with the strongest field it matched in and,
// for longer text, an excerpt around the match.
type SearchHit struct {
	Item
	Field   string `json:"field"`
	Excerpt string `json:"excerpt,omitempty"`
}

type SearchResult struct {
	Query    string      `json:"query"`
	Items    []SearchHit `json:"items"`
	Total    int         `json:"total"`
	Warnings []string    `json:"warnings"`
}

// Search finds items whose title, slug, labels, description or notes
// contain every term of the query, ignoring case. A quoted phrase is one
// term. Results rank by the strongest field any term matched in, then by
// most recent update. text reads an item's description and notes, and is
// asked only for items the title and labels did not already decide.
func (s *Service) Search(opts SearchOptions, folders int, text func(model.Manifest) SearchText) SearchResult {
	terms := SearchTerms(opts.Query)
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	result := SearchResult{Query: opts.Query, Items: []SearchHit{}, Warnings: []string{}}
	if len(terms) == 0 {
		return result
	}
	manifests, errs := s.store.ListManifests()
	for _, err := range errs {
		result.Warnings = append(result.Warnings, err.Error())
	}
	hits := []SearchHit{}
	rank := map[string]int{}
	for _, m := range manifests {
		if opts.State != "" && m.State != opts.State {
			continue
		}
		fields := map[string]string{
			SearchFieldTitle:  fold(m.Title + " " + m.Slug),
			SearchFieldLabels: fold(strings.Join(m.Labels, " ")),
		}
		original := map[string]string{}
		// Description and notes are read only when the title and labels
		// leave a term unmatched, or to place an excerpt.
		if !allIn(terms, fields[SearchFieldTitle], fields[SearchFieldLabels]) {
			extra := text(m)
			original[SearchFieldDescription] = extra.Description
			original[SearchFieldNotes] = strings.Join(extra.Notes, "\n")
			fields[SearchFieldDescription] = fold(original[SearchFieldDescription])
			fields[SearchFieldNotes] = fold(original[SearchFieldNotes])
		}
		values := make([]string, 0, len(fields))
		for _, value := range fields {
			values = append(values, value)
		}
		if !allIn(terms, values...) {
			continue
		}
		hit := SearchHit{Item: makeItem(m, false, folders)}
		for index, field := range searchFields {
			if anyIn(terms, fields[field]) {
				hit.Field = field
				rank[m.ID] = index
				break
			}
		}
		if hit.Field == SearchFieldDescription || hit.Field == SearchFieldNotes {
			hit.Excerpt = excerpt(original[hit.Field], terms)
		}
		hits = append(hits, hit)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if rank[hits[i].ID] != rank[hits[j].ID] {
			return rank[hits[i].ID] < rank[hits[j].ID]
		}
		return newer(hits[i].Item, hits[j].Item)
	})
	result.Total = len(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}
	result.Items = hits
	return result
}

// SearchTerms splits a query into lowercase terms; a quoted phrase stays
// one term.
func SearchTerms(query string) []string {
	terms := []string{}
	for index, part := range strings.Split(query, `"`) {
		if index%2 == 1 {
			if phrase := strings.Join(strings.Fields(fold(part)), " "); phrase != "" {
				terms = append(terms, phrase)
			}
			continue
		}
		terms = append(terms, strings.Fields(fold(part))...)
	}
	return terms
}

// fold lowercases text and collapses its whitespace, so phrases match
// across line breaks.
func fold(text string) string { return strings.Join(strings.Fields(strings.ToLower(text)), " ") }

func allIn(terms []string, values ...string) bool {
	for _, term := range terms {
		if !anyContains(values, term) {
			return false
		}
	}
	return true
}

func anyIn(terms []string, value string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func anyContains(values []string, term string) bool {
	for _, value := range values {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

// excerpt shows the text around the first match of any term, on one line,
// with an ellipsis where it is cut.
func excerpt(text string, terms []string) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	// Lowercased rune by rune, so indexes stay those of the text.
	lower := make([]rune, len(runes))
	for i, r := range runes {
		lower[i] = unicode.ToLower(r)
	}
	at := -1
	for _, term := range terms {
		if index := runeIndex(lower, []rune(term)); index >= 0 && (at < 0 || index < at) {
			at = index
		}
	}
	if at < 0 {
		at = 0
	}
	start := max(0, at-excerptRunes/3)
	end := min(len(runes), start+excerptRunes)
	// Cut on word boundaries.
	for start > 0 && start < at && !unicode.IsSpace(runes[start-1]) {
		start++
	}
	for end < len(runes) && end > at && !unicode.IsSpace(runes[end]) {
		end--
	}
	shown := strings.TrimSpace(string(runes[start:end]))
	if start > 0 {
		shown = "…" + shown
	}
	if end < len(runes) {
		shown += "…"
	}
	return shown
}

func runeIndex(text, term []rune) int {
	for i := 0; i+len(term) <= len(text); i++ {
		if string(text[i:i+len(term)]) == string(term) {
			return i
		}
	}
	return -1
}
