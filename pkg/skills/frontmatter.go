package skills

import (
	"fmt"
	"strings"
)

// frontmatter is the stdlib-only YAML-subset parse of a SKILL.md
// front-matter block. R-25 forbids a YAML dependency in pkg/skills, so
// this parser supports exactly the shapes the three dialects emit and
// nothing else:
//
//   - top-level scalar `key: value` (inline, single-line)
//   - folded/literal block scalars `key: >` / `key: |` (following
//     more-indented lines joined with space / newline)
//   - simple dash lists (`key:` followed by more-indented `- item` lines)
//
// Deeper nesting (maps, nested lists) is SKIPPED, never misread: only
// lines at indent 0 open a key, so a nested `metadata:` map cannot leak
// into a scalar. Unknown keys are preserved in Extra, never dropped
// silently (T-P1.08.6 named-gap rule).
type frontmatter struct {
	Scalars map[string]string
	Lists   map[string][]string
	// Maps holds one-level `key: value` blocks (e.g. HelixCode `variables:`).
	Maps map[string]map[string]string
}

func parseFrontmatter(block string) frontmatter {
	fm := frontmatter{
		Scalars: map[string]string{},
		Lists:   map[string][]string{},
		Maps:    map[string]map[string]string{},
	}
	var foldKey string
	var foldStyle byte
	var foldAcc []string
	var blockKey string
	blockIsMap := false
	commitFold := func() {
		if foldKey == "" {
			return
		}
		if foldStyle == '>' {
			fm.Scalars[foldKey] = strings.Join(foldAcc, " ")
		} else {
			fm.Scalars[foldKey] = strings.Join(foldAcc, "\n")
		}
		foldKey = ""
		foldAcc = nil
	}
	for _, raw := range strings.Split(block, "\n") {
		line := strings.TrimRight(raw, " \t\r")
		if line == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if indent > 0 {
			// Continuation of a block scalar, a list item, or a map entry.
			if foldKey != "" {
				foldAcc = append(foldAcc, strings.TrimSpace(trimmed))
				continue
			}
			if blockKey != "" && !blockIsMap && strings.HasPrefix(trimmed, "- ") {
				v := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
				fm.Lists[blockKey] = append(fm.Lists[blockKey], v)
				continue
			}
			if blockKey != "" {
				if k, v, ok := strings.Cut(trimmed, ":"); ok && !strings.HasPrefix(trimmed, "- ") {
					k, v = strings.TrimSpace(k), strings.TrimSpace(v)
					if k != "" {
						blockIsMap = true
						if fm.Maps[blockKey] == nil {
							fm.Maps[blockKey] = map[string]string{}
						}
						fm.Maps[blockKey][k] = unquote(v)
						continue
					}
				}
			}
			// Deeper nesting or anything else: skip, never misread.
			continue
		}
		commitFold()
		blockKey = ""
		blockIsMap = false
		key, val, ok := strings.Cut(trimmed, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key == "" {
			continue
		}
		switch val {
		case ">", "|":
			foldKey, foldStyle = key, val[0]
			fm.Scalars[key] = ""
		case "":
			blockKey = key
			if _, seen := fm.Lists[key]; !seen {
				fm.Lists[key] = []string{}
			}
		default:
			fm.Scalars[key] = unquote(val)
		}
	}
	commitFold()
	return fm
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// knownKeys partitions parsed keys into the canonical model. Anything
// outside the known set is returned as extra (named, never dropped).
func (fm frontmatter) partition() (scalars map[string]string, lists map[string][]string, maps map[string]map[string]string, extra map[string]string) {
	scalars, lists, maps, extra = map[string]string{}, map[string][]string{}, map[string]map[string]string{}, map[string]string{}
	for k, v := range fm.Scalars {
		switch k {
		case "name", "description", "version", "license", "allowed-tools", "author", "category", "requires_isolation":
			scalars[k] = v
		default:
			extra[k] = v
		}
	}
	for k, v := range fm.Lists {
		switch k {
		case "triggers", "tags", "capabilities":
			lists[k] = v
		default:
			for _, item := range v {
				extra[k+"/"+item] = item
			}
		}
	}
	for k, v := range fm.Maps {
		switch k {
		case "variables":
			maps[k] = v
		default:
			for mk, mv := range v {
				extra[k+"/"+mk] = mv
			}
		}
	}
	// A block key that stayed empty (neither list items nor map entries
	// followed) leaves an empty list entry behind; drop those.
	for k, v := range lists {
		if len(v) == 0 {
			delete(lists, k)
		}
	}
	return scalars, lists, maps, extra
}

var errNoFrontmatter = fmt.Errorf("skills: SKILL.md has no YAML front-matter block")
