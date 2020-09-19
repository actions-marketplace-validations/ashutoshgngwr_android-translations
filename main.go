package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/pkg/errors"
	"github.com/spf13/pflag"
)

// doNotTranslateFileName as recognised by the Android Developer Tools
// http://tools.android.com/recent/non-translatablestrings
const doNotTranslateFileName = "donottranslate.xml"

// xmlTranslatable is a generic struct that can be embedded in other structs
// to parse values for 'translatable' attribute
type xmlTranslatable struct {
	Translatable string `xml:"translatable,attr"`
}

// IsTranslatable returns false if the value of 'Translatable' attr was set
// to 'false'. Returns true otherwise.
func (res *xmlTranslatable) IsTranslatable() bool {
	return !strings.EqualFold("false", res.Translatable)
}

// xmlStringResources declares data structure for unmarshalling 'resources' tag in
// Android values XML files.
type xmlStringResources struct {
	xml.Name     `xml:"resources"`
	Strings      []xmlStringResource      `xml:"string"`
	StringArrays []xmlStringArrayResource `xml:"string-array"`
}

// xmlStringResource declares data structure for unmarshalling 'string' tags in Android
// values XML files.
type xmlStringResource struct {
	Name         string    `xml:"name,attr"`
	Value        string    `xml:",chardata"`
	LastModified time.Time `xml:"-"`
	xmlTranslatable
}

type xmlStringArrayResource struct {
	Name string `xml:"name,attr"`
	// since items have only the value, we can re-use xmlStringResource struct
	Items []xmlStringResource `xml:"item"`
	xmlTranslatable
}

// localeStringsMap declares the type to map locales => string_name => stringResource
type localeStringsMap map[string]map[string]xmlStringResource

// stringResource declares the output structure for a single string resource.
type stringResource struct {
	Name            string   `json:"name"`
	Value           string   `json:"value"`
	MissingLocales  []string `json:"missing_locales"`
	OutdatedLocales []string `json:"outdated_locales"`
}

// MissingLocalesString joins the MissingLocales slice using ", " separator
func (res stringResource) MissingLocalesString() string {
	if len(res.MissingLocales) == 0 {
		return "-"
	}

	return strings.Join(res.MissingLocales, ", ")
}

// OutdatesLocalesString joins the OutdatesLocales slice using ", " separator
func (res stringResource) OutdatedLocalesString() string {
	if len(res.OutdatedLocales) == 0 {
		return "-"
	}

	return strings.Join(res.OutdatedLocales, ", ")
}

// stringResources is a named type for stringResource slice that implements
// the sort.Interface for sorting slices.
type stringResources []stringResource

func (res stringResources) Len() int           { return len(res) }
func (res stringResources) Swap(i, j int)      { res[i], res[j] = res[j], res[i] }
func (res stringResources) Less(i, j int) bool { return res[i].Name < res[j].Name }

// defaultLocale declares the constant to identify default string resources (resources
// in 'values' [no suffix] directory)
const defaultLocale = "default"

var (
	projectDir      string // root directory of the Android Project
	outdatedLocales bool   // if true, also print potentially outdated locales
	outputFormat    string // output format, must be one of markdown or json
	markdownTitle   string // heading for markdown content
	githubActions   bool   // if true, also call setGitHubActionsOutput to set action output
)

func init() {
	pflag.CommandLine.SortFlags = false
	pflag.StringVar(&projectDir, "project-dir", ".", "Android Project's root directory")
	pflag.BoolVar(&outdatedLocales, "outdated-locales", true, "If true, find potentially outdated translations")
	pflag.StringVar(&outputFormat, "output-format", "json", "Output format. Must be 'json' or 'markdown'")
	pflag.StringVar(&markdownTitle, "markdown-title", "Android Translations", "Title for the Markdown content")
	pflag.BoolVar(&githubActions, "github-actions", false, "Indicates if the runtime is GitHub Actions")
	pflag.Parse()

	if outputFormat != "json" && outputFormat != "markdown" {
		fatal(fmt.Sprintf("unknow output format %s", outputFormat))
	}
}

func main() {
	valuesFiles, err := findValuesFiles(projectDir)
	if err != nil {
		fatal(err)
	}

	localeStrings, err := findTranslatableStrings(valuesFiles)
	if err != nil {
		fatal(err)
	}

	defaultStrings, ok := localeStrings[defaultLocale]
	if !ok { // shouldn't be true for valid input
		fatal("unable to find string resources for default locale")
	}

	report := make([]stringResource, 0)
	for _, str := range defaultStrings {
		strResource := stringResource{
			Name:            str.Name,
			Value:           strings.TrimSpace(str.Value),
			MissingLocales:  []string{},
			OutdatedLocales: []string{},
		}

		for locale := range localeStrings {
			if localeStr, ok := localeStrings[locale][str.Name]; !ok {
				strResource.MissingLocales = append(strResource.MissingLocales, locale)
			} else if localeStr.LastModified.Before(str.LastModified) {
				strResource.OutdatedLocales = append(strResource.OutdatedLocales, locale)
			}
		}

		if len(strResource.MissingLocales)+len(strResource.OutdatedLocales) > 0 {
			report = append(report, strResource)
		}
	}

	sort.Sort(stringResources(report))
	var output string
	switch outputFormat {
	case "json":
		output = mustRenderJSON(report)
		break
	case "markdown":
		output = mustRenderMarkdown(markdownTitle, report)
		break
	}

	if githubActions {
		setGitHubActionsOutput("report", output)
		fmt.Println()
	}

	fmt.Println(output)
}

