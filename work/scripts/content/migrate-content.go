//usr/bin/env go run "$0" "$@"; exit "$?"

package main

import (
	"log"
	"regexp"

	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hacdias/fileutils"

	"github.com/olekukonko/tablewriter"
)

type keyVal struct {
	key string
	val string
}

var (
	frontmatterRe = regexp.MustCompile(`(?s)---
(.*)
---(\n?)`)
)

type mover struct {
	// Test run.
	try bool

	changeLogFromTo []string

	projectRoot string
}

func newMigrator(root string) *mover {
	return &mover{projectRoot: root}
}

func (m *mover) contentPath() string {
	return filepath.Join(m.projectRoot, "content")
}

func (m *mover) logChange(from, to string) {
	m.changeLogFromTo = append(m.changeLogFromTo, from, to)
}

func (m *mover) contentMoveStep1() error {
	// Copy main content to content/en
	if err := m.copyDir("docs", "content/en/docs"); err != nil {
		return err
	}
	// Copy Chinese content to content/cn
	if err := m.copyDir("cn/docs", "content/cn/docs"); err != nil {
		return err
	}

	// Move generated content to /static
	if err := m.moveDir("content/en/docs/reference/generated", "static/reference/generated"); err != nil {
		return err
	}

	// Create proper Hugo sections
	if err := m.renameContentFiles("index\\.md$", "_index.md"); err != nil {
		return err
	}

	// Copy additional content files from the work dir.
	if err := m.copyDir("work/content", "content"); err != nil {
		return err
	}

	// Adjust link titles
	linkTitles := []keyVal{
		keyVal{"en/docs/home/_index.md", "Home"},
		keyVal{"en/docs/reference/_index.md", "Reference"},
	}

	for _, title := range linkTitles {
		if err := m.replaceInFile(filepath.Join("content", title.key), addLinkTitle(title.val)); err != nil {
			return err
		}
	}

	filesInDocsMainMenu := []string{
		"en/docs/home/_index.md",
		"en/docs/setup/_index.md",
		"en/docs/concepts/_index.md",
		"en/docs/tasks/_index.md",
		"en/docs/tutorials/_index.md",
		"en/docs/reference/_index.md",
	}

	for i, f := range filesInDocsMainMenu {
		weight := 20 + (i * 10)
		if err := m.replaceInFile(filepath.Join("content", f), addToDocsMainMenu(weight)); err != nil {
			return err
		}
	}

	// Adjust some layouts
	if err := m.replaceInFile(filepath.Join("content", "en/docs/home/_index.md"), stringsReplacer("layout: docsportal", "layout: docsportal_home")); err != nil {
		return err
	}

	return nil
}

func (m *mover) renameContentFiles(match, renameTo string) error {
	re := regexp.MustCompile(match)
	return m.doWithContentFile("", func(path string, info os.FileInfo) error {
		if !info.IsDir() && re.MatchString(path) {
			dir := filepath.Dir(path)
			targetFilename := filepath.Join(dir, renameTo)
			m.logChange(path, targetFilename)
			if !m.try {
				return os.Rename(path, targetFilename)
			}
		}

		return nil
	})
}

func (m *mover) doWithContentFile(subfolder string, f func(path string, info os.FileInfo) error) error {
	docsPath := filepath.Join(m.projectRoot, "content", subfolder)
	return filepath.Walk(docsPath, func(path string, info os.FileInfo, err error) error {
		return f(path, info)
	})
}

func (m *mover) copyDir(from, to string) error {
	from, to = m.absFromTo(from, to)

	m.logChange(from, to)
	if m.try {
		return nil
	}

	return fileutils.CopyDir(from, to)
}

func (m *mover) moveDir(from, to string) error {
	from, to = m.absFromTo(from, to)
	m.logChange(from, to)
	if m.try {
		return nil
	}

	if err := os.RemoveAll(to); err != nil {
		return err
	}

	return os.Rename(from, to)

}

