//usr/bin/env go run "$0" "$@"; exit "$?"

package main

import (
	"io/ioutil"
	"log"
	"regexp"

	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cast"

	"gopkg.in/yaml.v2"

	"github.com/hacdias/fileutils"

	"github.com/olekukonko/tablewriter"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("k8cm: ")
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

	// Start fresh
	if err := os.RemoveAll(m.absFilename("content")); err != nil {
		log.Fatal(err)
	}

	// Copies the content files into the new Hugo content roots and do basic
	// renaming of some files to match Hugo's standard.
	must(m.contentMigrate_Step1_Basic_Copy_And_Rename())

	must(m.contentMigrate_CreateGlossaryFromData())

	// Do all replacements needed in the content files:
	// * Add menu config
	// * Replace inline Liquid with shortcodes
	// * Etc.
	must(m.contentMigrate_Replacements())

	// Creates sections for Toc/Menus
	must(m.contentMigrate_CreateSections())

	// Copy in some content that failed in the steps above etc.
	must(m.contentMigrate_Final_Step())

	if m.try {
		m.printStats(os.Stdout)
	}

	log.Println("Done.")

}

type keyVal struct {
	key string
	val string
}

type contentFixer func(path, s string) (string, error)
type contentFixers []contentFixer

func (f contentFixers) fix(path, s string) (string, error) {
	var err error
	for _, fixer := range f {
		s, err = fixer(path, s)
		if err != nil {
			return "", err
		}
	}
	return s, nil
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

	addedFiles map[string]bool
	tocDirs    map[string]bool
}

func newMigrator(root string) *mover {
	return &mover{projectRoot: root, addedFiles: make(map[string]bool), tocDirs: make(map[string]bool)}
}