// fatal is a convenience function that calls 'fmt.Println' with 'msg' followed by an
// 'os.Exit(1)' invocation.
func fatal(msg interface{}) {
	fmt.Fprintln(os.Stderr, "error:", msg)
	os.Exit(1)
}

// findValuesFiles finds XML files in 'path/**/*/values*'. This function should be
// compatible with cases where multiple resource directories are in use.
func findValuesFiles(path string) ([]string, error) {
	files, err := ioutil.ReadDir(path)
	if err != nil {
		return nil, errors.Wrapf(err, "unable to read directory %s", path)
	}

	valuesFiles := make([]string, 0)
	for _, file := range files {
		filePath := filepath.Join(path, file.Name())
		if isGitIgnored(path, filePath) {
			continue
		}

		if file.IsDir() {
			moreValuesFiles, err := findValuesFiles(filePath)
			if err != nil {
				return nil, err
			}

			valuesFiles = append(valuesFiles, moreValuesFiles...)
		} else {
			if isValuesFile(filePath) {
				valuesFiles = append(valuesFiles, filePath)
			}
		}
	}

	return valuesFiles, nil
}

// isValuesFile checks the prefix on the parent of the given path. It also checks
// the file extension of the path. If the file name is equal to doNotTranslateFileName,
// it returns false. If the prefix equals 'values' and file extension
// equals 'xml', it returns true. False otherwise.
func isValuesFile(path string) bool {
	if doNotTranslateFileName == filepath.Base(path) {
		return false
	}

	parent := filepath.Base(filepath.Dir(path))
	return strings.HasPrefix(parent, "values") && strings.EqualFold(".xml", filepath.Ext(path))
}

// findTranslatableStrings looks for '<string>' tags with '<resources>' tag as its root
// in given files. It parses all the string tags without 'translatable="fasle"' attribute.
// It returns a mapping of locale to their strings where locale is suffix of 'values-'.
// If no suffix is present, i.e. 'values', defaultLocale constant is used to identify those
// values.
func findTranslatableStrings(files []string) (localeStringsMap, error) {
	strResources := make(localeStringsMap, 0)
	for _, file := range files {
		content, err := ioutil.ReadFile(file)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to read file at %s", file)
		}

		resources := &xmlStringResources{}
		err = xml.Unmarshal(content, resources)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to parse XML file at %s", file)
		}

		locale := getLocaleForValuesFile(file)
		strResCount := len(resources.Strings) + len(resources.StringArrays)
		if _, ok := strResources[locale]; !ok && strResCount > 0 {
			strResources[locale] = map[string]xmlStringResource{}
		}

		for _, str := range resources.Strings {
			if !str.IsTranslatable() {
				continue
			}

			start, count, err := getLineRange(content, str.Value)
			if err == nil {
				str.LastModified, err = getLastModifiedTime(file, start, count)
			}

			if err != nil {
				fmt.Fprintln(os.Stderr, "warning:", err)
				str.LastModified = time.Now()
			}

			strResources[locale][str.Name] = str
		}

		for _, strArr := range resources.StringArrays {
			if !strArr.IsTranslatable() {
				continue
			}

			for i, strArrItem := range strArr.Items {
				strArrItem.Name = fmt.Sprintf("%s[%d]", strArr.Name, i)
				start, count, err := getLineRange(content, strArrItem.Value)
				if err == nil {
					strArrItem.LastModified, err = getLastModifiedTime(file, start, count)
				}

				if err != nil {
					fmt.Fprintln(os.Stderr, "warning:", err)
					strArrItem.LastModified = time.Now()
				}

				strResources[locale][strArrItem.Name] = strArrItem
			}
		}
	}

	return strResources, nil
}

// getLocaleForValuesFile returns the suffix after 'values-'. If no suffix is present,
// e.g. 'values', it returns the defaultLocale constant.
func getLocaleForValuesFile(path string) string {
	parent := filepath.Base(filepath.Dir(path))
	if strings.EqualFold(parent, "values") {
		return defaultLocale
	}

	split := strings.SplitN(parent, "-", 2)
	if len(split) < 2 { // edge case. shouldn't be true for valid input
		return defaultLocale
	}

	return split[1]
}

