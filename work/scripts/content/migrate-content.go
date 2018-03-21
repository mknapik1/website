//usr/bin/env go run "$0" "$@"; exit "$?"

package main

import (
	"log"
	"regexp"

	"flag"
	"io"
	"os"
	"path/filepath"

	"github.com/hacdias/fileutils"

	"github.com/olekukonko/tablewriter"
)

type migrator struct {
	// Test run.
	try bool

	changeLogFromTo []string

	projectRoot string
}

func newMigrator(root string) *migrator {
	return &migrator{projectRoot: root}
}

func (m *migrator) contentPath() string {
	return filepath.Join(m.projectRoot, "content")
}

func (m *migrator) logChange(from, to string) {
	m.changeLogFromTo = append(m.changeLogFromTo, from, to)
}

func (m *migrator) contentMoveStep1() error {
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

	return nil
}

func (m *migrator) renameContentFiles(match, renameTo string) error {
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

func (m *migrator) doWithContentFile(subfolder string, f func(path string, info os.FileInfo) error) error {
	docsPath := filepath.Join(m.projectRoot, "content", subfolder)
	return filepath.Walk(docsPath, func(path string, info os.FileInfo, err error) error {
		return f(path, info)
	})
}

func (m *migrator) copyDir(from, to string) error {
	from, to = m.absFromTo(from, to)

	m.logChange(from, to)
	if m.try {
		return nil
	}

	return fileutils.CopyDir(from, to)
}

func (m *migrator) moveDir(from, to string) error {
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

func (m *migrator) absFromTo(from, to string) (string, string) {
	from = filepath.Join(m.projectRoot, from)
	to = filepath.Join(m.projectRoot, to)
	if len(from) < 20 || len(to) < 20 {
		panic("path too short")
	}
	return from, to
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

func (m *migrator) printStats(w io.Writer) {
	table := tablewriter.NewWriter(w)
	for i := 0; i < len(m.changeLogFromTo); i += 2 {
		table.Append([]string{m.changeLogFromTo[i], m.changeLogFromTo[i+1]})
	}
	table.SetHeader([]string{"From", "To"})
	table.SetBorder(false)
	table.Render()
}