func (m *mover) contentMigrate_Step1_Basic_Copy_And_Rename() error {

	log.Println("Start Step 1 …")

	// Copy main content to content/en
	if err := m.copyDir("docs", "content/en/docs"); err != nil {
		return err
	}

	// Copy blog content to content/en
	if err := m.copyDir("blog", "content/en/blog"); err != nil {
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

	if err := m.renameContentFiles("doc.*index\\.html$", "_index.html"); err != nil {
		return err
	}

	// We are going to replce this later, but just make sure it gets the name correctly.
	if err := m.renameContentFile("content/en/blog/index.html", "content/en/blog/_index.md"); err != nil {
		return err
	}

	return nil
}

func (m *mover) contentMigrate_CreateGlossaryFromData() error {
	mm, err := m.readDataDir("glossary_remove", func() interface{} { return make(map[string]interface{}) })
	if err != nil {
		return err
	}

	glossaryDir := m.absFilename("content/en/docs/reference/glossary")

	if err := os.MkdirAll(glossaryDir, os.FileMode(0755)); err != nil && !os.IsNotExist(err) {
		return err
	}

	// Add a bundle index page.
	filename := filepath.Join(glossaryDir, "index.md")
	if err := ioutil.WriteFile(filename, []byte(`---
approvers:
- chenopis
- abiogenesis-now
title: Standardized Glossary
layout: glossary
noedit: true
default_active_tag: fundamental
weight: 5
---

`), os.FileMode(0755)); err != nil {
		return err
	}

	for k, v := range mm {
		if k == "_example" {
			continue
		}

		// Create pages in content/en/docs/reference/glossary for every entry.

		vv := cast.ToStringMap(v)

		name := vv["name"]
		id := vv["id"]
		shortDesc := cast.ToString(vv["short-description"])
		longDesc := cast.ToString(vv["long-description"])
		fullLink := cast.ToString(vv["full-link"])
		aka := cast.ToString(vv["aka"])
		tags := cast.ToStringSlice(vv["tags"])
		tagsStr := ""
		for _, tag := range tags {
			tagsStr = tagsStr + "- " + tag + "\n"
		}
		tagsStr = strings.TrimSpace(tagsStr)

		filename := filepath.Join(glossaryDir, fmt.Sprintf("%s.md", k))

		content := fmt.Sprintf(`---
title: %s
id: %s
date: 2018-04-12
full_link: %s
aka: %s
tags:
%s 
---
 %s
<!--more--> 

%s
`, name, id, fullLink, aka, tagsStr, shortDesc, longDesc)

		if err := ioutil.WriteFile(filename, []byte(content), os.FileMode(0755)); err != nil {
			return err
		}

	}

	return nil

}

func (m *mover) contentMigrate_CreateSections() error {
	log.Println("Start Create Sections Step …")

	// Create sections from the root nodes (the Toc) in /data
	//sectionsData := make(map[string]SectionFromData)

	mm, err := m.readDataDir("", func() interface{} { return &SectionFromData{} })
	if err != nil {
		return err
	}

	for _, vi := range mm {
		v := vi.(*SectionFromData)

		for i, tocEntry := range v.Toc {
			switch v := tocEntry.(type) {
			case string:
			case map[interface{}]interface{}:
				if err := m.handleTocEntryRecursive(i, cast.ToStringMap(v)); err != nil {
					return err
				}
			default:
				panic("unknown type")
			}
		}

	}

	// Mark any section not in ToC with a flag
	if err := m.doWithContentFile("en/docs", func(path string, info os.FileInfo) error {
		contenRoot := filepath.Join(m.projectRoot, "content")
		if !info.IsDir() {
			if strings.HasPrefix(info.Name(), "_index") {
				dir, _ := filepath.Split(path)
				dir = strings.TrimPrefix(dir, contenRoot)
				dir = strings.TrimPrefix(dir, string(os.PathSeparator)+"en"+string(os.PathSeparator))
				dir = strings.TrimSuffix(dir, string(os.PathSeparator))

				if !m.tocDirs[dir] {
					show := false
					for k, _ := range m.tocDirs {
						if strings.HasPrefix(k, dir) {
							show = true
							break
						}
					}
					if !show {
						if err := m.replaceInFile(path, addKeyValue("toc_hide", true)); err != nil {
							return err
						}
					}
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}

	return nil
}

func (m *mover) readDataDir(name string, createMapEntry func() interface{}) (map[string]interface{}, error) {
	dataDir := filepath.Join(m.absFilename("data"), name)
	log.Println("Read data from", dataDir)
	fd, err := os.Open(dataDir)
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	fis, err := fd.Readdir(-1)
	if err != nil {
		return nil, err
	}

	mm := make(map[string]interface{})

	for _, fi := range fis {
		if fi.IsDir() {
			continue
		}
		name := fi.Name()
		baseName := strings.TrimSuffix(name, filepath.Ext(name))
		b, err := ioutil.ReadFile(filepath.Join(dataDir, name))
		if err != nil {
			return nil, err
		}

		to := createMapEntry()

		if err := yaml.Unmarshal(b, to); err != nil {
			return nil, err
		}
		mm[baseName] = to

	}

	return mm, nil
}

type SectionFromData struct {
	Bigheader   string        `yaml:"bigheader"`
	Abstract    string        `yaml:"abstract"`
	LandingPage string        `yaml:"landing_page"`
	Toc         []interface{} `yaml:"toc"`
}

func (m *mover) handleTocEntryRecursive(sidx int, entry map[string]interface{}) error {
	title := cast.ToString(entry["title"])
	//landingPage := cast.ToString(entry["landing_page"])

	var sectionContentPageWritten bool

	sectionWeight := (sidx + 1) * 10

	if sect, found := entry["section"]; found {
		for i, e := range sect.([]interface{}) {

			switch v := e.(type) {
			case string:
				v = strings.TrimSpace(v)
				if !strings.HasPrefix(v, "docs") {
					log.Println("skip toc file:", v)
					continue
				}
				if strings.HasSuffix(v, "index.md") {
					continue
				}

				if strings.Contains(v, "generated") {
					continue
				}

				dir := filepath.Dir(v)
				m.tocDirs[dir] = true

				// 1. Create a section content file if not already written
				if !sectionContentPageWritten {
					sectionContentPageWritten = true
					// TODO(bep) cn?
					force := false
					relFilename := filepath.Join("content", "en", dir, "_index.md")
					relFilenameHTML := filepath.Join("content", "en", dir, "_index.html")
					if m.addedFiles[relFilename] {
						log.Printf("WARNING: %q section already added. Ambigous?", relFilename)
						// Use the title from the last owning folder for now.
						paths := strings.Split(filepath.Dir(relFilename), string(os.PathSeparator))
						title = paths[len(paths)-1]
						title = strings.Replace(title, "-", " ", -1)
						title = strings.Title(title)
						force = true
					}

					if force || (!m.checkRelFileExists(relFilename) && !m.checkRelFileExists(relFilenameHTML)) {
						m.addedFiles[relFilename] = true
						filename := filepath.Join(m.absFilename(relFilename))
						content := fmt.Sprintf(`---
title: %q
weight: %d
---

`, title, sectionWeight)

						if err := ioutil.WriteFile(filename, []byte(content), os.FileMode(0755)); err != nil {
							return err
						}
					}

				}

				relFilename := filepath.Join("content", "en", v)
				if !m.checkRelFileExists(relFilename) {
					log.Println("content file in toc does not exist:", relFilename)
					continue
				}
				// 2. Set a weight in the relevant content file to get proper ordering.
				if err := m.replaceInFileRel(relFilename, addWeight((i+1)*10)); err != nil {
					return err
				}

			case map[interface{}]interface{}:
				mm := cast.ToStringMap(v)

				if err := m.handleTocEntryRecursive(i, mm); err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (m *mover) contentMigrate_Replacements() error {
	log.Println("Start Replacement Step …")

	if m.try {
		// The try flag is mainly to get the first step correct before we
		// continue.
		m.logChange("All content files", "Replacements")
		return nil
	}

	// Adjust link titles
	linkTitles := []keyVal{
		keyVal{"en/docs/home/_index.md", "Home"},
		keyVal{"en/docs/reference/_index.md", "Reference"},
	}

	for _, title := range linkTitles {
		if err := m.replaceInFileRel(filepath.Join("content", title.key), addLinkTitle(title.val)); err != nil {
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
		if err := m.replaceInFileRel(filepath.Join("content", f), addToDocsMainMenu(weight)); err != nil {
			return err
		}
	}

	// Adjust some layouts
	if err := m.replaceInFileRel(filepath.Join("content", "en/docs/home/_index.md"), stringsReplacer("layout: docsportal", "layout: docsportal_home")); err != nil {
		return err
	}

	// This is replaced with a section content file
	if err := os.Remove(m.absFilename(filepath.Join("content", "en/docs/reference/glossary.md"))); err != nil {
		return err
	}

	mainContentFixSet := contentFixers{
		// This is a type, but it creates a breaking shortcode
		// {{ "{% glossary_tooltip text=" }}"cluster" term_id="cluster" %}
		func(path, s string) (string, error) {
			return strings.Replace(s, `{{ "{% glossary_tooltip text=" }}"cluster" term_id="cluster" %}`, `{% glossary_tooltip text=" term_id="cluster" %}`, 1), nil
		},

		func(path, s string) (string, error) {
			re := regexp.MustCompile(`{% glossary_tooltip(.*?)%}`)
			return re.ReplaceAllString(s, `{{< glossary_tooltip$1>}}`), nil
			return s, nil
		},

		func(path, s string) (string, error) {
			re := regexp.MustCompile(`{% glossary_definition(.*?)%}`)
			return re.ReplaceAllString(s, `{{< glossary_definition$1>}}`), nil
			return s, nil
		},

		// Code includes
		// TODO(bep) ghlink looks superflous?
		// {% include code.html file="frontend/frontend.conf" ghlink="/docs/tasks/access-application-cluster/frontend/frontend.conf" %}
		func(path, s string) (string, error) {
			re := regexp.MustCompile(`{% include code.html(.*?)%}`)
			return re.ReplaceAllString(s, `{{< code$1>}}`), nil
			return s, nil
		},

		replaceCaptures,

		calloutsToShortCodes,
	}

	if err := m.applyContentFixers(mainContentFixSet, "md$"); err != nil {
		return err
	}

	blogFixers := contentFixers{
		// Makes proper YAML dates from "Friday, July 02, 2015" etc.
		fixDates,
	}

	if err := m.applyContentFixers(blogFixers, ".*blog/.*md$"); err != nil {
		return err
	}

	// Handle the tutorial includes
	if err := m.replaceStringWithFrontMatter("{% include templates/tutorial.md %}", "content_template", "templates/tutorial"); err != nil {
		return err
	}

	return nil

}

// TODO(bep) {% include templates/user-journey-content.md %} etc.

func (m *mover) contentMigrate_Final_Step() error {
	log.Println("Start Final Step …")
	// Copy additional content files from the work dir.
	// This will in some cases revert changes made in previous steps, but
	// these are intentional.

	// These are new files.
	if err := m.copyDir("work/content", "content"); err != nil {
		return err
	}

	// These are just kept unchanged from the orignal. Needs manual handling.
	if err := m.copyDir("work/content_preserved", "content"); err != nil {
		return err
	}

	return nil
}

func (m *mover) applyContentFixers(fixers contentFixers, match string) error {
	re := regexp.MustCompile(match)
	return m.doWithContentFile("", func(path string, info os.FileInfo) error {
		if !info.IsDir() && re.MatchString(path) {
			if !m.try {
				if err := m.replaceInFile(path, fixers.fix); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (m *mover) renameContentFile(from, to string) error {
	from = m.absFilename(from)
	to = m.absFilename(to)
	return os.Rename(from, to)
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

func (m *mover) checkRelFileExists(rel string) bool {
	if _, err := os.Stat(m.absFilename(rel)); err != nil {
		if !os.IsNotExist(err) {
			panic(err)
		}
		return false
	}
	return true
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

func (m *mover) logChange(from, to string) {
	m.changeLogFromTo = append(m.changeLogFromTo, from, to)
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

func (m *mover) handleFile(filename string, create bool, info os.FileInfo, replacer func(path string, content string) (string, error)) error {

	var (
		out io.ReadWriteCloser
		in  bytes.Buffer
		err error
	)

	infile, err := os.Open(filename)
	if err != nil {
		return err
	}
	in.ReadFrom(infile)
	infile.Close()

	if create {
		out, err = m.openOrCreateTargetFile(filename, info)
	} else {
		out, err = m.openFileForWriting(filename, info)
	}

	if err != nil {
		return err
	}
	defer out.Close()

	return m.replace(filename, &in, out, replacer)
}

func (m *mover) replace(path string, in io.Reader, out io.Writer, replacer func(path string, content string) (string, error)) error {
	var buff bytes.Buffer
	if _, err := io.Copy(&buff, in); err != nil {
		return err
	}

	var r io.Reader

	fixed, err := replacer(path, buff.String())
	if err != nil {
		// Just print the path and error to the console.
		// This will have to be handled manually somehow.
		log.Printf("%s\t%s\n", path, err)
		r = &buff
	} else {
		r = strings.NewReader(fixed)
	}

	if _, err = io.Copy(out, r); err != nil {
		return err
	}
	return nil
}

func (m *mover) replaceInFileRel(rel string, replacer func(path string, content string) (string, error)) error {
	return m.replaceInFile(m.absFilename(rel), replacer)
}

func (m *mover) replaceInFile(filename string, replacer func(path string, content string) (string, error)) error {
	fi, err := os.Stat(filename)
	if err != nil {
		return err
	}
	return m.handleFile(filename, false, fi, replacer)
}

/*func addToDocsMainMenu(weight int) func(path, s string) (string, error) {
	return func(path, s string) (string, error) {
		return appendToFrontMatter(s, fmt.Sprintf(`menu:
  docsmain:
    weight: %d`, weight)), nil
	}

}*/

func (m *mover) replaceStringWithFrontMatter(matcher, key, val string) error {

	matcherRe := regexp.MustCompile(matcher)

	err := m.doWithContentFile("", func(path string, info os.FileInfo) error {
		if info != nil && !info.IsDir() {
			if !m.try {
				b, err := ioutil.ReadFile(path)
				if err != nil {
					return err
				}
				if matcherRe.Match(b) {
					if err := m.replaceInFile(path, addKeyValue(key, val)); err != nil {
						return err
					}
					if err := m.replaceInFile(path, func(path, s string) (string, error) {
						return matcherRe.ReplaceAllString(s, ""), nil
					}); err != nil {
						return err
					}
				}

			}
		}
		return nil
	})

	return err

}
func addToDocsMainMenu(weight int) func(path, s string) (string, error) {
	return func(path, s string) (string, error) {
		return appendToFrontMatter(s, fmt.Sprintf(`main_menu: true
weight: %d`, weight)), nil
	}

}

func addLinkTitle(title string) func(path, s string) (string, error) {
	return func(path, s string) (string, error) {
		return appendToFrontMatter(s, fmt.Sprintf("linkTitle: %q", title)), nil
	}
}

func addWeight(weight int) func(path, s string) (string, error) {
	return func(path, s string) (string, error) {
		return appendToFrontMatter(s, fmt.Sprintf("weight: %d", weight)), nil
	}
}

func addKeyValue(key string, value interface{}) func(path string, s string) (string, error) {
	return func(path, s string) (string, error) {
		return appendToFrontMatter(s, fmt.Sprintf("%s: %v", key, value)), nil
	}
}

func appendToFrontMatter(src, addition string) string {
	return frontmatterRe.ReplaceAllString(src, fmt.Sprintf(`---
$1
%s
---$2`, addition))

}

// TODO(bep) the below regexp seem to have missed some.
func replaceCaptures(path, s string) (string, error) {
	re := regexp.MustCompile(`(?s){% capture (.*?) %}(.*?){% endcapture %}`)
	return re.ReplaceAllString(s, `{{% capture $1 %}}$2{{% /capture %}}`), nil
}

// Introduce them little by little to test
var callouts = regexp.MustCompile("note|caution|warning")

func calloutsToShortCodes(path, s string) (string, error) {

	// This contains callout examples that needs to be handled by hand.
	// TODO(bep)
	if strings.Contains(path, "style-guide") {
		return s, nil
	}
	/*if !strings.Contains(path, "foundational") {
		return s, nil
	}*/

	if !strings.Contains(s, "{:") {
		return s, nil
	}

	calloutRe := regexp.MustCompile(`(\s*){:\s*\.(.*)}`)

	var all strings.Builder
	var current strings.Builder

	scanner := bufio.NewScanner(strings.NewReader(s))
	pcounter := 0
	var indent, shortcode string
	isOpen := false

	for scanner.Scan() {
		line := scanner.Text()
		l := strings.TrimSpace(line)
		if l == "" || l == "---" {
			pcounter = 0
		} else {
			pcounter++
		}

		// Test with the notes
		if strings.Contains(line, "{:") && callouts.MatchString(line) {
			// This may be the start or the end of a callout.
			isStart := pcounter == 1

			m := calloutRe.FindStringSubmatch(line)
			indent = m[1]
			shortcode = m[2]
			all.WriteString(fmt.Sprintf("%s{{< %s >}}\n", indent, shortcode))
			if !isStart {
				all.WriteString(current.String())
				all.WriteString(fmt.Sprintf("%s{{< /%s >}}\n", indent, shortcode))
				current.Reset()
			} else {
				isOpen = true
			}

		} else {
			current.WriteString(line)
			if !isOpen || pcounter != 0 {
				current.WriteRune('\n')
			}
			if pcounter == 0 {
				if isOpen {
					current.WriteString(fmt.Sprintf("%s{{< /%s >}}\n", indent, shortcode))
					isOpen = false
				}
				all.WriteString(current.String())
				current.Reset()
			}

		}

	}

	all.WriteString(current.String())

	return all.String(), nil

}

func stringsReplacer(old, new string) func(path, s string) (string, error) {
	return func(path, s string) (string, error) {
		return strings.Replace(s, old, new, -1), nil
	}

}

func fixDates(path, s string) (string, error) {
	dateRe := regexp.MustCompile(`(date):\s*(.*)\s*\n`)

	// Make text dates in front matter date into proper YAML dates.
	var err error
	s = dateRe.ReplaceAllStringFunc(s, func(s string) string {
		m := dateRe.FindAllStringSubmatch(s, -1)
		key, val := m[0][1], m[0][2]
		var tt time.Time

		tt, err = time.Parse("Monday, January 2, 2006", val)
		if err != nil {
			err = fmt.Errorf("%s: %s", key, err)
			return ""
		}

		return fmt.Sprintf("%s: %s\n", key, tt.Format("2006-01-02"))
	})

	return s, err
}