// isGitIgnored checks if the given path is ignored from being tracked by 'git'. 'workingDir'
// is used provide additional to 'git' command. It returns false, if 'workingDir' is not an
// ancestor of the given file path.
func isGitIgnored(workingDir, file string) bool {
	relFilePath, err := filepath.Rel(workingDir, file)
	if err != nil {
		return false
	}

	cmd := exec.Command("git", "check-ignore", relFilePath)
	cmd.Dir = workingDir
	if err := cmd.Run(); err != nil {
		return false
	}

	return true
}

// mustRenderJSON marshals the given value as JSON. It panics on encountering an error
// while marshaling JSON.
func mustRenderJSON(v interface{}) string {
	content, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(errors.Wrap(err, "failed to marshal content as JSON"))
	}

	return string(content)
}

// mustRenderMarkdown tries render markdown content using on a const template.
// If there is an error when rendering the template, it panics.
func mustRenderMarkdown(title string, data []stringResource) string {
	mdTemplate, err := template.New("markdown").Parse(`# {{ .title }}

{{ if eq .length 0 -}}
No missing {{- if eq .outdated_on true }} or outdated {{- end }} translations found.
{{ else -}}
{{ .table }}
{{- end }}
_Generated using [Android Translations][1] GitHub action._

[1]: https://github.com/ashutoshgngwr/android-translations
`)

	var content bytes.Buffer
	err = mdTemplate.Execute(&content, map[string]interface{}{
		"title":       title,
		"length":      len(data),
		"outdated_on": outdatedLocales,
		"table":       renderMarkdownTable(data),
	})

	if err != nil {
		panic(errors.Wrap(err, "unable to render data as markdown"))
	}

	return content.String()
}

// renderMarkdownTable pretty prints the slice of stringResource as Markdown
// table to be used with Markdown format.
func renderMarkdownTable(data []stringResource) string {
	var tableContent bytes.Buffer
	table := tablewriter.NewWriter(&tableContent)
	table.SetBorders(tablewriter.Border{Left: true, Right: true})
	table.SetCenterSeparator("|")

	header := []string{"#", "Name", "Default Value", "Missing Locales"}
	if outdatedLocales {
		header = append(header, "Potentially Outdated Locales")
	}

	table.SetHeader(header)
	for i, item := range data {
		row := []string{
			fmt.Sprintf("%d", 1+i),
			fmt.Sprintf("`%s`", item.Name),
			item.Value,
			item.MissingLocalesString(),
		}

		if outdatedLocales {
			row = append(row, item.OutdatedLocalesString())
		}

		table.Append(row)
	}

	table.Render()
	return tableContent.String()
}

// setGitHubActionsOutput sets the output variable for Github Actions runtime.
// This output can be used by other steps in a workflow.
func setGitHubActionsOutput(key, value string) {
	value = strings.ReplaceAll(value, "%", "%25")
	value = strings.ReplaceAll(value, "\r", "%0D")
	value = strings.ReplaceAll(value, "\n", "%0A")
	fmt.Printf("::set-output name=%s::%s\n", key, value)
}

// getLastModifiedTime returns the last modified time of the given line range in the
// given file using 'git blame'.
func getLastModifiedTime(file string, lineStart, lineCount int) (time.Time, error) {
	const errFmt = "unable to find last modified time, file: %q, start: %d, count: %d"
	const cmdFmt = "git blame -p -L %d,+%d %s | grep committer-time | awk '{ print $2 }'"

	var stdoutBuffer bytes.Buffer
	command := fmt.Sprintf(cmdFmt, lineStart, lineCount, filepath.Base(file))
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = filepath.Dir(file)
	cmd.Stdout = &stdoutBuffer
	if err := cmd.Run(); err != nil {
		return time.Time{}, errors.Wrapf(err, errFmt, file, lineStart, lineCount)
	}

	// should handle case where multiline blame returns multiple commits and thus
	// multiple committer-time fields
	output := strings.TrimSpace(stdoutBuffer.String())
	var latestTimestamp int64
	for _, timestampStr := range strings.Split(output, "\n") {
		timestamp, err := strconv.ParseInt(timestampStr, 10, 64)
		if err != nil {
			return time.Time{}, errors.Wrapf(err, errFmt, file, lineStart, lineCount)
		}

		if timestamp > latestTimestamp {
			latestTimestamp = timestamp
		}
	}

	return time.Unix(latestTimestamp, 0), nil
}

// getLineRange returns the line range of the first occurrence of 'searchTerm' in
// 'content'. 'searchTerm' can be a multiline string. It returns the following
// positional values
// 1. start: line number where searchTerm occurrence started
// 2. count: total line count of the searchTerm itself.
// 3. error: if the there was error in reading the file or find the search term
func getLineRange(fileContent []byte, searchTerm string) (int, int, error) {
	chunks := strings.Split(string(fileContent), searchTerm)
	if len(chunks) < 2 {
		const errFmt = "searchTerm: %q is not found"
		return 0, 0, fmt.Errorf(errFmt, searchTerm)
	}

	start := 1 + strings.Count(chunks[0], "\n")
	count := 1 + strings.Count(searchTerm, "\n")
	return start, count, nil
}