func (m *mover) absFromTo(from, to string) (string, string) {
	return m.absFilename(from), m.absFilename(to)
}

func (m *mover) absFilename(name string) string {
	abs := filepath.Join(m.projectRoot, name)
	if len(abs) < 20 {
		panic("path too short")
	}
	return abs
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("content migrator: ")
	pwd, err := os.Getwd()
	if err != nil {
		log.Fatal("error:", err)
	}

	m := newMigrator(filepath.Join(pwd, "../../../"))
	flag.BoolVar(&m.try, "try", false, "trial run, no updates")

	flag.Parse()

	// During development.
	m.try = false

	if m.try {
		log.Println("trial mode on")
	}
	must(m.contentMoveStep1())

	m.printStats(os.Stdout)

}

func must(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

func (m *mover) printStats(w io.Writer) {
	table := tablewriter.NewWriter(w)
	for i := 0; i < len(m.changeLogFromTo); i += 2 {
		table.Append([]string{m.changeLogFromTo[i], m.changeLogFromTo[i+1]})
	}
	table.SetHeader([]string{"From", "To"})
	table.SetBorder(false)
	table.Render()
}

func (m *mover) openOrCreateTargetFile(target string, info os.FileInfo) (io.ReadWriteCloser, error) {
	targetDir := filepath.Dir(target)

	err := os.MkdirAll(targetDir, os.FileMode(0755))
	if err != nil {
		return nil, err
	}

	return m.openFileForWriting(target, info)
}

func (m *mover) openFileForWriting(filename string, info os.FileInfo) (io.ReadWriteCloser, error) {
	return os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
}

func (m *mover) handleFile(name string, create bool, info os.FileInfo, replacer func(path string, content string) (string, error)) error {
	sourceFilename := m.absFilename(name)

	var (
		out io.ReadWriteCloser
		in  bytes.Buffer
		err error
	)

	infile, err := os.Open(sourceFilename)
	if err != nil {
		return err
	}
	in.ReadFrom(infile)
	infile.Close()

	if create {
		out, err = m.openOrCreateTargetFile(sourceFilename, info)
	} else {
		out, err = m.openFileForWriting(sourceFilename, info)
	}

	if err != nil {
		return err
	}
	defer out.Close()

	return m.replace(name, &in, out, replacer)
}

func (m *mover) replace(path string, in io.Reader, out io.Writer, replacer func(path string, content string) (string, error)) error {
	var buff bytes.Buffer
	if _, err := io.Copy(&buff, in); err != nil {
		return err
	}

	var r io.Reader

	fixed, err := replacer(path, buff.String())
	if err != nil {
		fmt.Printf("%s\t%s\n", path, err)
		r = &buff
	} else {
		r = strings.NewReader(fixed)
	}

	if _, err = io.Copy(out, r); err != nil {
		return err
	}
	return nil
}

func (m *mover) replaceInFile(filename string, replacer func(path string, content string) (string, error)) error {
	fi, err := os.Stat(m.absFilename(filename))
	if err != nil {
		return err
	}
	return m.handleFile(filename, false, fi, replacer)
}

func addToDocsMainMenu(weight int) func(path, s string) (string, error) {
	return func(path, s string) (string, error) {
		return appendToFrontMatter(s, fmt.Sprintf(`menu:
  docsmain:
    weight: %d`, weight)), nil
	}

}

func addLinkTitle(title string) func(path, s string) (string, error) {
	return func(path, s string) (string, error) {
		return appendToFrontMatter(s, fmt.Sprintf("linkTitle: %q", title)), nil
	}
}

func appendToFrontMatter(src, addition string) string {
	return frontmatterRe.ReplaceAllString(src, fmt.Sprintf(`---
$1
%s
---$2`, addition))

}

func stringsReplacer(old, new string) func(path, s string) (string, error) {
	return func(path, s string) (string, error) {
		return strings.Replace(s, old, new, -1), nil
	}

}
