/* Copyright 2018 The Bazel Authors. All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

   http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package rosetta

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bazelbuild/bazel-gazelle/config"
	"github.com/bazelbuild/bazel-gazelle/language"
	"github.com/bazelbuild/bazel-gazelle/merger"
	"github.com/bazelbuild/bazel-gazelle/resolve"
	"github.com/bazelbuild/bazel-gazelle/rule"
	"github.com/bazelbuild/bazel-gazelle/testtools"
	"github.com/bazelbuild/bazel-gazelle/walk"

	bzl "github.com/bazelbuild/buildtools/build"
)

func TestGenerateRules(t *testing.T) {
	if runtime.GOOS == "windows" {
		// TODO(jayconrod): set up testdata directory on windows before running test
		if _, err := os.Stat("testdata"); os.IsNotExist(err) {
			t.Skip("testdata missing on windows due to lack of symbolic links")
		} else if err != nil {
			t.Fatal(err)
		}
	}

	c, lang, _ := testConfig(t, "testdata")

	walk.Walk(c, []config.Configurer{lang}, []string{"testdata"}, walk.VisitAllUpdateSubdirsMode, func(dir, rel string, c *config.Config, update bool, oldFile *rule.File, subdirs, regularFiles, genFiles []string) {
		isTest := false
		for _, name := range regularFiles {
			if name == "BUILD.want" {
				isTest = true
				break
			}
		}
		if !isTest {
			return
		}
		t.Run(rel, func(t *testing.T) {
			res := lang.GenerateRules(language.GenerateArgs{
				Config:       c,
				Dir:          dir,
				Rel:          rel,
				File:         oldFile,
				Subdirs:      subdirs,
				RegularFiles: regularFiles,
				GenFiles:     genFiles})
			if len(res.Empty) > 0 {
				t.Errorf("got %d empty rules; want 0", len(res.Empty))
			}
			f := rule.EmptyFile("test", "")
			for _, r := range res.Gen {
				r.Insert(f)
			}
			merger.FixLoads(f, lang.Loads())
			f.Sync()
			got := string(bzl.Format(f.File))
			wantPath := filepath.Join(dir, "BUILD.want")
			wantBytes, err := ioutil.ReadFile(wantPath)
			if err != nil {
				t.Fatalf("error reading %s: %v", wantPath, err)
			}
			want := string(wantBytes)

			if got != want {
				t.Errorf("GenerateRules %q: got:\n%s\nwant:\n%s", rel, got, want)
			}
		})
	})
}

func testConfig(t *testing.T, repoRoot string) (*config.Config, language.Language, []config.Configurer) {
	cexts := []config.Configurer{
		&config.CommonConfigurer{},
		&walk.Configurer{},
		&resolve.Configurer{},
	}
	lang := NewLanguage()
	c := testtools.NewTestConfig(t, cexts, []language.Language{lang}, []string{
		"-build_file_name=BUILD.old",
		"-repo_root=" + repoRoot,
	})
	cexts = append(cexts, lang)
	return c, lang, cexts
}
