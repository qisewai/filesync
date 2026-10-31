package main

import (
	"cmp"
	"path"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"filemanager/remoteclient"

	"github.com/egoist/mygo/ui"
)

// tableSort is the column a table's rows are sorted by, which a click on
// its header changes, and the order the rows were last put in.
type tableSort struct {
	revision    uint64
	by, applied ui.SortOrder
	group       fileGrouping
}

// stale reports whether the rows are not yet in the order asked.
func (s *tableSort) stale() bool { return s.by != s.applied }

// sortRows puts rows in the order s asks, comparing them with compare by
// column, and keeps the row cursor shows on it, by key.
func sortRows[T any](s *tableSort, rows []T, cursor *int, key func(T) any, compare func(x, y T, column string) int) {
	s.applied = s.by
	if s.by.Column == "" {
		return
	}
	var at any
	if cursor != nil && *cursor >= 0 && *cursor < len(rows) {
		at = key(rows[*cursor])
	}
	slices.SortStableFunc(rows, func(x, y T) int {
		c := compare(x, y, s.by.Column)
		if s.by.Descending {
			c = -c
		}
		return c
	})
	if at != nil {
		*cursor = slices.IndexFunc(rows, func(r T) bool { return key(r) == at })
	}
}

// The columns of the tables of files, which sort them.
const (
	colName  = "名称"
	colSize  = "大小"
	colType  = "类型"
	colTime  = "修改时间"
	colWhere = "位置"
)

// compareEntries orders files by a column, directories before files
// whichever the direction, then by name.
func compareEntries(x, y fileindex.Entry, column string, desc bool) int {
	if x.IsDir != y.IsDir {
		// Before the sort reverses it, when it does: directories stay first.
		first := -1
		if desc {
			first = 1
		}
		if x.IsDir {
			return first
		}
		return -first
	}
	var c int
	switch column {
	case colSize:
		c = cmp.Compare(x.Size, y.Size)
	case colTime:
		c = cmp.Compare(x.MTime, y.MTime)
	case colType:
		c = strings.Compare(fileExtension(x.Name), fileExtension(y.Name))
	case colWhere:
		c = naturalCompare(path.Join(x.RootName, x.Parent), path.Join(y.RootName, y.Parent))
	}
	if c == 0 {
		c = naturalCompare(x.Name, y.Name)
	}
	return c
}

// sortEntries puts files in the order s asks, directories first.
func sortEntries(s *tableSort, es []fileindex.Entry, cursor *int) {
	s.revision++
	desc := s.by.Descending
	sortRows(s, es, cursor, func(e fileindex.Entry) any { return entryKey(e) }, func(x, y fileindex.Entry, column string) int {
		return compareEntries(x, y, column, desc)
	})
}

func entryKey(e fileindex.Entry) string { return path.Join(e.RootName, e.Path) + "\x00" + e.Name }

// naturalCompare orders names as people do: without case, and the numbers
// in them by value, so that 文件2 comes before 文件10.
func naturalCompare(a, b string) int {
	for a != "" && b != "" {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if isDigit(ra) && isDigit(rb) {
			da, db := digits(a), digits(b)
			ta, tb := strings.TrimLeft(da, "0"), strings.TrimLeft(db, "0")
			if c := cmp.Compare(len(ta), len(tb)); c != 0 {
				return c
			}
			if c := strings.Compare(ta, tb); c != 0 {
				return c
			}
			a, b = a[len(da):], b[len(db):]
			continue
		}
		if c := cmp.Compare(unicode.ToLower(ra), unicode.ToLower(rb)); c != 0 {
			return c
		}
		a, b = a[na:], b[nb:]
	}
	return cmp.Compare(len(a), len(b))
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// digits is the run of ASCII digits s starts with.
func digits(s string) string {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}
