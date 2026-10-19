package workrun

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var feedbackCommentID = regexp.MustCompile(`^(inline|review|comment):[0-9]+$`)

// GitHub issue comments have no reply metadata. Only explicit links within this
// PR and unambiguous whole-comment quotes establish a parent relationship.
func linkFeedbackConversations(comments []PRFeedbackComment, prURL string) {
	links := regexp.MustCompile(regexp.QuoteMeta(prURL) + `#(issuecomment-|pullrequestreview-|discussion_r)([0-9]+)`)
	for i := range comments {
		c := &comments[i]
		if c.Kind == "inline" {
			continue
		}
		parents := map[string]bool{}
		for _, match := range links.FindAllStringSubmatch(c.Body, -1) {
			kind := "comment"
			if match[1] == "pullrequestreview-" {
				kind = "review"
			}
			if match[1] == "discussion_r" {
				kind = "inline"
			}
			parents[kind+":"+match[2]] = true
		}
		var blocks []string
		var block []string
		flush := func() {
			if len(block) > 0 {
				blocks = append(blocks, strings.Join(block, "\n"))
				block = nil
			}
		}
		for _, line := range strings.Split(strings.ReplaceAll(c.Body, "\r\n", "\n"), "\n") {
			line = strings.TrimLeft(line, " \t")
			if strings.HasPrefix(line, ">") {
				block = append(block, strings.TrimPrefix(strings.TrimPrefix(line, ">"), " "))
			} else {
				flush()
			}
		}
		flush()
		for _, quote := range blocks {
			var matches []string
			for _, parent := range comments {
				if parent.ID != c.ID && strings.TrimSpace(parent.Body) != "" && normaliseFeedbackQuote(parent.Body) == normaliseFeedbackQuote(quote) {
					matches = append(matches, parent.ID)
				}
			}
			if len(matches) == 1 {
				parents[matches[0]] = true
			}
		}
		for _, parent := range comments {
			if parent.ID != c.ID && parents[parent.ID] {
				c.ParentIDs = append(c.ParentIDs, parent.ID)
			}
		}
		sort.Strings(c.ParentIDs)
	}
}
func normaliseFeedbackQuote(s string) string { return strings.Join(strings.Fields(s), " ") }

func inheritFeedbackIgnores(comments []PRFeedbackComment) {
	byID := map[string]int{}
	for i, c := range comments {
		byID[c.ID] = i
	}
	// A fixed point handles arbitrarily ordered and nested replies without recursion.
	for changed := true; changed; {
		changed = false
		for i := range comments {
			c := &comments[i]
			if c.Ignored != nil {
				continue
			}
			for _, parent := range c.ParentIDs {
				if index, ok := byID[parent]; ok && comments[index].Ignored != nil {
					inherited := *comments[index].Ignored
					if inherited.SourceCommentID == "" {
						inherited.SourceCommentID = parent
					}
					c.Ignored = &inherited
					changed = true
					break
				}
			}
		}
	}
}

// An operator rocket closes the selected comment and all explicit descendants.
func inheritFeedbackAddressed(comments []PRFeedbackComment) {
	byID := map[string]int{}
	for i, c := range comments {
		byID[c.ID] = i
	}
	// A fixed point handles arbitrarily ordered and nested replies without recursion.
	for changed := true; changed; {
		changed = false
		for i := range comments {
			c := &comments[i]
			if c.Addressed != nil {
				continue
			}
			for _, parent := range c.ParentIDs {
				if index, ok := byID[parent]; ok && comments[index].Addressed != nil {
					inherited := *comments[index].Addressed
					if inherited.SourceCommentID == "" {
						inherited.SourceCommentID = parent
					}
					c.Addressed = &inherited
					changed = true
					break
				}
			}
		}
	}
}

type feedbackConversation struct {
	Comments []PRFeedbackComment `json:"comments"`
}

// Include the latest active parent and siblings even when only a new reply is
// pending. Previously assessed input remains context, not a new assessment task.
func feedbackConversations(history *PRFeedbackHistory) []feedbackConversation {
	if history == nil {
		return nil
	}
	current := map[string]PRFeedbackComment{}
	for _, e := range history.Entries {
		current[e.Comment.ID] = e.Comment
	}
	active := map[string]PRFeedbackComment{}
	for id, c := range current {
		if !c.Deleted && !c.Resolved && c.Ignored == nil {
			active[id] = c
		}
	}
	groups := map[string]string{}
	for id := range active {
		groups[id] = id
	}
	for changed := true; changed; {
		changed = false
		for id, c := range active {
			for _, parent := range c.ParentIDs {
				if root, ok := groups[parent]; ok && groups[id] != root {
					a, b := groups[id], root
					if a > b {
						a, b = b, a
					}
					for key, value := range groups {
						if value == b {
							groups[key] = a
							changed = true
						}
					}
				}
			}
		}
	}
	keys := []string{}
	for id := range active {
		keys = append(keys, id)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := strings.SplitN(keys[i], ":", 2), strings.SplitN(keys[j], ":", 2)
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		ai, _ := strconv.ParseUint(a[1], 10, 64)
		bi, _ := strconv.ParseUint(b[1], 10, 64)
		if ai == bi {
			return keys[i] < keys[j]
		}
		return ai < bi
	})
	result := []feedbackConversation{}
	indexes := map[string]int{}
	for _, id := range keys {
		root := groups[id]
		index, ok := indexes[root]
		if !ok {
			index = len(result)
			indexes[root] = index
			result = append(result, feedbackConversation{})
		}
		result[index].Comments = append(result[index].Comments, active[id])
	}
	return result
}
