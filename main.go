package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/pflag"
)

// stringResources declares data structure for unmarshalling 'resources' tag in
// Android values XML files.
type stringResources struct {
	xml.Name `xml:"resources"`
	Strings  []stringResource `xml:"string"`
}

// stringResource declares data structure for unmarshalling 'string' tags in Android
// values XML files.
type stringResource struct {
	Name         string `xml:"name,attr"`
	Translatable string `xml:"translatable,attr"`
	Locale       string `xml:"-"`
}

// localeStringsMap declares the type to map locales => string_name => stringResource
type localeStringsMap map[string]map[string]*stringResource

// defaultLocale declares the constant to identify default string resources (resources
// in 'values' [no suffix] directory)
const defaultLocale = "default"

var (
	projectDir string // root directory of the Android Project
)

func init() {
	pflag.StringVar(&projectDir, "project-dir", ".", "Android Project's root directory")
	pflag.Parse()
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

	missingTranslations := map[string][]string{}
	for str := range defaultStrings {
		for locale := range localeStrings {
			if _, ok := localeStrings[locale][str]; !ok {
				missingTranslations[str] = append(missingTranslations[str], locale)
			}
		}
	}

	content, err := json.Marshal(missingTranslations)
	if err != nil {
		fatal("unable to marshal missing translations data into JSON")
	}

	fmt.Println(string(content))
}

// fatal is a convenience function that calls 'fmt.Println' with 'msg' followed by an
// 'os.Exit(1)' invocation.
func fatal(msg interface{}) {
	fmt.Println("error:", msg)
	os.Exit(1)
}

// findValuesFiles finds XML files in 'path/**/*/values*'. This function should be
// compatible with cases where multiple resource directories are in use.
func findValuesFiles(path string) ([]string, error) {
	valuesFiles := make([]string, 0)
	err := filepath.Walk(path, func(filePath string, file os.FileInfo, err error) error {
		if isGitIgnored(path, filePath) {
			return nil
		}

		parent := filepath.Base(filepath.Dir(filePath))
		if strings.HasPrefix(parent, "values") {
			valuesFiles = append(valuesFiles, filePath)
		}

		return nil
	})

	return valuesFiles, errors.Wrapf(err, "unable to walk path %s", path)
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

		resources := &stringResources{}
		err = xml.Unmarshal(content, resources)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to parse XML file at %s", file)
		}

		locale := getLocaleForValuesFile(file)
		for _, str := range resources.Strings {
			if !strings.EqualFold(str.Translatable, "false") {
				if _, ok := strResources[locale]; !ok {
					strResources[locale] = map[string]*stringResource{}
				}

				strResources[locale][str.Name] = &str
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
