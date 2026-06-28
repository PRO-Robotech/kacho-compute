// Copyright (c) PRO-Robotech
// SPDX-License-Identifier: BUSL-1.1

// Package commentlint содержит регрессионный страж дисциплины комментариев:
// комментарии исходного кода описывают бизнес-задачу и принцип работы, а не
// внутренний процесс разработки и не сторонние облачные продукты.
package commentlint

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// forbidden — запрещённые в тексте комментариев фрагменты: упоминания сторонних
// облаков и процессный шум (трекерные номера, маркеры фаз/итераций, ссылки на
// служебные документы). Машинные директивы под действие стража не попадают.
var forbidden = []struct {
	name string
	re   *regexp.Regexp
}{
	// Сторонние облака — модель описывается только в терминах Kachō.
	{"foreign-cloud:yandex", regexp.MustCompile(`(?i)yandex`)},
	{"foreign-cloud:verbatim", regexp.MustCompile(`(?i)verbatim`)},
	{"foreign-cloud:ycloud", regexp.MustCompile(`\bYC\b`)},
	{"foreign-cloud:aws", regexp.MustCompile(`\bAWS\b`)},
	{"foreign-cloud:eni", regexp.MustCompile(`\bENI\b`)},
	{"foreign-cloud:gcp", regexp.MustCompile(`(?i)\bgcp\b`)},
	{"foreign-cloud:azure", regexp.MustCompile(`(?i)\bazure\b`)},
	{"foreign-cloud:kubeovn", regexp.MustCompile(`(?i)kube-ovn`)},
	{"foreign-cloud:netbox", regexp.MustCompile(`(?i)netbox`)},
	// Процессный шум.
	{"process:tracker-id", regexp.MustCompile(`KAC-\d`)},
	{"process:issue-ref", regexp.MustCompile(`#\d`)},
	{"process:sub-phase", regexp.MustCompile(`(?i)sub-phase`)},
	{"process:phase", regexp.MustCompile(`(?i)\bphase\b`)},
	{"process:wave", regexp.MustCompile(`(?i)\bwave\b`)},
	{"process:sec-marker", regexp.MustCompile(`SEC-[A-Z]`)},
	{"process:stage", regexp.MustCompile(`(?i)\bstage\b`)},
	{"process:section-sign", regexp.MustCompile(`§`)},
	{"process:skill-ref", regexp.MustCompile(`(?i)skill evgeniy`)},
	{"process:claude-md", regexp.MustCompile(`CLAUDE\.md`)},
	{"process:todo", regexp.MustCompile(`\bTODO\b`)},
	{"process:fixme", regexp.MustCompile(`\bFIXME\b`)},
	{"process:xxx", regexp.MustCompile(`\bXXX\b`)},
	{"process:finding", regexp.MustCompile(`(?i)находка`)},
}

// machineDirective — компиляторные/линтерные директивы (не комментарии по сути);
// их текст нормативно сохраняется байт-в-байт, поэтому страж их пропускает.
var machineDirective = regexp.MustCompile(`^//(go:|nolint|lint:ignore|\s*\+build)`)

// repoRoot вычисляет корень репозитория относительно расположения этого файла
// (internal/commentlint).
func repoRoot(t *testing.T) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(self), "..", "..")
}

// TestCommentsAreCleanOfProcessNoiseAndForeignClouds обходит комментарии всех
// .go-файлов в internal/ и cmd/ и требует отсутствия запрещённых фрагментов.
func TestCommentsAreCleanOfProcessNoiseAndForeignClouds(t *testing.T) {
	root := repoRoot(t)
	var offenders []string
	for _, dir := range []string{"internal", "cmd"} {
		walkErr := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			fset := token.NewFileSet()
			f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if perr != nil {
				return perr
			}
			for _, cg := range f.Comments {
				for _, c := range cg.List {
					if machineDirective.MatchString(c.Text) {
						continue
					}
					for _, p := range forbidden {
						if p.re.MatchString(c.Text) {
							pos := fset.Position(c.Pos())
							rel, _ := filepath.Rel(root, path)
							offenders = append(offenders, rel+":"+itoa(pos.Line)+" ["+p.name+"]")
						}
					}
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", dir, walkErr)
		}
	}
	if len(offenders) > 0 {
		t.Fatalf("найдены запрещённые фрагменты в комментариях (%d):\n%s",
			len(offenders), strings.Join(offenders, "\n"))
	}
}

// itoa — минимальная замена strconv.Itoa, чтобы не тянуть лишний импорт в страж.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
